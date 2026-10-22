package metatesting

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/jellevandenhooff/gosim/internal/gosimtool"
	"github.com/jellevandenhooff/gosim/internal/prettylog"
)

// A Chooser makes a run's choices from the runner's side: which goroutine
// runs next, which select case fires, and what the simulated program asks
// for with gosimruntime.Choose. A qenv.Chooser is a Chooser.
type Chooser interface {
	Choose(kind string, n int) int
}

// The protocol messages, copied from gosimruntime (choose.go). Keep in sync.
type remoteConfig struct {
	RunConfig
	Remote bool
}

type childMsg struct {
	Choose *struct {
		Kind string
		N    int
	}
	Emit   *string
	Result *RunResult
}

type choiceReply struct {
	Value int
}

// RunWith runs a test with chooser making its choices, in this goroutine,
// and calls emit with each line the test passes to gosimruntime.Emit.
// config.Seed is ignored: the seed for unbounded randomness is a choice too.
//
// A chooser may abort the run by panicking (Hegel does): RunWith stops the
// test binary, so that the next run starts a fresh one, and re-panics.
func (r *MetaT) RunWith(t testing.TB, config *RunConfig, chooser Chooser, emit func(line string)) (res *RunResult, err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			r.kill()
			panic(p)
		}
	}()

	if err := r.w.Encode(remoteConfig{RunConfig: *config, Remote: true}); err != nil {
		return nil, err
	}
	for {
		var msg childMsg
		if err := r.r.Decode(&msg); err != nil {
			r.kill()
			return nil, fmt.Errorf("metatest protocol: %w", err)
		}
		switch {
		case msg.Choose != nil:
			v := chooser.Choose(msg.Choose.Kind, msg.Choose.N)
			if err := r.w.Encode(choiceReply{Value: v}); err != nil {
				return nil, err
			}
		case msg.Emit != nil:
			if emit != nil {
				emit(*msg.Emit)
			}
		case msg.Result != nil:
			if msg.Result.Failed {
				b := new(bytes.Buffer)
				fmt.Fprintf(b, "\n> running %s\n", config.Test)
				w := prettylog.NewWriter(b)
				for _, line := range bytes.SplitAfter(msg.Result.LogOutput, []byte("\n")) {
					if len(line) > 0 {
						w.Write(line)
					}
				}
				t.Log(b.String())
			}
			return msg.Result, nil
		default:
			r.kill()
			return nil, fmt.Errorf("metatest protocol: empty message")
		}
	}
}

// Start returns a MetaT of its own for pkg, a test binary not shared with
// ForOtherPackage or other Starts: run several in parallel, one run at a
// time each. Close it when done.
func Start(t *testing.T, pkg string) (*MetaT, error) {
	return newRunner(gosimtool.GetPathForPrecompiledTestBinary(t, pkg))
}

// Close stops the test binary.
func (r *MetaT) Close() {
	if !r.dead {
		r.kill()
	}
}

// Alive reports whether the test binary still runs: a run a chooser
// aborted stops it (RunWith).
func (r *MetaT) Alive() bool { return !r.dead }

// kill stops the test binary and forgets it, so that ForOtherPackage starts
// a new one.
func (r *MetaT) kill() {
	r.dead = true
	r.cmd.Process.Kill()
	r.cmd.Wait()
	runnerMapMu.Lock()
	defer runnerMapMu.Unlock()
	for pkg, m := range runnerMap {
		if m == r {
			delete(runnerMap, pkg)
		}
	}
}
