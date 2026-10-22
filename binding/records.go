package binding

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

// qobsPath is the import path of the recording package.
const qobsPath = "github.com/casselc/quint-connect-go/qobs"

// RecordCall is an explicit qobs.Record or Recorder.Record call whose
// action is a string literal, found in Go source.
type RecordCall struct {
	Pos    string        // file:line
	At     token.Pos     // Pos in the file set the file was parsed with
	Call   *ast.CallExpr // the call
	Func   string        // the enclosing function ("" at package level)
	Action string
	// Method is set for x.Record(...) on a value, taken for a
	// *qobs.Recorder by its name and arguments alone (no types): a caller
	// with types confirms it.
	Method bool
}

// ScanRecords finds the literal qobs.Record actions in the Go files under
// dir, tests included and testdata (fixtures, wrong on purpose) skipped.
func ScanRecords(dir string) ([]RecordCall, error) {
	var out []RecordCall
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && path != dir {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out = append(out, RecordsOf(fset, f)...)
		return nil
	})
	return out, err
}

// RecordsOf are the calls in one parsed file that record a step with a
// literal action: qobs.Record(ctx, "a", err, ...), and x.Record(ctx, "a",
// err, ...) on any value (a *qobs.Recorder). Only files that import qobs
// are scanned, and an action that is not a literal (a constant, a
// variable) is not seen.
func RecordsOf(fset *token.FileSet, f *ast.File) []RecordCall {
	// The file's package names: qtest.Record(t, "b.yaml", ...) is another
	// package's Record, not a recorder's.
	pkg, imported := "", map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := importName(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imported[name] = true
		if p == qobsPath {
			pkg = name
		}
	}
	if pkg == "" || pkg == "_" {
		return nil
	}
	var out []RecordCall
	scan := func(fn string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Record" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			action, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			id, isIdent := sel.X.(*ast.Ident)
			if isIdent && id.Name != pkg && imported[id.Name] {
				return true
			}
			out = append(out, RecordCall{
				Pos: fset.Position(lit.Pos()).String(), At: lit.Pos(), Call: call,
				Func: fn, Action: action, Method: !isIdent || id.Name != pkg,
			})
			return true
		})
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			scan(fn.Name.Name, fn)
		} else {
			scan("", d)
		}
	}
	return out
}

// importName is the name a package is usually declared with: its path's
// last element, without a major version (yaml.v3, /v2) or a go- prefix.
func importName(path string) string {
	elems := strings.Split(path, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
		name = elems[len(elems)-2]
	}
	if i := strings.Index(name, ".v"); i > 0 {
		name = name[:i]
	}
	return strings.TrimPrefix(name, "go-")
}

// LintRecords checks literal Record actions against the model: each names
// one of its actions.
func LintRecords(m *Model, recs []RecordCall) []string {
	var errs []string
	for _, r := range recs {
		if _, ok := m.Actions[r.Action]; ok {
			continue
		}
		in := ""
		if r.Func != "" {
			in = " " + r.Func + ":"
		}
		errs = append(errs, fmt.Sprintf("%s:%s Record(ctx, %q, ...): %s", r.Pos, in, r.Action, m.notAnAction(r.Action)))
	}
	return errs
}

// LintSource checks annotations (LintAnnotations) and literal Record
// actions (LintRecords) together, so a bound action that one of them
// emits is not reported as emitted by neither.
func (b *Binding) LintSource(m *Model, anns []Annotation, recs []RecordCall) (errs, warns []string) {
	errs, _ = b.lintAnnotations(m, anns)
	errs = append(errs, LintRecords(m, recs)...)
	emitted := map[string]bool{}
	for _, a := range anns {
		emitted[a.Action] = true
	}
	for _, r := range recs {
		emitted[r.Action] = true
	}
	for _, name := range sortedNames(b.Actions) {
		if !emitted[name] {
			warns = append(warns, fmt.Sprintf("action %s is bound but neither a //quint:action nor a literal qobs.Record emits it (an action recorded through a constant or a variable is not seen)", name))
		}
	}
	return errs, warns
}

// notAnAction says that name is not an action of m, with the closest
// action if one is near, and the list.
func (m *Model) notAnAction(name string) string {
	var names []string
	best, bestD := "", -1
	for _, a := range m.ActionNames() {
		if a == "init" || a == "step" {
			continue // the state machine, not steps
		}
		names = append(names, a)
		if d := editDistance(strings.ToLower(name), strings.ToLower(a)); bestD < 0 || d < bestD {
			best, bestD = a, d
		}
	}
	msg := fmt.Sprintf("%s is not an action of %s", name, m.Module)
	if bestD >= 0 && bestD <= max(2, len(name)/3) {
		msg += fmt.Sprintf(" (did you mean %s?)", best)
	}
	return msg + "; its actions: " + strings.Join(names, ", ")
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
