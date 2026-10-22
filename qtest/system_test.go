package qtest

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
)

// failTB records failures instead of failing the test, and stops like t.
type failTB struct {
	testing.TB
	errs   []string
	failed bool
}

func (f *failTB) Helper() {}
func (f *failTB) Errorf(format string, a ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, a...))
	f.failed = true
}
func (f *failTB) Fatalf(format string, a ...any)  { f.Errorf(format, a...); runtime.Goexit() }
func (f *failTB) FailNow()                        { f.failed = true; runtime.Goexit() }
func (f *failTB) Failed() bool                    { return f.failed }
func (f *failTB) Logf(format string, args ...any) {}

// A system whose Run fails fails the exploration, naming the seed, before
// any run is checked.
func TestSystemRunError(t *testing.T) {
	f := &failTB{TB: t}
	sys := System{Binding: "../validate/testdata/account.yaml", Run: func(c qenv.Chooser) error {
		return errors.New("no database")
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sys.Explore(f, ExploreConfig{Seeds: 2, Parallel: 1})
	}()
	<-done
	if len(f.errs) != 2 || !strings.Contains(f.errs[0], "seed 1 did not run: no database") {
		t.Errorf("errors %q", f.errs)
	}
}
