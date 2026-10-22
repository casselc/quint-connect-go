package weave

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const aspects = `
aspects:
  - id: act
    join-point:
      function-body:
        directive: 'quint:action'
    advice:
      - prepend-statements:
          imports:
            context: context
            rec: example.com/rec
          template: |-
            {{- $ctx := .Function.ArgumentOfType "context.Context" -}}
            {{- $err := .Function.ResultOfType "error" -}}
            {{- $name := "" -}}
            {{- range .DirectiveArgs "quint:action" }}{{ if eq .Key "action" }}{{ $name = .Value }}{{ end }}{{ end -}}
            defer func() { rec.Record({{ if ne $ctx "" }}{{ $ctx }}{{ else }}context.TODO(){{ end }}, {{ printf "%q" $name }}, {{ if ne $err "" }}{{ $err }}{{ else }}nil{{ end }}
              {{- range .DirectiveArgs "quint:action" }}{{ if ne .Key "action" }}, {{ printf "%q" .Key }}, {{ .Value }}{{ end }}{{ end }}) }()
  - id: calls
    join-point:
      function-call: fmt.Println
    advice:
      - wrap-expression:
          template: x
`

const src = `package p

import (
	"context"
	"fmt"

	recorder "example.com/rec"
)

var _ = recorder.X

//quint:action action:deposit amount:n who:"f(a, b)"
func deposit(ctx context.Context, n int) error { return nil }

// other is not annotated.
func other() { fmt.Println() }

//quint:action action:noctx
func noctx(int) (int, error) {
	return 0, nil
}
`

func TestWeave(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "orchestrion.yml")
	if err := os.WriteFile(yml, []byte(aspects), 0o644); err != nil {
		t.Fatal(err)
	}
	as, skipped, err := Load(yml)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || len(skipped) != 1 || skipped[0].ID != "calls" {
		t.Fatalf("aspects %v, skipped %v", as, skipped)
	}
	out, err := File("/src/p.go", []byte(src), as)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		`func deposit(ctx context.Context, n int) (__weave_res0 error)`,
		`recorder.Record(ctx, "deposit", __weave_res0, "amount", n, "who", f(a, b))`,
		`func noctx(int) (__weave_res0 int, __weave_res1 error)`,
		`recorder.Record(context.TODO(), "noctx", __weave_res1)`,
	} {
		if strings.Contains(got, `rec "example.com/rec"`) {
			t.Error("example.com/rec imported twice")
		}
		if strings.Contains(got, `context "context"`) {
			t.Error("context imported twice")
		}
		if !strings.Contains(got, want) {
			t.Errorf("woven file lacks %s:\n%s", want, got)
		}
	}
	// it parses, and the original lines keep their numbers
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "/src/p.go", out, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	lines := map[string]int{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			lines[fn.Name.Name] = fset.Position(fn.Pos()).Line
		}
	}
	if lines["deposit"] != 13 || lines["other"] != 16 || lines["noctx"] != 19 {
		t.Errorf("functions at lines %v, want deposit 13, other 16, noctx 19:\n%s", lines, got)
	}
}
