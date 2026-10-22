// Package tier runs the pipeline against a real log client (Kafka,
// Kinesis): the publisher relays an in-memory outbox through a Writer,
// consumers apply through Readers to an in-memory projection, until the
// projection has every event or the time is up. Steps go to the recorder
// in ctx, then settle is recorded: the run is checked against the same
// model and binding as the simulated ones.
package tier

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/qobs"
)

// Config of a run.
type Config struct {
	Mutation pipeline.Mutation
	// Consumers in the group (default 2), Keys (default 2), Events per key
	// (default 3); Timeout (default 20 s).
	Consumers, Keys, Events int
	Timeout                 time.Duration
}

// Result is a run's projection and how it ended.
type Result struct {
	Projection *pipeline.MemDB
	Keys       []string
	Complete   bool // every key's last event was applied before the timeout
}

// Run runs the pipeline: w writes, newReader(i) is consumer i's Reader
// and close function.
func Run(ctx context.Context, cfg Config, w pipeline.Writer, newReader func(i int) (pipeline.Reader, func(), error)) (*Result, error) {
	if cfg.Consumers == 0 {
		cfg.Consumers = 2
	}
	if cfg.Keys == 0 {
		cfg.Keys = 2
	}
	if cfg.Events == 0 {
		cfg.Events = 3
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	res := &Result{Projection: pipeline.NewMemDB(), Keys: pipeline.Keys(cfg.Keys)}
	outbox := pipeline.NewMemDB()
	if err := pipeline.Outbox(ctx, outbox, res.Keys, cfg.Events); err != nil {
		return nil, err
	}
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	for i := 1; i <= cfg.Consumers; i++ {
		r, closeR, err := newReader(i)
		if err != nil {
			return nil, fmt.Errorf("consumer %d: %w", i, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer closeR()
			(&pipeline.Consumer{Reader: r, DB: res.Projection, Mutation: cfg.Mutation}).Run(cctx)
		}()
	}
	if err := pipeline.Publish(ctx, w, outbox); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(cfg.Timeout)
	for time.Now().Before(deadline) && !res.Complete {
		res.Complete = true
		for _, k := range res.Keys {
			if pipeline.Version(ctx, res.Projection, k) < cfg.Events {
				res.Complete = false
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // the last batches' commits
	stop()
	wg.Wait()
	qobs.Record(ctx, "settle", nil)
	return res, nil
}
