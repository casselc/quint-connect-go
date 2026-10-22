package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/binding"
)

// scaffold -system prints a test that compiles as it is, and whose
// conformance test fails, saying what to do, until Run records steps.
func TestScaffoldSystem(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	b, err := binding.Load("../../examples/bank/bank.binding.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := binding.LoadModel("quint", b.SpecPath(), b.Module)
	if err != nil {
		t.Fatal(err)
	}
	src := systemTest("scaffolded", "../bank/bank.binding.yaml", b, m)
	for _, want := range []string{
		`qobs.Record(ctx, "deposit", err, "account", account, "amount", amount, "obs.balance", observedBalance)`,
		`qtest.Some(qtest.Violates("noOverdraft"))`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %s in:\n%s", want, src)
		}
	}
	// a package inside the module, so the test builds against it
	dir, err := os.MkdirTemp("../../examples", "_scaffolded") // _: no ./... pattern sees it
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "system_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Run records no steps yet") {
		t.Fatalf("want TestConforms to fail asking for steps, got (%v):\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- SKIP: TestMutantIsCaught") {
		t.Fatalf("want it built and the mutant test skipped:\n%s", out)
	}
}
