// Package weave applies Orchestrion aspects (https://datadoghq.dev/orchestrion)
// to Go source, before gosim translates it, so that code annotated for
// Orchestrion (quint-connect-go's //quint:action, for one) is woven in a
// simulation as it is in an `orchestrion go build`.
//
// It reads the aspect files (orchestrion.yml) of the packages a module's
// tool file imports, and applies the aspects whose join points and advice
// it supports, at the source level: the woven files are handed to the
// package loader as an overlay, so types, imports and positions are right
// downstream. Aspects it does not support are skipped, and reported.
//
// Supported so far:
//   - join point: function-body with directive;
//   - advice: prepend-statements, with imports and a template using
//     .Function.ArgumentOfType, .Function.ResultOfType and
//     .DirectiveArgs (Orchestrion's names and semantics).
package weave

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// An Aspect is a supported aspect: a directive's function bodies and the
// statements to prepend to them.
type Aspect struct {
	ID        string
	Directive string            // "quint:action": the line //quint:action ...
	Imports   map[string]string // alias -> import path, for the template
	Template  string
	tmpl      *template.Template
}

// A Skipped aspect uses a join point or advice weave does not support.
type Skipped struct {
	File, ID, Why string
}

type aspectFile struct {
	Aspects []struct {
		ID        string    `yaml:"id"`
		JoinPoint yaml.Node `yaml:"join-point"`
		Advice    []map[string]struct {
			Imports  map[string]string `yaml:"imports"`
			Template string            `yaml:"template"`
		} `yaml:"advice"`
	} `yaml:"aspects"`
}

// Load reads an aspect file.
func Load(path string) ([]*Aspect, []Skipped, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var f aspectFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []*Aspect
	var skipped []Skipped
	for _, a := range f.Aspects {
		var jp struct {
			FunctionBody *struct {
				Directive string `yaml:"directive"`
			} `yaml:"function-body"`
		}
		_ = a.JoinPoint.Decode(&jp)
		var keys []string
		if a.JoinPoint.Kind == yaml.MappingNode {
			for i := 0; i < len(a.JoinPoint.Content); i += 2 {
				keys = append(keys, a.JoinPoint.Content[i].Value)
			}
		}
		if jp.FunctionBody == nil || jp.FunctionBody.Directive == "" || len(keys) != 1 {
			skipped = append(skipped, Skipped{path, a.ID, fmt.Sprintf("join point %v (only function-body with a directive)", keys)})
			continue
		}
		if len(a.Advice) != 1 || len(a.Advice[0]) != 1 {
			skipped = append(skipped, Skipped{path, a.ID, "more than one advice"})
			continue
		}
		adv, ok := a.Advice[0]["prepend-statements"]
		if !ok {
			skipped = append(skipped, Skipped{path, a.ID, "advice other than prepend-statements"})
			continue
		}
		t, err := template.New(a.ID).Parse(adv.Template)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: aspect %s: %w", path, a.ID, err)
		}
		out = append(out, &Aspect{ID: a.ID, Directive: jp.FunctionBody.Directive, Imports: adv.Imports, Template: adv.Template, tmpl: t})
	}
	return out, skipped, nil
}

// Hash identifies a set of aspects, for caches of woven output.
func Hash(aspects []*Aspect) string {
	h := sha256.New()
	for _, a := range aspects {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", a.ID, a.Directive, a.Template)
		keys := make([]string, 0, len(a.Imports))
		for k := range a.Imports {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s=%s\x00", k, a.Imports[k])
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// directiveOn returns the arguments of the directive in a function's doc
// comment ("//quint:action a:b c:d" -> "a:b c:d"), or ok false.
func directiveOn(fn *ast.FuncDecl, directive string) (string, bool) {
	if fn.Doc == nil {
		return "", false
	}
	for _, c := range fn.Doc.List {
		text := strings.TrimPrefix(c.Text, "//")
		if text == c.Text || !strings.HasPrefix(text, directive) {
			continue
		}
		rest := text[len(directive):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// A DirectiveArg is one key:value of a directive.
type DirectiveArg struct {
	Key, Value string
}

// splitArgs splits a directive's arguments on spaces outside quotes and
// parentheses, then each at its first colon; a quoted value is unquoted.
func splitArgs(s string) []DirectiveArg {
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
	var out []DirectiveArg
	for _, f := range fields {
		k, v, _ := strings.Cut(f, ":")
		if len(v) >= 2 && v[0] == '"' {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
		}
		out = append(out, DirectiveArg{k, v})
	}
	return out
}

// tctx is the template's dot, with Orchestrion's names.
type tctx struct {
	fn   *function
	args []DirectiveArg
	name string
}

// Function is the woven function.
func (c tctx) Function() *function { return c.fn }

// DirectiveArgs returns the arguments of the named directive.
func (c tctx) DirectiveArgs(name string) []DirectiveArg {
	if name != c.name {
		return nil
	}
	return c.args
}

type function struct {
	decl    *ast.FuncDecl
	imports map[string]string // local name -> path, of the file
	// names given to unnamed parameters and results the template refers to
	renamed bool
}

func (f *function) typeIs(expr ast.Expr, want string) bool {
	pkg, name := "", want
	if i := strings.LastIndex(want, "."); i >= 0 {
		pkg, name = want[:i], want[i+1:]
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return pkg == "" && e.Name == name
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && e.Sel.Name == name && f.imports[x.Name] == pkg
	}
	return false
}

func (f *function) find(list *ast.FieldList, want, prefix string) string {
	if list == nil {
		return ""
	}
	for i, field := range list.List {
		if !f.typeIs(field.Type, want) {
			continue
		}
		if len(field.Names) == 0 {
			// Orchestrion names an unnamed parameter or result the advice
			// needs; naming them all keeps the declaration valid.
			for j, fl := range list.List {
				if len(fl.Names) == 0 {
					fl.Names = []*ast.Ident{ast.NewIdent(fmt.Sprintf("%s%d", prefix, j))}
				}
			}
			f.renamed = true
			return fmt.Sprintf("%s%d", prefix, i)
		}
		if field.Names[0].Name == "_" {
			field.Names[0].Name = fmt.Sprintf("%s%d", prefix, i)
			f.renamed = true
		}
		return field.Names[0].Name
	}
	return ""
}

// ArgumentOfType is the name of the first parameter of the type ("" if
// none), as in Orchestrion.
func (f *function) ArgumentOfType(t string) string { return f.find(f.decl.Type.Params, t, "__weave_arg") }

// ResultOfType is the name of the first result of the type ("" if none).
func (f *function) ResultOfType(t string) string { return f.find(f.decl.Type.Results, t, "__weave_res") }

type edit struct {
	at   int
	text string
	cut  int // bytes of the original replaced from at
}

// File weaves one file: it returns the woven source, or nil if no aspect
// applies. filename is the file's path, for //line directives that keep
// positions pointing at the original lines.
func File(filename string, src []byte, aspects []*Aspect) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	imports := map[string]string{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = path
	}
	var edits []edit
	need := map[string]string{} // alias -> path
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, a := range aspects {
			argsText, ok := directiveOn(fn, a.Directive)
			if !ok {
				continue
			}
			info := &function{decl: fn, imports: imports}
			var out bytes.Buffer
			if err := a.tmpl.Execute(&out, tctx{fn: info, args: splitArgs(argsText), name: a.Directive}); err != nil {
				return nil, fmt.Errorf("%s: %s: aspect %s: %w", fset.Position(fn.Pos()), fn.Name.Name, a.ID, err)
			}
			stmts := out.String()
			for alias, path := range a.Imports {
				if !strings.Contains(stmts, alias+".") || imports[alias] == path {
					continue
				}
				if name := nameOf(imports, path); name != "" && name != "_" && name != "." {
					// imported already, under another name: use that one (a
					// second import of the path confuses gosim's translator)
					stmts = renameQualifier(stmts, alias, name)
					continue
				}
				need[alias] = path
			}
			if info.renamed {
				// rewrite the signature with the names given
				var sig bytes.Buffer
				if err := printNode(&sig, fset, fn.Type); err != nil {
					return nil, err
				}
				start, end := fset.Position(fn.Type.Params.Pos()).Offset, fset.Position(fn.Type.End()).Offset
				text := sig.String()
				text = text[strings.Index(text, "("):] // without "func"
				edits = append(edits, edit{at: start, cut: end - start, text: text})
			}
			lbrace := fset.Position(fn.Body.Lbrace)
			edits = append(edits, edit{at: lbrace.Offset + 1,
				text: "\n" + stmts + lineDirective(filename, lbrace.Line)})
		}
	}
	if len(edits) == 0 {
		return nil, nil
	}
	if len(need) > 0 {
		var b strings.Builder
		b.WriteString("\nimport (\n")
		aliases := make([]string, 0, len(need))
		for a := range need {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			fmt.Fprintf(&b, "\t%s %q\n", a, need[a])
		}
		b.WriteString(")\n")
		after := f.Name.End()
		if n := len(f.Imports); n > 0 {
			// after the last import declaration
			for _, d := range f.Decls {
				if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
					after = g.End()
				}
			}
		}
		pos := fset.Position(after)
		b.WriteString(lineDirective(filename, pos.Line))
		edits = append(edits, edit{at: pos.Offset, text: b.String()})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.at], append([]byte(e.text), out[e.at+e.cut:]...)...)
	}
	return out, nil
}

// nameOf is the name a file imports path under, or "".
func nameOf(imports map[string]string, path string) string {
	for name, p := range imports {
		if p == path {
			return name
		}
	}
	return ""
}

// renameQualifier replaces the package qualifier alias. with name. in Go
// source, where alias is not part of a longer identifier.
func renameQualifier(src, alias, name string) string {
	var b strings.Builder
	for {
		i := strings.Index(src, alias+".")
		if i < 0 {
			b.WriteString(src)
			return b.String()
		}
		if i > 0 && isIdent(src[i-1]) {
			b.WriteString(src[:i+len(alias)+1])
		} else {
			b.WriteString(src[:i] + name + ".")
		}
		src = src[i+len(alias)+1:]
	}
}

func isIdent(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// lineDirective resumes the original numbering: the line after it is line
// of filename, the rest of the original line the woven text was inserted
// into (after a function's brace, or after the imports).
func lineDirective(filename string, line int) string {
	return fmt.Sprintf("\n//line %s:%d\n", filename, line)
}

// Tool lists the packages a tool file (orchestrion.tool.go, gosim.tool.go)
// imports for their aspects.
func Tool(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, p)
	}
	return out, nil
}

// ToolFile returns the tool file of the module in dir: gosim.tool.go if it
// has one (aspects for simulations only), else orchestrion.tool.go, else "".
func ToolFile(dir string) string {
	for _, name := range []string{"gosim.tool.go", "orchestrion.tool.go"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func printNode(w *bytes.Buffer, fset *token.FileSet, n ast.Node) error {
	return format.Node(w, fset, n)
}
