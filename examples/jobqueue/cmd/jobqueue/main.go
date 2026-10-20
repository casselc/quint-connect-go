// Command jobqueue runs the jobqueue example: a queue service, concurrent
// workers whose jobs sometimes outlast their lease, and a restart of the
// queue half-way through, recording the model steps:
//
//	jobqueue -jobs 30 -workers 3 -steps steps.jsonl
//	quintgo validate -binding jobqueue.binding.yaml steps.jsonl                          # conforms
//	quintgo validate -binding jobqueue.binding.yaml -invariants exactlyOnce steps.jsonl  # where a job ran twice
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/examples/jobqueue/queue"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	jobs := flag.Int("jobs", 30, "jobs to enqueue")
	workers := flag.Int("workers", 3, "workers (at most 4: the binding names w1..w4)")
	ttl := flag.Duration("ttl", 20*time.Millisecond, "lease time-to-live")
	slow := flag.Float64("slow", 0.15, "fraction of jobs that take longer than a lease")
	steps := flag.String("steps", "steps.jsonl", "native step log to write")
	mutation := flag.String("mutation", "", "inject a bug: ack-any-lease | forget-done")
	seed := flag.Uint64("seed", 1, "random seed")
	flag.Parse()

	m := map[string]queue.Mutation{"": queue.Faithful, "ack-any-lease": queue.AckAnyLease, "forget-done": queue.ForgetDone}[*mutation]
	sink, err := qobs.FileSink(*steps)
	if err != nil {
		log.Fatal(err)
	}
	qobs.SetGlobal(qobs.NewRecorder(sink))

	svc := queue.New(queue.Config{LeaseTTL: *ttl, Mutation: m})
	r := rand.New(rand.NewPCG(*seed, 0))
	var mu sync.Mutex
	performed := map[int]int{}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 1; i <= *workers; i++ {
		w := &queue.Worker{Name: fmt.Sprintf("w%d", i), Svc: svc, Perform: func(ctx context.Context, job int) {
			mu.Lock()
			d := time.Duration(r.IntN(int(*ttl / 4)))
			if r.Float64() < *slow {
				d = *ttl * 2 // outlasts the lease: the job is leased again meanwhile
			}
			performed[job]++
			mu.Unlock()
			time.Sleep(d)
		}}
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	for j := 1; j <= *jobs; j++ {
		if err := svc.Enqueue(ctx, j); err != nil {
			log.Fatal(err)
		}
		if j == *jobs/2 {
			time.Sleep(*ttl)
			svc.Restart()
			log.Print("queue restarted")
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		all := true
		for _, st := range svc.Status() {
			all = all && st == "done"
		}
		if all {
			break
		}
		time.Sleep(*ttl)
	}
	cancel()
	wg.Wait()
	if err := sink.Close(); err != nil {
		log.Fatal(err)
	}
	twice := 0
	for _, n := range performed {
		if n > 1 {
			twice++
		}
	}
	log.Printf("%d jobs, %d performed more than once; steps in %s", *jobs, twice, *steps)
}
