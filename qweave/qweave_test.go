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
// With -cover too: a block's coverage call goes before an advised
// statement that starts it, not inside its wrapped expression.
func TestWovenFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	for _, cover := range []bool{false, true} {
		r, err := qweave.Weave(qweave.Config{Dir: "testdata/sync", Cover: cover})
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
			t.Fatalf("cover=%v: %v\n%s", cover, err, out)
		}
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

// qrole's aspects (//quint:serve, //quint:operation, //quint:call) weave
// counting into a server and a client with no qrole calls of their own; the
// fixture's test fails unwoven.
func TestRolesFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/roles"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || len(r.Skipped) != 0 {
		t.Fatalf("woven %d files, skipped %v", len(r.Files), r.Skipped)
	}
	overlay, err := r.WriteOverlay(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-overlay="+overlay, "./...")
	cmd.Dir = "testdata/roles"
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// qenv/fault's aspects (prepend-statements and inject-declarations on one
// join point): //quint:fault methods return their package's own error or
// crash in a simulation, unnamed results are named for the advice, and
// every point is declared for coverage. The fixture's test fails unwoven.
func TestFaultFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/fault"})
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for name, b := range r.Files {
		if strings.HasSuffix(name, filepath.Join("store", "store.go")) {
			src = string(b)
		}
	}
	for _, want := range []string{
		`func (s *Store) Get(ctx context.Context, k string) (__qw_res0 string, __qw_res1 error) {`,
		`quintqenv.FaultPoint(ctx, "store.get", false, ErrUnavailable)`,
		`quintqenv.FaultPoint(ctx, "store.put", true)`,
		`func init() { quintqenv.DeclareFault("store.flush", "Flush") }`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("woven store.go lacks %s:\n%s", want, src)
		}
	}
	overlay, err := r.WriteOverlay(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-overlay="+overlay, "./...")
	cmd.Dir = "testdata/fault"
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// -cover instruments the main module's blocks for qcover, and the woven
// fixture attributes them to its steps.
func TestCoverFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/cover", Cover: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 {
		t.Fatalf("woven %d files, want store.go alone (test files are not instrumented)", len(r.Files))
	}
	for _, b := range r.Files {
		if src := string(b); !strings.Contains(src, "quintqcover.Hit(__qcov_") || !strings.Contains(src, "quintqcover.Register([]quintqcover.Block{") {
			t.Errorf("not instrumented:\n%s", src)
		}
	}
	overlay, err := r.WriteOverlay(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-overlay="+overlay, "./...")
	cmd.Dir = "testdata/cover"
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// -mutate weaves every mutant of the main module into one build, and the
// fixture's test turns each on in turn: each changes what it sees.
func TestMutateFixtureRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture")
	}
	r, err := qweave.Weave(qweave.Config{Dir: "testdata/mutate", Mutate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 {
		t.Fatalf("woven %d files, want limit.go alone (test files are not mutated)", len(r.Files))
	}
	for _, b := range r.Files {
		src := string(b)
		for _, want := range []string{"quintqmut.Cond(", "quintqmut.Pick(", "quintqmut.On(", "quintqmut.Register([]quintqmut.Mutant{"} {
			if !strings.Contains(src, want) {
				t.Errorf("no %s in:\n%s", want, src)
			}
		}
	}
	overlay, err := r.WriteOverlay(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-overlay="+overlay, "./...")
	cmd.Dir = "testdata/mutate"
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
