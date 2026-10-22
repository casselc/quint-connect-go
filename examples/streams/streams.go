// Package streams deploys the pipeline (package pipeline) into a qenv
// simulation: a publisher and consumers as crashable processes, the
// partitioned log of package simlog with its faults and rebalances, and
// a serializable store for the projection. Every choice of that
// environment is made by one qenv.Chooser: Hegel, a seed, a fuzz input or
// a replay.
package streams

import (
	"context"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/examples/streams/simlog"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Config of a deployment and its workload.
type Config struct {
	Mutation pipeline.Mutation
	// Partitions of the log (default 2), Consumers in the group (default
	// 2), Keys published (default 2) and Events per key (default 3).
	Partitions, Consumers, Keys, Events int
	// NoCrashes keeps every process up (rebalances still happen).
	NoCrashes bool
}

func (c *Config) defaults() {
	if c.Partitions == 0 {
		c.Partitions = 2
	}
	if c.Consumers == 0 {
		c.Consumers = 2
	}
	if c.Keys == 0 {
		c.Keys = 2
	}
	if c.Events == 0 {
		c.Events = 3
	}
}

// Deployment is a running pipeline in a simulation.
type Deployment struct {
	Sim        *qenv.Sim
	Log        *simlog.Log
	Outbox     *qenv.Store // the publisher's database
	Projection *qenv.Store // the consumers'
	Keys       []string
}

// Deploy starts the publisher and the consumers in s, with Events events
// of each key in the outbox.
func Deploy(s *qenv.Sim, cfg Config) *Deployment {
	cfg.defaults()
	d := &Deployment{Sim: s, Log: simlog.New(s, "events", cfg.Partitions), Outbox: qenv.NewStore(s, "outbox", qenv.Serializable),
		Projection: qenv.NewStore(s, "projection", qenv.Serializable), Keys: pipeline.Keys(cfg.Keys)}
	for _, k := range d.Keys {
		for n := 1; n <= cfg.Events; n++ {
			d.Outbox.Load(fmt.Sprintf("outbox/%s/%06d", k, n), n)
		}
	}
	crash := !cfg.NoCrashes
	idempotent := cfg.Mutation != pipeline.NonIdempotentProducer
	s.Process("publisher", crash, func(ctx context.Context) {
		_ = pipeline.Publish(ctx, d.Log.Producer(idempotent), d.Outbox)
	})
	for i := 1; i <= cfg.Consumers; i++ {
		name := fmt.Sprintf("consumer-%d", i)
		s.Process(name, crash, func(ctx context.Context) {
			c := &pipeline.Consumer{Reader: d.Log.Member(ctx, name), DB: d.Projection, Mutation: cfg.Mutation}
			c.Run(ctx)
		})
	}
	return d
}

// Run deploys the pipeline with c making the choices, runs it for 10 s of
// virtual time with faults, settles for up to 60 s without, and records
// settle.
func Run(c qenv.Chooser, cfg Config) (*Deployment, error) {
	s := qenv.New(c)
	defer s.Close()
	d := Deploy(s, cfg)
	if err := s.Run(10 * time.Second); err != nil {
		return d, err
	}
	if err := s.Settle(60 * time.Second); err != nil {
		return d, err
	}
	qobs.Record(s.Context(), "settle", nil)
	return d, nil
}
