// Package qanalysis has quint-connect-go's checks as go/analysis passes:
// what escapes a qenv simulation (Sim), what qenv/weave cannot rewrite
// (Weave), and //quint:action annotations against their binding (Action).
// They run in a multichecker (cmd/quintvet, also usable as `go vet
// -vettool`) and in golangci-lint as a module plugin (package golangci).
//
// Sim and Weave wrap the checks of `quintgo lint -sim` and `-weave`
// (package qlint), so the CLI and the analyzers report the same findings.
// This is its own module because the core keeps to OTel, yaml.v3 and
// Hegel, and x/tools is none of them.
package qanalysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/casselc/quint-connect-go/binding"
	"github.com/casselc/quint-connect-go/qlint"
)

// All are the analyzers, for a multichecker.
var All = []*analysis.Analyzer{Sim, Weave, Action}

// Sim reports code in files that import qenv that escapes the simulation:
// go statements, the wall clock, steps recorded with a background context
// (qlint.SimFile). Test files are checked, as by `quintgo lint -sim`: a
// System's Run closure lives in one.
var Sim = &analysis.Analyzer{
	Name: "quintsim",
	Doc:  "report code that escapes a qenv simulation (go statements, the wall clock, steps recorded with context.Background)",
	URL:  "https://github.com/casselc/quint-connect-go/tree/main/qanalysis",
	Run: func(pass *analysis.Pass) (any, error) {
		return nil, report(pass, qlint.SimFile, true)
	},
}

// Weave reports what qenv/weave cannot bring into a simulation: unmarked
// go statements, lock methods used as values, embedded sync types
// (qlint.WovenFile). Run it on packages built with qenv/weave's aspects.
var Weave = &analysis.Analyzer{
	Name: "quintweave",
	Doc:  "report what qenv/weave cannot rewrite (unmarked go statements, sync method values, embedded sync types)",
	URL:  "https://github.com/casselc/quint-connect-go/tree/main/qanalysis",
	Run: func(pass *analysis.Pass) (any, error) {
		return nil, report(pass, qlint.WovenFile, false)
	},
}

func report(pass *analysis.Pass, check func(*token.FileSet, *ast.File) []qlint.Finding, tests bool) error {
	for _, f := range pass.Files {
		if !tests && strings.HasSuffix(pass.Fset.File(f.Pos()).Name(), "_test.go") {
			continue
		}
		for _, x := range check(pass.Fset, f) {
			pass.Reportf(x.At, "%s: %s", x.What, x.Fix)
		}
	}
	return nil
}

// Action checks //quint:action annotations. Without a binding: each names
// its action, and its expressions use only the function's parameters,
// receiver and named results (they are evaluated on return, where its
// locals are out of scope). With -binding: each names an action of the
// binding's model and records every field the binding's templates for it
// use (binding.LintAnnotations), and every literal action passed to
// qobs.Record or Recorder.Record is one of the model's (binding.LintRecords;
// loading the model runs quint).
var Action = &analysis.Analyzer{
	Name: "quintaction",
	Doc:  "check //quint:action annotations: the action is named, expressions use only parameters, the receiver and named results, and (with -binding) the binding's model has the action and its fields are recorded, as it has every literal qobs.Record action",
	URL:  "https://github.com/casselc/quint-connect-go/tree/main/qanalysis",
	Run:  runAction,
}

var bindingPath, quintBin string

func init() {
	Action.Flags.StringVar(&bindingPath, "binding", "", "binding file to check annotations against (empty: no binding checks)")
	Action.Flags.StringVar(&quintBin, "quint", "quint", "quint binary, to load the binding's model")
}

// notExpressions are the directive's keys that are not Go expressions.
var notExpressions = map[string]bool{"when": true}

func runAction(pass *analysis.Pass) (any, error) {
	var lint func(binding.Annotation) []string
	var model *binding.Model
	if bindingPath != "" {
		b, m, err := loadBinding(bindingPath, quintBin)
		if err != nil {
			return nil, err
		}
		lint = func(a binding.Annotation) []string {
			errs, _ := b.LintAnnotations(m, []binding.Annotation{a})
			return errs
		}
		model = m
	}
	for _, f := range pass.Files {
		if model != nil {
			checkRecords(pass, model, f)
		}
		for _, a := range binding.AnnotationsOf(pass.Fset, f) {
			if a.Action == "" {
				pass.Reportf(a.At, "//quint:action on %s without action:NAME: name the model action the step is", a.Func)
				continue
			}
			checkScope(pass, a)
			if lint != nil {
				for _, e := range lint(a) {
					pass.Reportf(a.At, "%s", strings.TrimPrefix(e, a.Pos+": "))
				}
			}
		}
	}
	return nil, nil
}

// checkRecords reports the literal actions of f's qobs.Record and
// Recorder.Record calls that model lacks. The scan is quintgo lint's
// (binding.RecordsOf); types confirm each call is qobs's.
func checkRecords(pass *analysis.Pass, m *binding.Model, f *ast.File) {
	for _, r := range binding.RecordsOf(pass.Fset, f) {
		if !isQobsRecord(typeutil.Callee(pass.TypesInfo, r.Call)) {
			continue
		}
		for _, e := range binding.LintRecords(m, []binding.RecordCall{r}) {
			pass.Reportf(r.At, "%s", strings.TrimPrefix(e, r.Pos+": "))
		}
	}
}

// isQobsRecord reports whether obj is qobs.Record or (*qobs.Recorder).Record.
func isQobsRecord(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	return ok && fn.Name() == "Record" && fn.Pkg() != nil && fn.Pkg().Path() == "github.com/casselc/quint-connect-go/qobs"
}

// checkScope reports the names in a's expressions that are not in the
// function's scope (parameters, receiver, results) or an enclosing one
// (package, universe), declared before the body: go/types puts the
// body's top-level locals in the parameters' scope, and the advice
// cannot see them.
func checkScope(pass *analysis.Pass, a binding.Annotation) {
	scope := pass.TypesInfo.Scopes[a.Decl.Type]
	if scope == nil || a.Decl.Body == nil {
		return
	}
	keys := make([]string, 0, len(a.Keys))
	for k := range a.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if notExpressions[k] {
			continue
		}
		e, err := parser.ParseExpr(a.Keys[k])
		if err != nil {
			pass.Reportf(a.At, "//quint:action %s: %s:%s is not a Go expression: %v (quote a value with spaces: %s:\"f(a, b)\")", a.Action, k, a.Keys[k], err, k)
			continue
		}
		for _, name := range freeNames(e) {
			if _, obj := scope.LookupParent(name, a.Decl.Body.Lbrace); obj == nil {
				pass.Reportf(a.At, "//quint:action %s: %s uses %s, which is not a parameter, the receiver or a named result of %s: the step is recorded on return, where locals are out of scope; name the result or pass it in", a.Action, k, name, a.Func)
			}
		}
	}
}

// freeNames are the identifiers e refers to by name: not selected fields
// or methods, not composite literal keys, not "_".
func freeNames(e ast.Expr) []string {
	seen := map[string]bool{}
	var out []string
	skip := map[*ast.Ident]bool{}
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			skip[x.Sel] = true
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok {
				skip[id] = true
			}
		case *ast.FuncLit:
			return false // its own scope
		case *ast.Ident:
			if !skip[x] && x.Name != "_" && !seen[x.Name] {
				seen[x.Name] = true
				out = append(out, x.Name)
			}
		}
		return true
	})
	return out
}

type loaded struct {
	b   *binding.Binding
	m   *binding.Model
	err error
}

var (
	bindingsMu sync.Mutex
	bindings   = map[string]*loaded{}
)

// loadBinding loads a binding and its model once per process: the
// analyzer runs per package, and loading the model runs quint.
func loadBinding(path, quint string) (*binding.Binding, *binding.Model, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	bindingsMu.Lock()
	defer bindingsMu.Unlock()
	if l, ok := bindings[abs]; ok {
		return l.b, l.m, l.err
	}
	l := &loaded{}
	if l.b, l.err = binding.Load(abs); l.err == nil {
		l.m, l.err = binding.LoadModel(quint, l.b.SpecPath(), l.b.Module)
	}
	if l.err != nil {
		l.err = fmt.Errorf("quintaction: -binding %s: %w", path, l.err)
	}
	bindings[abs] = l
	return l.b, l.m, l.err
}
