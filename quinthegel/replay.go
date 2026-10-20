package quinthegel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/itf"
	"hegel.dev/go/hegel"
)

// Traces generates model traces once per test binary and Config (`quint run
// --mbt`, see connect.GenerateRun) and returns them decoded, for Replay.
func Traces(t testing.TB, c connect.Config) []*itf.Trace {
	t.Helper()
	key := fmt.Sprintf("%+v", c)
	if v, ok := traceCache.Load(key); ok {
		return v.([]*itf.Trace)
	}
	files, rc, err := connect.GenerateRun(context.Background(), c)
	if err != nil {
		t.Fatalf("quinthegel: generating traces: %v", err)
	}
	var out []*itf.Trace
	for _, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatalf("quinthegel: %v", err)
		}
		out = append(out, tr)
	}
	t.Logf("quinthegel: %d model traces from %s (seed %s)", len(out), c.Spec, rc.Seed)
	traceCache.Store(key, out)
	return out
}

var traceCache sync.Map

// ReplayOptions configure Replay.
type ReplayOptions struct {
	connect.ReplayOptions
	// Prefix lets Hegel choose how many steps of the trace to replay, so a
	// failure shrinks to the shortest failing prefix as well.
	Prefix bool
}

// Replay is one Hegel test case of model-driven testing: Hegel draws one of
// traces (and, with Prefix, a prefix of it), and the trace is replayed on
// the driver newDriver returns. newDriver gets the test case, so handlers
// can hegel.Draw the concrete details the model leaves abstract; Hegel then
// shrinks those along with the choice of trace. A failing step fails the
// test case with the step and, for state mismatches, the field-level diff.
//
//	traces := quinthegel.Traces(t, connect.Config{Spec: "bank.qnt", MaxSamples: 50, MaxSteps: 20})
//	hegel.Test(t, func(ht *hegel.T) {
//		quinthegel.Replay(ht, traces, func(tc hegel.TestCase) connect.Driver { return newDriver(tc) }, quinthegel.ReplayOptions{Prefix: true})
//	})
func Replay(tc hegel.TestCase, traces []*itf.Trace, newDriver func(tc hegel.TestCase) connect.Driver, opt ReplayOptions) {
	if len(traces) == 0 {
		tc.Errorf("quinthegel: no traces to replay")
	}
	idx := hegel.Draw(tc, hegel.Integers(0, len(traces)-1))
	tr := traces[idx]
	if opt.Prefix && len(tr.States) > 1 {
		n := hegel.Draw(tc, hegel.Integers(1, len(tr.States)-1))
		cut := *tr
		cut.States = tr.States[:n+1]
		tr = &cut
	}
	ro := opt.ReplayOptions
	ro.Trace = idx
	inner := ro.OnStep
	ro.OnStep = func(s *connect.Step) {
		tc.Note(fmt.Sprintf("model step %d: %s", s.Index, summary(s)))
		if inner != nil {
			inner(s)
		}
	}
	if err := connect.Replay(tr, newDriver(tc), ro); err != nil {
		var se *connect.StepError
		if errors.As(err, &se) && se.Kind != "trace" {
			tc.Errorf("quinthegel: %v\n%s", err, se.Step)
		}
		tc.Errorf("quinthegel: %v", err)
	}
}

func summary(s *connect.Step) string {
	out := s.Action
	if len(s.Picks) > 0 {
		out += "("
		first := true
		for _, k := range sortedKeys(s.Picks) {
			if !first {
				out += ", "
			}
			first = false
			out += k + "=" + s.Picks[k].String()
		}
		out += ")"
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
