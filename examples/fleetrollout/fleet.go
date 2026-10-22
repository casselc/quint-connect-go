// Package fleetrollout runs the rollout controller (package rollout)
// against a simulated fleet (package kubesim) in a qenv simulation: member
// clusters whose API servers fail requests and lose responses, whose
// Deployment status lags its spec, and a controller process that crashes
// and restarts. cmd/fleet-controller runs the same controller against
// real clusters (kwok) through multicluster-runtime.
package fleetrollout

import (
	"context"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/fleetrollout/kubesim"
	"github.com/casselc/quint-connect-go/examples/fleetrollout/rollout"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/sktrace"
)

// Config of a simulated rollout.
type Config struct {
	Members  int // default 3
	Mutation rollout.Mutation
	// NoCrashes keeps the controller up.
	NoCrashes bool
	// Workload, if set, is a production trace replayed onto the fleet as
	// another writer while the rollout runs (kubesim.Fleet.Replay), Speed
	// times faster (default 60: a trace's minute in a second).
	Workload *sktrace.Trace
	Speed    float64
}

// Target is the image rolled out; members start at web:v1.
const Target = "web:v2"

// Run rolls Target out across the simulated fleet, with c making the
// choices: 20 s of virtual time with faults, a settle of up to 30 s
// without, then a fresh read of every member (observe steps) and settle.
func Run(c qenv.Chooser, cfg Config) (*kubesim.Fleet, error) {
	if cfg.Members == 0 {
		cfg.Members = 3
	}
	s := qenv.New(c)
	defer s.Close()
	f := &kubesim.Fleet{}
	for i := 1; i <= cfg.Members; i++ {
		f.Members = append(f.Members, kubesim.NewCluster(s, fmt.Sprintf("m%d", i), "web:v1"))
	}
	if cfg.Workload != nil {
		if cfg.Speed == 0 {
			cfg.Speed = 60
		}
		f.Replay(s, cfg.Workload, cfg.Speed)
	}
	newController := func() *rollout.Controller {
		return &rollout.Controller{Clusters: f, Key: kubesim.Web, Image: Target, MaxUnavailable: 1, Mutation: cfg.Mutation}
	}
	// a level-based controller: reconcile, wait, repeat; a restart starts
	// a new Controller, with nothing remembered
	s.Process("controller", !cfg.NoCrashes, func(ctx context.Context) {
		ctl := newController()
		for {
			_, _ = ctl.Reconcile(ctx)
			qenv.Sleep(ctx, 200*time.Millisecond)
		}
	})
	if err := s.Run(20 * time.Second); err != nil {
		return f, err
	}
	if err := s.Settle(30 * time.Second); err != nil {
		return f, err
	}
	final := newController()
	for _, m := range f.Members {
		final.Observe(s.Context(), m.Name, m.Read())
	}
	qobs.Record(s.Context(), "settle", nil)
	return f, nil
}
