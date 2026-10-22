//go:build !sim

package qgosim

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/jellevandenhooff/gosim/metatesting"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// pool keeps idle test binaries by package, so that parallel runs
// (qtest.Explore) each have one.
var pool struct {
	sync.Mutex
	idle map[string][]*metatesting.MetaT
}

func get(t *testing.T, pkg string) (*metatesting.MetaT, error) {
	pool.Lock()
	for n := len(pool.idle[pkg]); n > 0; n-- {
		mt := pool.idle[pkg][n-1]
		pool.idle[pkg] = pool.idle[pkg][:n-1]
		if mt.Alive() { // not closed by the end of the test that started it
			pool.Unlock()
			return mt, nil
		}
	}
	pool.Unlock()
	mt, err := metatesting.Start(t, pkg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(mt.Close)
	return mt, nil
}

func put(pkg string, mt *metatesting.MetaT) {
	if !mt.Alive() {
		return
	}
	pool.Lock()
	defer pool.Unlock()
	if pool.idle == nil {
		pool.idle = map[string][]*metatesting.MetaT{}
	}
	pool.idle[pkg] = append(pool.idle[pkg], mt)
}

// Run runs the gosim test named test, in the package pkg (an import path),
// with c making every choice of the run, and returns the steps it recorded
// (Sink). env is the simulated test's environment ("KEY=value"). The
// package's test binary is built (gosim build-tests) the first time.
//
// A chooser that aborts the run by panicking (Hegel's) stops the binary,
// and the panic goes on: the next run starts another.
func Run(t *testing.T, pkg, test string, c qenv.Chooser, env ...string) ([]qtrace.Step, error) {
	t.Helper()
	mt, err := get(t, pkg)
	if err != nil {
		return nil, err
	}
	defer put(pkg, mt)
	var log bytes.Buffer
	res, err := mt.RunWith(t, &metatesting.RunConfig{Test: test, ExtraEnv: env}, c, func(line string) { log.WriteString(line) })
	if err != nil {
		return nil, err
	}
	steps, err := qtrace.ReadJSONL(&log)
	if err != nil {
		return nil, fmt.Errorf("qgosim: the steps %s sent: %w", test, err)
	}
	if res.Failed {
		return steps, fmt.Errorf("qgosim: the simulated %s failed: %s (its log is above)", test, res.Err)
	}
	return steps, nil
}

// Scenario is Run as a qtest.Explore scenario: the steps go to the
// recorder in the chooser's context. A run that fails is an error of t.
func Scenario(t *testing.T, pkg, test string, env ...string) func(qenv.Chooser) {
	return func(c qenv.Chooser) {
		steps, err := Run(t, pkg, test, c, env...)
		if err != nil {
			t.Error(err)
		}
		rec := qobs.From(qenv.ContextOf(c))
		if rec == nil {
			t.Error("qgosim: Scenario's chooser carries no recorder (qenv.WithContext, qobs.WithRecorder)")
			return
		}
		rec.Add(steps...)
	}
}
