package qcover_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/casselc/quint-connect-go/qcover"
)

// A real environment's binary, built with -cover, ran retry; the
// simulation ran refund only. Compare says retry ran only for real (the
// simulation never leads there) and refund only in simulation.
func TestCompareRealWithSimulation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := t.TempDir()
	bin, covdir := filepath.Join(dir, "realapp"), filepath.Join(dir, "cov")
	if out, err := exec.Command("go", "build", "-cover", "-o", bin, "./testdata/realapp").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.Mkdir(covdir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(bin, "retry")
	run.Env = append(os.Environ(), "GOCOVERDIR="+covdir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	real, err := qcover.ReadGoCover(covdir)
	if err != nil {
		t.Fatal(err)
	}
	if r := real.Ratio(); r <= 0 || r >= 1 {
		t.Fatalf("ratio %v; want some statements run", r)
	}
	file := "qcover/testdata/realapp/main.go"
	sim := &qcover.Summary{Runs: 1, Hit: map[int]int{1: 1}, Blocks: []qcover.Block{
		{File: file, Line: 11, EndLine: 11, Func: "main.retry", Stmts: 1, ID: 0},
		{File: file, Line: 15, EndLine: 15, Func: "main.refund", Stmts: 1, ID: 1},
		{File: "elsewhere/x.go", Line: 1, Func: "x.F", Stmts: 1, ID: 2},
	}}
	g := qcover.Compare(sim, real)
	if len(g.RealOnly) != 1 || g.RealOnly[0].Func != "main.retry" || len(g.SimOnly) != 1 || g.SimOnly[0].Func != "main.refund" {
		t.Fatalf("gap:\n%s", g)
	}
	t.Log(g)
}
