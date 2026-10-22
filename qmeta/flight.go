package qmeta

import (
	"context"
	"fmt"
	"io"
	"runtime/trace"
	"time"
)

// A Watch takes an execution trace when a running system enters the
// alert region: the runtime's flight recorder keeps the last seconds, and
// the Watch writes them out once the state has been in the region for For
// samples in a row. The storm then arrives with what every goroutine was
// doing as it began, which the alert rule (Region.Rules) cannot say.
//
//	roles := qrole.From(ctx)
//	w := &qmeta.Watch{Region: region, State: func() qmeta.State {
//		return qmeta.State{U: roles.Queue("api"), V: roles.Orbit("api")}
//	}, Snapshot: func(s qmeta.State) (io.WriteCloser, error) { return os.Create(...) }}
//	go w.Run(ctx, time.Second)
//
// It runs on the real clock, in the real process: not in a simulation.
type Watch struct {
	Region *Region
	// State is the system's (u, v) now, as qrole's gauges give it.
	State func() State
	// For is how many consecutive samples in the region fire it (default
	// 1). Gap is the least time between two snapshots (default 1 min).
	For int
	Gap time.Duration
	// Snapshot opens where a trace taken in state s goes; it is closed
	// after the trace is written.
	Snapshot func(s State) (io.WriteCloser, error)
	// Recorder configures the flight recorder (zero: the runtime's
	// defaults, seconds of history).
	Recorder trace.FlightRecorderConfig

	fr    *trace.FlightRecorder
	in    int
	last  time.Time
	Taken int // snapshots written
}

// Start starts the flight recorder. One may run per process.
func (w *Watch) Start() error {
	w.fr = trace.NewFlightRecorder(w.Recorder)
	if err := w.fr.Start(); err != nil {
		return fmt.Errorf("qmeta: flight recorder: %w", err)
	}
	return nil
}

// Stop stops the flight recorder.
func (w *Watch) Stop() {
	if w.fr != nil {
		w.fr.Stop()
	}
}

// Observe samples the state at now and writes a snapshot if the watch
// fires; it reports whether it did.
func (w *Watch) Observe(now time.Time) (bool, error) {
	s := w.State()
	if !w.Region.Contains(s) {
		w.in = 0
		return false, nil
	}
	w.in++
	gap := w.Gap
	if gap == 0 {
		gap = time.Minute
	}
	if w.in < max(w.For, 1) || (!w.last.IsZero() && now.Sub(w.last) < gap) {
		return false, nil
	}
	out, err := w.Snapshot(s)
	if err != nil {
		return false, err
	}
	_, err = w.fr.WriteTo(out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("qmeta: writing the flight recorder's trace: %w", err)
	}
	w.last = now
	w.Taken++
	return true, nil
}

// Run starts the recorder, samples every period until ctx ends, and stops
// it. It returns the first error writing a snapshot.
func (w *Watch) Run(ctx context.Context, every time.Duration) error {
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if _, err := w.Observe(now); err != nil {
				return err
			}
		}
	}
}
