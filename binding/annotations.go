package binding

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// Annotation is one //quint:action directive found in Go source.
type Annotation struct {
	Pos    string        // file:line
	At     token.Pos     // Pos in the file set the file was parsed with
	Decl   *ast.FuncDecl // the annotated function
	Func   string
	Action string
	Keys   map[string]string // key -> Go expression
}

// ScanAnnotations finds //quint:action directives on functions under dir.
func ScanAnnotations(dir string) ([]Annotation, error) {
	var out []Annotation
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) && path != dir {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		out = append(out, AnnotationsOf(fset, f)...)
		return nil
	})
	return out, err
}

// AnnotationsOf are the //quint:action directives on the functions of one
// parsed file (with comments).
func AnnotationsOf(fset *token.FileSet, f *ast.File) []Annotation {
	var out []Annotation
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		for _, c := range fn.Doc.List {
			if !strings.HasPrefix(c.Text, "//quint:action") || (len(c.Text) > 14 && c.Text[14] != ' ') {
				continue
			}
			a := Annotation{Pos: fset.Position(c.Pos()).String(), At: c.Pos(), Decl: fn, Func: fn.Name.Name, Keys: map[string]string{}}
			for _, kv := range directiveArgs(strings.TrimPrefix(c.Text, "//quint:action")) {
				k, v := kv[0], kv[1]
				if k == "action" {
					a.Action = v
				} else {
					a.Keys[k] = v
				}
			}
			out = append(out, a)
		}
	}
	return out
}

// directiveArgs splits a directive's arguments on spaces outside quotes
// and brackets, then each at its first colon; a quoted value is unquoted.
// It is qweave's splitArgs, which decides what weaving makes of the
// directive: testdata/directives.json is the table both are tested on.
func directiveArgs(s string) [][2]string {
	var fields []string
	var cur strings.Builder
	depth, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\\' && i+1 < len(s) {
				cur.WriteByte(c)
				i++
				c = s[i]
			} else if c == '"' {
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case (c == ' ' || c == '\t') && depth == 0:
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	var out [][2]string
	for _, f := range fields {
		k, v, _ := strings.Cut(f, ":")
		if len(v) >= 2 && v[0] == '"' {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

// LintAnnotations checks annotations against the model and the binding:
// each names an action of the model, and records every field the binding's
// templates for that action use. It also reports bound actions that no
// annotation emits (a warning: they may be emitted by explicit calls,
// which LintSource checks too).
func (b *Binding) LintAnnotations(m *Model, anns []Annotation) (errs, warns []string) {
	errs, emitted := b.lintAnnotations(m, anns)
	for _, name := range sortedNames(b.Actions) {
		if !emitted[name] {
			warns = append(warns, fmt.Sprintf("action %s is bound but no //quint:action emits it (explicit qobs.Record calls are not scanned)", name))
		}
	}
	return errs, warns
}

func (b *Binding) lintAnnotations(m *Model, anns []Annotation) (errs []string, emitted map[string]bool) {
	emitted = map[string]bool{}
	for _, a := range anns {
		if a.Action == "" {
			errs = append(errs, fmt.Sprintf("%s: %s: //quint:action without action:NAME", a.Pos, a.Func))
			continue
		}
		emitted[a.Action] = true
		if _, ok := m.Actions[a.Action]; !ok {
			errs = append(errs, fmt.Sprintf("%s: %s: %s", a.Pos, a.Func, m.notAnAction(a.Action)))
			continue
		}
		ab := b.Actions[a.Action]
		for _, f := range ab.Fields(m.Actions[a.Action]...) {
			if _, ok := a.Keys[f]; !ok { // obs fields are recorded as obs.<name> too
				errs = append(errs, fmt.Sprintf("%s: %s: the binding for %s uses .%s, which this annotation does not record", a.Pos, a.Func, a.Action, f))
			}
		}
	}
	return errs, emitted
}
