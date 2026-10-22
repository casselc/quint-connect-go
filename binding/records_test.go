package binding

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Literal Record actions are found in files that import qobs (tests
// too), whatever the package's local name; actions through a constant
// are not.
func TestScanRecords(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.go", `package app

import (
	"context"

	obs "github.com/casselc/quint-connect-go/qobs"
)

const settle = "settle"

func deposit(ctx context.Context, r *obs.Recorder) {
	obs.Record(ctx, "deposit", nil, "amount", 1)
	r.Record(ctx, "withdraw", nil)
	obs.Record(ctx, settle, nil)
	obs.Record(ctx, `+"`depositt`"+`, nil)
}

var _ = func(ctx context.Context) { obs.Record(ctx, "init", nil) }
`)
	write("app_test.go", `package app

import (
	"context"

	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

func record(ctx context.Context) { qobs.Record(ctx, "transfer", nil) }

func TestRun(t *testing.T) { qtest.Record(t, "bank.yaml", nil) }
`)
	write("other.go", `package app

func other(l logger) { l.Record(nil, "deposit", nil) }
`)
	recs, err := ScanRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		m := ""
		if r.Method {
			m = " (method)"
		}
		got = append(got, r.Pos[strings.LastIndex(r.Pos, "/")+1:]+" "+r.Func+" "+r.Action+m)
	}
	want := []string{
		"app.go:12:18 deposit deposit",
		"app.go:13:16 deposit withdraw (method)",
		"app.go:15:18 deposit depositt",
		"app.go:18:53  init",
		"app_test.go:12:53 record transfer",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func bankModel() *Model {
	return &Model{Module: "bank", Actions: map[string][]Param{
		"init": nil, "step": nil, "deposit": nil, "withdraw": nil, "transfer": nil,
	}}
}

// An unknown literal action is reported with the nearest action and the
// model's list.
func TestLintRecords(t *testing.T) {
	src := `package app

import "github.com/casselc/quint-connect-go/qobs"

func f() {
	qobs.Record(nil, "deposit", nil)
	qobs.Record(nil, "depositt", nil)
	qobs.Record(nil, "close", nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	errs := LintRecords(bankModel(), RecordsOf(fset, f))
	want := []string{
		`app.go:7:19: f: Record(ctx, "depositt", ...): depositt is not an action of bank (did you mean deposit?); its actions: deposit, transfer, withdraw`,
		`app.go:8:19: f: Record(ctx, "close", ...): close is not an action of bank; its actions: deposit, transfer, withdraw`,
	}
	if strings.Join(errs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(errs, "\n"), strings.Join(want, "\n"))
	}
}

// LintSource counts an action a literal Record emits as emitted.
func TestLintSourceEmitted(t *testing.T) {
	b := &Binding{Actions: map[string]Action{"deposit": {}, "withdraw": {}}}
	recs := []RecordCall{{Pos: "app.go:1", Action: "deposit"}}
	errs, warns := b.LintSource(bankModel(), nil, recs)
	if len(errs) != 0 || len(warns) != 1 || !strings.HasPrefix(warns[0], "action withdraw is bound but neither") {
		t.Fatalf("errs %q, warns %q", errs, warns)
	}
}
