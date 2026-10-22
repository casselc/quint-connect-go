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

	file      string
	line, col int
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.file != b.file {
			return a.file < b.file
		}
		if a.line != b.line {
			return a.line < b.line
		}
		return a.col < b.col
	})
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
//     run through the goroutine (qrun), but the task's context
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
	sortFindings(out)
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
			out = append(out, Finding{Pos: fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column), What: what, Fix: fix,
				file: pos.Filename, line: pos.Line, col: pos.Column})
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

// syncMethods are the sync.Mutex, RWMutex and WaitGroup methods qenv/weave
// rewrites, when called.
var syncMethods = map[string]bool{"Lock": true, "Unlock": true, "TryLock": true, "RLock": true, "RUnlock": true,
	"TryRLock": true, "Add": true, "Done": true, "Wait": true}

// LintWoven scans the Go files under dir, tests excluded, for what
// qenv/weave cannot bring into a simulation:
//   - a go statement not marked //qenv:go: its goroutine runs outside the
//     simulation;
//   - a lock or wait group method used as a value (st.mu.RUnlock passed or
//     returned, not called): weaving rewrites calls, so it would run the
//     real method on an object whose simulated state it never sees. Wrap
//     it in a function literal that calls it;
//   - a sync.Mutex, RWMutex or WaitGroup embedded in a struct: Orchestrion
//     matches a call by its receiver expression's type, so a promoted call
//     (t.Lock() on a struct embedding sync.Mutex) is not woven, and takes
//     the real lock. Name the field (mu sync.Mutex) and call t.mu.Lock().
//
// Method values are found by name (no type information), so a method of
// another type with one of those names is reported too: mark a deliberate
// one //qenv:ok.
func LintWoven(dir string) ([]Finding, error) {
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
		out = append(out, lintWovenFile(fset, f)...)
		return nil
	})
	sortFindings(out)
	return out, err
}

func lintWovenFile(fset *token.FileSet, f *ast.File) []Finding {
	marked := map[int]string{} // line -> directive on it
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			for _, d := range []string{"//qenv:go", "//qenv:ok"} {
				if c.Text == d || strings.HasPrefix(c.Text, d+" ") {
					marked[fset.Position(c.Pos()).Line] = d
				}
			}
		}
	}
	var out []Finding
	add := func(n ast.Node, what, fix string) {
		pos := fset.Position(n.Pos())
		out = append(out, Finding{Pos: fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column), What: what, Fix: fix,
			file: pos.Filename, line: pos.Line, col: pos.Column})
	}
	called := map[ast.Expr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			called[c.Fun] = true
		}
		return true
	})
	syncName := ""
	for _, im := range f.Imports {
		if im.Path.Value == `"sync"` {
			syncName = "sync"
			if im.Name != nil {
				syncName = im.Name.Name
			}
		}
	}
	embedsSync := func(e ast.Expr) (string, bool) {
		if st, ok := e.(*ast.StarExpr); ok {
			e = st.X
		}
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || syncName == "" {
			return "", false
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != syncName {
			return "", false
		}
		switch sel.Sel.Name {
		case "Mutex", "RWMutex", "WaitGroup":
			return sel.Sel.Name, true
		}
		return "", false
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			if t, ok := embedsSync(n.Type); ok && len(n.Names) == 0 && marked[fset.Position(n.Pos()).Line] != "//qenv:ok" {
				add(n, "embedded sync."+t+": calls promoted through the struct are not woven (weaving matches the receiver's type)",
					"name the field (mu sync."+t+") and call it through the name")
			}
		case *ast.GoStmt:
			line := fset.Position(n.Pos()).Line
			if marked[line-1] != "//qenv:go" && marked[line] != "//qenv:ok" {
				add(n, "go statement not marked //qenv:go: its goroutine runs outside the simulation",
					"mark it //qenv:go (the call must be a function literal), or //qenv:ok if it should stay outside")
			}
		case *ast.SelectorExpr:
			if syncMethods[n.Sel.Name] && !called[n] && marked[fset.Position(n.Pos()).Line] != "//qenv:ok" {
				add(n, "method value "+n.Sel.Name+" is not woven (weaving rewrites calls)",
					"wrap it in a function literal that calls it: func() { x."+n.Sel.Name+"() }")
			}
		}
		return true
	})
	return out
}
