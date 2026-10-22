// Package hello is the smallest simulated system checked against a model:
// a counter service reached over a network that loses requests and
// responses and delivers some requests twice, and a client that retries
// until it hears back. The service dedupes by request id, so every request
// counts once (model/hello.qnt). The NoDedupe mutant counts a retried or
// duplicated request again.
package hello

import (
	"context"
	"errors"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// NoDedupe counts every request it receives, including retries.
	NoDedupe
)

// Requests is how many requests the client sends (ids 1..Requests, the
// model's REQUESTS).
const Requests = 3

// Counter counts requests.
type Counter struct {
	Mutation Mutation
	seen     map[int]bool
	count    int
}

// Add handles request id: counted the first time, acknowledged again
// after. Each is the model step of that name, with the count it leaves.
func (c *Counter) Add(ctx context.Context, id int) (int, error) {
	if c.seen[id] && c.Mutation != NoDedupe {
		qobs.Record(ctx, "replay", nil, "id", id, "obs.count", c.count)
		return c.count, nil
	}
	c.seen[id] = true
	c.count++
	qobs.Record(ctx, "apply", nil, "id", id, "obs.count", c.count)
	return c.count, nil
}

// Send calls the service until it answers, waiting timeout for each
// attempt: a lost request or response looks the same to the client, so it
// retries both, and the service sees the request again.
func Send(ctx context.Context, svc *qenv.Service[int, int], id int, timeout time.Duration) error {
	for attempt := 0; attempt < 10; attempt++ {
		_, err := svc.Call(ctx, id, timeout)
		if !errors.Is(err, qenv.ErrTimeout) {
			return err
		}
	}
	return errors.New("hello: no answer after 10 attempts")
}

// Run deploys the counter and one client on c and runs them until quiet.
func Run(c qenv.Chooser, m Mutation) error {
	s := qenv.New(c)
	defer s.Close()
	counter := &Counter{Mutation: m, seen: map[int]bool{}}
	svc := qenv.NewService(s, "counter", nil, counter.Add)
	var err error
	s.Spawn("client", func(ctx context.Context) {
		for id := 1; id <= Requests && err == nil; id++ {
			err = Send(ctx, svc, id, 100*time.Millisecond)
		}
	})
	if rerr := s.Run(time.Minute); rerr != nil {
		return rerr
	}
	return err
}
