package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lint -binding -src reports a literal qobs.Record action the model
// lacks, with the nearest action, and counts the ones it has as emitted.
func TestLintRecords(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	dir := t.TempDir()
	src := `package app

import (
	"context"

	"github.com/casselc/quint-connect-go/qobs"
)

func pay(ctx context.Context) {
	qobs.Record(ctx, "deposit", nil)
	qobs.Record(ctx, "withdraw", nil)
	qobs.Record(ctx, "transfr", nil)
}
`
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	code, err := lint([]string{"-binding", "../../examples/bank/bank.binding.yaml", "-src", dir})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"3 literal Record calls under",
		`app.go:12:19: pay: Record(ctx, "transfr", ...): transfr is not an action of bank (did you mean transfer?); its actions: deposit, transfer, withdraw`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "warning: action deposit") {
		t.Errorf("deposit is recorded, but warned as emitted by nothing:\n%s", out)
	}
}
