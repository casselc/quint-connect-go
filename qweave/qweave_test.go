package qweave_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qweave"
)

// qenv/weave's aspects (method-call join points, //qenv:go) and the
// escapes Orchestrion misses: a method value, promoted calls through an
// embedded lock.
func TestWeaveSyncFixture(t *testing.T) {
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/sync"})
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for name, b := range r.Files {
		if strings.HasSuffix(name, filepath.Join("guarded", "guarded.go")) {
			src = string(b)
		}
	}
	for _, want := range []string{
		`weave.RLock(&s.mu)`,
		`return func() func()  { __qw_r := &(s.mu); return func()  { weave.RUnlock(&(*__qw_r)) } }()`, // the method value
		`weave.MutexLock(&(c).Mutex)`, // the promoted call, on the embedded field
		`go weave.Go(func() {`,
		`defer weave.Done(wg)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("woven guarded.go lacks %s:\n%s", want, src)
		}
	}
	if len(r.Skipped) != 0 {
		t.Errorf("skipped: %v", r.Skipped)
	}
}

// The woven fixture runs in a qenv simulation: tasks sleep inside the
// critical sections of the embedded lock and the method-value unlock, and
// no increment is lost. Unwoven, the real locks block qenv's scheduler.
func TestWovenFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/sync"})
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := r.WriteOverlay(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-overlay="+overlay, "./...")
	cmd.Dir = "testdata/sync"
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// //quint:action (qobs's function-body aspect) on a function with unnamed
// results: they are named for the advice, as Orchestrion names them, and
// positions stay on the original lines.
func TestFunctionBodyAdvice(t *testing.T) {
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/action"})
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for name, b := range r.Files {
		if strings.HasSuffix(name, "bank.go") {
			src = string(b)
		}
	}
	for _, want := range []string{
		`func (b *Bank) Deposit(ctx context.Context, acct string, n int) (__qw_res0 error) {`,
		`quintqobs.Record(ctx, "deposit", __qw_res0,`,
		`"amount", n,`,
		"//line ",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("woven bank.go lacks %s:\n%s", want, src)
		}
	}
}
