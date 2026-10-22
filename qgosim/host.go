//go:build !sim

package qgosim

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jellevandenhooff/gosim/metatesting"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/qweave"
)

var weaving struct {
	sync.Once
	err error
}

// weave weaves the module once per test process, before the first test
// binary is built: qweave applies the aspects the module's gosim.tool.go
// (else orchestrion.tool.go) names, and the translator reads the woven
// files from GOSIM_OVERLAY. A GOSIM_OVERLAY already set is used as is.
func weave() error {
	weaving.Do(func() {
		if os.Getenv("GOSIM_OVERLAY") != "" {
			return
		}
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			weaving.err = err
			return
		}
		dir := filepath.Dir(strings.TrimSpace(string(out)))
		r, err := qweave.Weave(qweave.Config{Dir: dir, GOSIM: true, Tags: []string{"sim", "purego"}})
		if err != nil {
			weaving.err = fmt.Errorf("qgosim: weaving %s: %w", dir, err)
			return
		}
		if len(r.Files) == 0 {
			return
		}
		tmp, err := os.MkdirTemp("", "qgosim-weave")
		if err != nil {
			weaving.err = err
			return
		}
		path, err := r.WriteOverlay(tmp)
		if err != nil {
			weaving.err = err
			return
		}
		os.Setenv("GOSIM_OVERLAY", path)
	})
	return weaving.err
}

// pool keeps idle test binaries by package, so that parallel runs
// (qtest.Explore) each have one.
var pool struct {
	sync.Mutex
	idle map[string][]*metatesting.MetaT
}

func get(t *testing.T, pkg string) (*metatesting.MetaT, error) {
	if err := weave(); err != nil {
		return nil, err
	}
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

// entropic hands the simulation chunks of entropy (qenv.EntropyOf) for
// its choices other than faults: a round trip per chunk, not per choice.
type entropic struct {
	qenv.Chooser
	e func(n int) []byte
}

func (c entropic) Entropy(n int) []byte { return c.e(n) }

// Run runs the gosim test named test, in the package pkg (an import path),
// with c making every choice of the run (from c's entropy, if it has any:
// qenv.EntropyOf), and returns the steps it recorded
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
	var mc metatesting.Chooser = c
	if e := qenv.EntropyOf(c); e != nil {
		mc = entropic{c, e}
	}
	res, err := mt.RunWith(t, &metatesting.RunConfig{Test: test, ExtraEnv: env}, mc, func(line string) { log.WriteString(line) })
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
