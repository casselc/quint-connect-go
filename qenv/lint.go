package qenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A Finding is code in a simulation that escapes it.
type Finding struct {
	Pos  string // file:line:col
	What string
	Fix  string
}

func (f Finding) String() string { return f.Pos + ": " + f.What + ": " + f.Fix }

// Lint scans the Go files under dir that import qenv (simulation code),
// tests excluded, for code that escapes the simulation:
//   - goroutines started with `go`: outside the scheduler, so runs are
//     not reproducible, and a step they record has no run (qobs.Strict
//     panics);
//   - the wall clock (time.Now, Since, Until, Sleep, After, AfterFunc, Tick,
//     NewTimer, NewTicker): virtual time does not pass for it;
//   - steps recorded with context.Background() or TODO(): they find their
//     run through the goroutine (qobs.SetFallback), but the task's context
//     says so directly, and keeps working if the code moves to a goroutine
//     of its own.
//
// A line with a //qenv:ok comment is exempt, for code that runs outside
// the simulation on purpose (a real server next to a simulated client).
func Lint(dir string) ([]Finding, error) {
	var out []Finding
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != dir && (d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		out = append(out, lintFile(fset, f)...)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out, err
}

const qenvPath = "github.com/casselc/quint-connect-go/qenv"

var wallClock = map[string]string{
	"Now": "qenv.Now(ctx) or Sim.Now", "Since": "qenv.Now(ctx).Sub(t)", "Until": "t.Sub(qenv.Now(ctx))",
	"Sleep": "qenv.Sleep(ctx, d)", "After": "qenv.Sleep, or a Signal with a deadline", "AfterFunc": "qenv.Go with qenv.Sleep",
	"Tick": "a loop with qenv.Sleep", "NewTimer": "qenv.Sleep, or a Signal with a deadline", "NewTicker": "a loop with qenv.Sleep",
}

func lintFile(fset *token.FileSet, f *ast.File) []Finding {
	names := map[string]string{} // import path -> local name
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		names[p] = name
	}
	if _, ok := names[qenvPath]; !ok || f.Name.Name == "qenv" {
		return nil
	}
	exempt := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//qenv:ok") {
				exempt[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	var out []Finding
	add := func(n ast.Node, what, fix string) {
		pos := fset.Position(n.Pos())
		if !exempt[pos.Line] {
			out = append(out, Finding{Pos: fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column), What: what, Fix: fix})
		}
	}
	sel := func(e ast.Expr, pkgPath string) (string, bool) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		id, ok := s.X.(*ast.Ident)
		if !ok || names[pkgPath] == "" || id.Name != names[pkgPath] {
			return "", false
		}
		return s.Sel.Name, true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			add(n, "goroutine started with `go`, outside the simulation", "start it with qenv.Go(ctx, name, fn)")
		case *ast.CallExpr:
			if fn, ok := sel(n.Fun, "time"); ok && wallClock[fn] != "" {
				add(n, "time."+fn+" reads the wall clock, which does not move in a simulation", "use "+wallClock[fn])
			}
			if fn, ok := sel(n.Fun, "github.com/casselc/quint-connect-go/qobs"); ok && fn == "Record" && len(n.Args) > 0 {
				if c, ok := n.Args[0].(*ast.CallExpr); ok {
					if bg, ok := sel(c.Fun, "context"); ok && (bg == "Background" || bg == "TODO") {
						add(n, "step recorded with context."+bg+"()", "record through the task's context, or Sim.Context() outside tasks")
					}
				}
			}
		}
		return true
	})
	return out
}
