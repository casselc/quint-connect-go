package qweave

import (
	"fmt"
	"go/ast"
	"strconv"
	"strings"
)

// directiveArgs returns the arguments of the directive in a function's doc
// comment ("//quint:action a:b c:d" -> "a:b c:d"), or ok false.
func directiveArgs(fn *ast.FuncDecl, directive string) (string, bool) {
	if fn == nil || fn.Doc == nil {
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

// functionBodyDirective is the directive of the function-body join point
// in j.
func functionBodyDirective(j Join) string {
	switch j := j.(type) {
	case FunctionBody:
		return j.Directive
	case AllOf:
		for _, x := range j {
			if d := functionBodyDirective(x); d != "" {
				return d
			}
		}
	}
	return ""
}

// A DirectiveArg is one key:value of a directive.
type DirectiveArg struct {
	Key, Value string
}

// splitArgs splits a directive's arguments on spaces outside quotes and
// brackets, then each at its first colon; a quoted value is unquoted.
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

// tctx is the template data of function-body advice, with Orchestrion's
// names.
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
	w       *fileWeaver
	renamed bool // unnamed parameters or results were named
}

func (f *function) typeIs(expr ast.Expr, want string) bool {
	t := f.w.p.info.TypeOf(expr)
	if t == nil {
		return false
	}
	return t.String() == want
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

// Name is the function's name (a method's, without its receiver).
func (f *function) Name() string { return f.decl.Name.Name }

// ArgumentOfType is the name of the first parameter of the type
// ("context.Context"), "" if none.
func (f *function) ArgumentOfType(t string) string {
	return f.find(f.decl.Type.Params, t, "__qw_arg")
}

// ResultOfType is the name of the first result of the type, "" if none.
func (f *function) ResultOfType(t string) string {
	return f.find(f.decl.Type.Results, t, "__qw_res")
}
