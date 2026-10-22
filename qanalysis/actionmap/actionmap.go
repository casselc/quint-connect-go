// Package actionmap computes, for each model action, the code statically
// reachable from where its steps are recorded, and compares it with the
// code its steps ran (qcover's per-step map, DESIGN §17).
//
// A function is a site of action A if it carries //quint:action action:A,
// or calls qobs.Record with the constant action A (directly or in a
// function literal: checkout records at commit, in tx.OnCommit's
// closure). From each site, reachability follows a VTA call graph
// (golang.org/x/tools/go/callgraph/vta, seeded by CHA) and every function
// literal the site defines, restricted to the loaded packages. Functions
// are named as qcover names a block's function: pkgpath.Func or
// pkgpath.(T).Method, a closure by its enclosing declaration.
//
// The comparison answers, per action:
//   - NotRun: code its sites reach that no run ran at all (a path the
//     exploration never took: a fault never injected, a branch the model
//     rules out);
//   - ByOthers: code its sites reach that runs ran, but only under other
//     actions' steps (shared helpers, or a path of this action that the
//     runs never took);
//   - Unreachable: code its steps ran that no site reaches (an
//     attribution gap: work done by the task before its next step, such
//     as a client whose step a service records, or a call the call graph
//     misses).
package actionmap

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/casselc/quint-connect-go/binding"
	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
)

const recordFunc = "github.com/casselc/quint-connect-go/qobs.Record"

// A Map is each action's sites and the functions they reach.
type Map struct {
	Sites map[string][]string        // action -> site functions
	Reach map[string]map[string]bool // action -> functions reachable from its sites (sites included)
	// Callers are each own function's own callers (closures under their
	// declaration, as Name names them).
	Callers map[string]map[string]bool
	// Dead are own functions no root reaches: main, every init, and the
	// exported functions and methods of non-main packages (what deadcode
	// reports, by the same names as qcover's blocks).
	Dead []string
}

// Actions are the map's actions, sorted.
func (m *Map) Actions() []string { return sortedKeys(m.Sites) }

// Static loads the packages matching patterns (relative to dir; tests
// excluded) and maps each action to the functions of those packages its
// sites reach.
func Static(dir string, patterns ...string) (*Map, error) {
	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Dir: dir}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs = append(errs, e.Error())
		}
	})
	if len(errs) > 0 {
		return nil, fmt.Errorf("actionmap: loading %v: %s", patterns, strings.Join(errs, "; "))
	}
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	own := map[*types.Package]bool{}
	for _, p := range ssaPkgs {
		if p != nil {
			own[p.Pkg] = true
		}
	}
	all := ssautil.AllFunctions(prog)
	cg := vta.CallGraph(all, cha.CallGraph(prog))
	cg.DeleteSyntheticNodes()

	m := &Map{Sites: map[string][]string{}, Reach: map[string]map[string]bool{}, Callers: map[string]map[string]bool{}}
	for f := range all {
		if !isOwn(f, own) {
			continue
		}
		if n := cg.Nodes[f]; n != nil {
			for _, e := range n.In {
				if c := e.Caller.Func; c != nil && isOwn(c, own) && Name(c) != Name(f) {
					if m.Callers[Name(f)] == nil {
						m.Callers[Name(f)] = map[string]bool{}
					}
					m.Callers[Name(f)][Name(c)] = true
				}
			}
		}
	}
	m.Dead = dead(all, cg, own, ssaPkgs)
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				obj, _ := p.TypesInfo.Defs[fd.Name].(*types.Func)
				fn := prog.FuncValue(obj)
				if fn == nil {
					continue
				}
				for _, action := range actionsOf(p, fd) {
					name := Name(fn)
					m.Sites[action] = appendUnique(m.Sites[action], name)
					if m.Reach[action] == nil {
						m.Reach[action] = map[string]bool{}
					}
					reach(cg, fn, own, m.Reach[action])
				}
			}
		}
	}
	for _, s := range m.Sites {
		sort.Strings(s)
	}
	return m, nil
}

// actionsOf are the actions fd is a site of: its //quint:action, and the
// constant actions of the qobs.Record calls in its body.
func actionsOf(p *packages.Package, fd *ast.FuncDecl) []string {
	var out []string
	file := &ast.File{Decls: []ast.Decl{fd}}
	for _, a := range binding.AnnotationsOf(p.Fset, file) {
		if a.Action != "" {
			out = appendUnique(out, a.Action)
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		var id *ast.Ident
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			id = fun.Sel
		case *ast.Ident:
			id = fun
		}
		if id == nil {
			return true
		}
		if f, ok := p.TypesInfo.Uses[id].(*types.Func); !ok || f.FullName() != recordFunc {
			return true
		}
		if tv, ok := p.TypesInfo.Types[call.Args[1]]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
			out = appendUnique(out, constant.StringVal(tv.Value))
		}
		return true
	})
	return out
}

// reach adds the own functions reachable from fn (and the function
// literals it defines) to into. It does not go through functions of other
// packages: a library that calls back (a store's commit hooks, a server's
// handlers) is one node for every closure handed to it, so going through
// it reaches every other action's closures too.
func reach(cg *callgraph.Graph, fn *ssa.Function, own map[*types.Package]bool, into map[string]bool) {
	seen := map[*ssa.Function]bool{}
	var visit func(f *ssa.Function)
	visit = func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		if !isOwn(f, own) {
			return // a library calls back into every closure it was given: not this site's
		}
		into[Name(f)] = true
		for _, anon := range f.AnonFuncs {
			visit(anon)
		}
		if n := cg.Nodes[f]; n != nil {
			for _, e := range n.Out {
				visit(e.Callee.Func)
			}
		}
	}
	visit(fn)
}

// dead are the own functions no root reaches (Map.Dead). Reaching goes
// through other packages' functions here: a callback handed to a library
// is live.
func dead(all map[*ssa.Function]bool, cg *callgraph.Graph, own map[*types.Package]bool, pkgs []*ssa.Package) []string {
	live := map[*ssa.Function]bool{}
	var visit func(f *ssa.Function)
	visit = func(f *ssa.Function) {
		if f == nil || live[f] {
			return
		}
		live[f] = true
		for _, anon := range f.AnonFuncs {
			visit(anon)
		}
		if n := cg.Nodes[f]; n != nil {
			for _, e := range n.Out {
				visit(e.Callee.Func)
			}
		}
	}
	for f := range all {
		if f.Pkg == nil || !own[f.Pkg.Pkg] || f.Parent() != nil {
			continue
		}
		name := f.Name()
		root := name == "init" || strings.HasPrefix(name, "init#") || (f.Pkg.Pkg.Name() == "main" && name == "main")
		// a library package's API is used from elsewhere (its tests too)
		if f.Pkg.Pkg.Name() != "main" && f.Object() != nil && f.Object().Exported() {
			root = true
		}
		if root {
			visit(f)
		}
	}
	set := map[string]bool{}
	for f := range all {
		if isOwn(f, own) && f.Parent() == nil && !live[f] && f.Synthetic == "" {
			set[Name(f)] = true
		}
	}
	return sortedKeys(set)
}

// An ErrorPath is code the exploration never tried: blocks no run ran in
// the callers of a fault point the runs reached but never faulted, which
// is where a fault point's error is handled.
type ErrorPath struct {
	Kind   string // the fault point's kind
	Site   string // the fault point's function, as declared
	Caller string
	Blocks []qcover.Block
}

// UntriedErrorPaths lists, for each fault point the runs reached and never
// faulted (cov.Unfaulted), the blocks no run ran in its callers: the
// error handling a fault there would run. Raise the kind's rate, or script
// it, before reading them as dead.
func UntriedErrorPaths(m *Map, s *qcover.Summary, cov *qenv.Coverage) []ErrorPath {
	var out []ErrorPath
	for _, p := range cov.Unfaulted() {
		for _, site := range p.Sites {
			for fn, callers := range m.Callers {
				if !strings.HasSuffix(fn, "."+site) && !strings.HasSuffix(fn, ")."+site) {
					continue
				}
				for _, caller := range sortedKeys(callers) {
					var blocks []qcover.Block
					for _, b := range s.Blocks {
						if b.Func == caller && s.Hit[b.ID] == 0 {
							blocks = append(blocks, b)
						}
					}
					if len(blocks) > 0 {
						out = append(out, ErrorPath{Kind: p.Kind, Site: site, Caller: caller, Blocks: blocks})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Caller < out[j].Kind+out[j].Caller })
	return out
}

func isOwn(f *ssa.Function, own map[*types.Package]bool) bool {
	if o := f.Origin(); o != nil {
		f = o
	}
	return f.Pkg != nil && own[f.Pkg.Pkg]
}

// Name is fn's name as qcover names a block's function: the enclosing
// declaration's pkgpath.Func or pkgpath.(T).Method.
func Name(fn *ssa.Function) string {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	path := ""
	if fn.Pkg != nil {
		path = fn.Pkg.Pkg.Path()
	} else if obj := fn.Object(); obj != nil && obj.Pkg() != nil {
		path = obj.Pkg().Path()
	}
	if recv := fn.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := types.Unalias(t).(*types.Named); ok {
			return path + ".(" + n.Obj().Name() + ")." + fn.Name()
		}
	}
	name := fn.Name()
	if strings.HasPrefix(name, "init#") {
		name = "init"
	}
	return path + "." + name
}

// A Gap is one action's static and dynamic code compared.
type Gap struct {
	Action string
	Sites  []string
	// NotRun are functions its sites reach that no run ran.
	NotRun []string
	// ByOthers are functions its sites reach that ran only under other
	// actions' steps.
	ByOthers []string
	// Unreachable are functions its steps ran that no site reaches.
	Unreachable []string
}

// Compare reads coverage s against the map: for each action of either, the
// functions on one side only. Functions without instrumented blocks (not
// woven by qweave -cover) are left out of the static side.
func Compare(m *Map, s *qcover.Summary) []Gap {
	byID := map[int]qcover.Block{}
	instrumented := map[string]bool{}
	hit := map[string]bool{}
	for _, b := range s.Blocks {
		byID[b.ID] = b
		instrumented[b.Func] = true
		if s.Hit[b.ID] > 0 {
			hit[b.Func] = true
		}
	}
	ran := map[string]map[string]bool{} // action -> functions its steps ran
	for k, ids := range s.ByStep {
		action, _, _ := strings.Cut(k, "/")
		if ran[action] == nil {
			ran[action] = map[string]bool{}
		}
		for id := range ids {
			ran[action][byID[id].Func] = true
		}
	}
	actions := map[string]bool{}
	for a := range m.Sites {
		actions[a] = true
	}
	for a := range ran {
		actions[a] = true
	}
	var out []Gap
	for _, a := range sortedKeys(actions) {
		g := Gap{Action: a, Sites: m.Sites[a]}
		for _, f := range sortedKeys(m.Reach[a]) {
			switch {
			case !instrumented[f] || ran[a][f]:
			case !hit[f]:
				g.NotRun = append(g.NotRun, f)
			default:
				g.ByOthers = append(g.ByOthers, f)
			}
		}
		for _, f := range sortedKeys(ran[a]) {
			if !m.Reach[a][f] {
				g.Unreachable = append(g.Unreachable, f)
			}
		}
		out = append(out, g)
	}
	return out
}

// Report formats gaps, one action per paragraph.
func Report(gaps []Gap) string {
	var b strings.Builder
	for _, g := range gaps {
		fmt.Fprintf(&b, "%s (sites: %s)\n", g.Action, list(g.Sites))
		if len(g.Sites) == 0 {
			b.WriteString("  no site records it: its steps come from code the call graph does not see\n")
		}
		for _, p := range []struct {
			what string
			fs   []string
		}{{"reachable, never run", g.NotRun}, {"reachable, run only under other actions", g.ByOthers}, {"run by its steps, not reachable from a site", g.Unreachable}} {
			if len(p.fs) > 0 {
				fmt.Fprintf(&b, "  %s: %s\n", p.what, list(p.fs))
			}
		}
	}
	return b.String()
}

func list(fs []string) string {
	if len(fs) == 0 {
		return "none"
	}
	return strings.Join(fs, ", ")
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
