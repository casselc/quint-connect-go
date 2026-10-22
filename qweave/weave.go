package qweave

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
)

func hash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// pkgInfo is what weaving a file needs of its package.
type pkgInfo struct {
	fset *token.FileSet
	info *types.Info
	path string
	root bool // in the main module (package-filter root)
}

// An edit replaces src[start:end] (an insertion when they are equal). Its
// text is rendered once the edits inside it are woven: inner returns the
// woven text of a node inside the edit.
type edit struct {
	start, end int
	render     func(inner func(ast.Node) string) (string, error)
	children   []*edit
}

type fileWeaver struct {
	p       pkgInfo
	file    *ast.File
	src     []byte
	name    string // file path, for //line directives
	imports map[string]string
	need    map[string]string // alias -> path, imports to add
	edits   []*edit
	skipped []string
	// directives: line -> directive names on that line
	directives map[int][]string
}

// weaveFile weaves one file; it returns nil if nothing applies.
func weaveFile(p pkgInfo, file *ast.File, src []byte, name string, aspects []*Aspect) ([]byte, []string, error) {
	w := &fileWeaver{p: p, file: file, src: src, name: name, imports: map[string]string{}, need: map[string]string{}, directives: map[int][]string{}}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		n := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			n = imp.Name.Name
		} else if pk := p.info.PkgNameOf(imp); pk != nil {
			n = pk.Imported().Name()
		}
		w.imports[n] = path
	}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//") && !strings.HasPrefix(c.Text, "// ") {
				d := strings.Fields(strings.TrimPrefix(c.Text, "//"))
				if len(d) > 0 {
					line := p.fset.Position(c.Pos()).Line
					w.directives[line] = append(w.directives[line], d[0])
				}
			}
		}
	}
	if err := w.walk(aspects); err != nil {
		return nil, nil, err
	}
	if len(w.edits) == 0 {
		return nil, w.skipped, nil
	}
	out, err := w.apply()
	return out, w.skipped, err
}

func (w *fileWeaver) offset(pos token.Pos) int { return w.p.fset.Position(pos).Offset }
func (w *fileWeaver) line(pos token.Pos) int   { return w.p.fset.Position(pos).Line }

// node is a candidate join point in its context.
type node struct {
	n    ast.Node
	stmt ast.Stmt // the innermost enclosing statement
	fn   *ast.FuncDecl
}

func (w *fileWeaver) walk(aspects []*Aspect) error {
	var err error
	var stmts []ast.Stmt
	var fn *ast.FuncDecl
	var stack []ast.Node
	ast.Inspect(w.file, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		if n == nil {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := top.(ast.Stmt); ok && len(stmts) > 0 && stmts[len(stmts)-1] == top {
				stmts = stmts[:len(stmts)-1]
			}
			if top == fn {
				fn = nil
			}
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
		if s, ok := n.(ast.Stmt); ok {
			stmts = append(stmts, s)
		}
		if d, ok := n.(*ast.FuncDecl); ok {
			fn = d
		}
		var stmt ast.Stmt
		if len(stmts) > 0 {
			stmt = stmts[len(stmts)-1]
		}
		c := node{n: n, stmt: stmt, fn: fn}
		for _, a := range aspects {
			if !w.matches(a.Join, c, parent) {
				continue
			}
			if e := w.advise(a, c, parent); e != nil {
				w.edits = append(w.edits, e)
			}
		}
		return true
	})
	return err
}

// matches reports whether the join point holds at c (parent is c.n's
// parent node).
func (w *fileWeaver) matches(j Join, c node, parent ast.Node) bool {
	switch j := j.(type) {
	case AllOf:
		for _, x := range j {
			if !w.matches(x, c, parent) {
				return false
			}
		}
		return len(j) > 0
	case OneOf:
		for _, x := range j {
			if w.matches(x, c, parent) {
				return true
			}
		}
		return false
	case Not:
		return !w.matches(j.J, c, parent)
	case PackageFilter:
		return w.p.root
	case FunctionBody:
		d, ok := c.n.(*ast.FuncDecl)
		if !ok || d.Body == nil {
			return false
		}
		_, has := directiveArgs(d, j.Directive)
		return has
	case Function:
		_, ok := c.n.(*ast.FuncLit)
		return ok
	case Directive:
		if c.stmt == nil {
			return false
		}
		for _, d := range w.directives[w.line(c.stmt.Pos())-1] {
			if d == j.Name {
				return true
			}
		}
		return false
	case MethodCall:
		sel, call := w.methodSelector(c.n, parent)
		if sel == nil {
			return false
		}
		_, ok := w.methodMatch(j, sel)
		return ok && (call != nil || sel != nil)
	case FunctionCall:
		call, ok := c.n.(*ast.CallExpr)
		if !ok {
			return false
		}
		var id *ast.Ident
		switch f := call.Fun.(type) {
		case *ast.Ident:
			id = f
		case *ast.SelectorExpr:
			id = f.Sel
		}
		if id == nil {
			return false
		}
		obj, ok := w.p.info.Uses[id].(*types.Func)
		return ok && obj.Pkg() != nil && obj.Pkg().Path() == j.Path && obj.Name() == j.Name &&
			obj.Type().(*types.Signature).Recv() == nil
	}
	return false
}

// methodSelector returns the selector of a method call (the call too), or
// of a method value: a selector naming a method, not called.
func (w *fileWeaver) methodSelector(n, parent ast.Node) (*ast.SelectorExpr, *ast.CallExpr) {
	switch n := n.(type) {
	case *ast.CallExpr:
		if sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr); ok {
			return sel, n
		}
	case *ast.SelectorExpr:
		if call, ok := parent.(*ast.CallExpr); ok && ast.Unparen(call.Fun) == n {
			return nil, nil // the call is the join point
		}
		if s := w.p.info.Selections[n]; s != nil && s.Kind() == types.MethodVal {
			return n, nil
		}
	}
	return nil, nil
}

// receiverPath is how a matched method's receiver is reached: the field
// names from the selector's operand to the embedded field whose method it
// is, and whether that field (or the operand) is a pointer.
type receiverPath struct {
	fields  []string
	pointer bool
}

func (w *fileWeaver) methodMatch(j MethodCall, sel *ast.SelectorExpr) (receiverPath, bool) {
	s := w.p.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal || s.Obj().Name() != j.Name {
		return receiverPath{}, false
	}
	recv := s.Obj().Type().(*types.Signature).Recv().Type()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path()+"."+named.Obj().Name() != j.Receiver {
		return receiverPath{}, false
	}
	// walk the implicit fields to the receiver
	t := w.p.info.TypeOf(sel.X)
	var rp receiverPath
	idx := s.Index()
	for _, i := range idx[:len(idx)-1] {
		st, ok := deref(t).Underlying().(*types.Struct)
		if !ok {
			return receiverPath{}, false
		}
		f := st.Field(i)
		if !f.Exported() && f.Pkg() != nil && f.Pkg().Path() != w.p.path {
			return receiverPath{}, false // unreachable from here
		}
		rp.fields = append(rp.fields, f.Name())
		t = f.Type()
	}
	_, rp.pointer = t.Underlying().(*types.Pointer)
	switch j.Match {
	case "value-only":
		return rp, !rp.pointer
	case "pointer-only":
		return rp, rp.pointer
	}
	return rp, true
}

func deref(t types.Type) types.Type {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// methodJoin finds the method-call join point inside j, for the receiver
// path of a matched call.
func methodJoin(j Join) (MethodCall, bool) {
	switch j := j.(type) {
	case MethodCall:
		return j, true
	case AllOf:
		for _, x := range j {
			if m, ok := methodJoin(x); ok {
				return m, true
			}
		}
	}
	return MethodCall{}, false
}

// srcText is a node's woven text in a template: {{ . }} prints it.
type srcText string

func (s srcText) String() string { return string(s) }

type selData struct {
	X   srcText
	Sel srcText
}

func (s selData) String() string { return string(s.X) + "." + string(s.Sel) }

type callData struct {
	Fun  selData
	Args []srcText
}

type exprData struct {
	text srcText
	AST  callData
}

func (e exprData) String() string { return string(e.text) }

func (w *fileWeaver) advise(a *Aspect, c node, parent ast.Node) *edit {
	adv := a.Advice[0]
	switch adv.Kind {
	case "prepend-statements":
		d, ok := c.n.(*ast.FuncDecl)
		if !ok {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: aspect %s: prepend-statements on a %T", w.p.fset.Position(c.n.Pos()), a.ID, c.n))
			return nil
		}
		return w.prepend(a, adv, d)
	case "wrap-expression":
		expr, ok := c.n.(ast.Expr)
		if !ok {
			return nil
		}
		return w.wrap(a, adv, expr, parent)
	}
	return nil
}

func (w *fileWeaver) execute(a *Aspect, adv Advice, data any) (string, error) {
	var out bytes.Buffer
	if err := adv.tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("aspect %s: %w", a.ID, err)
	}
	s := out.String()
	for alias, path := range adv.Imports {
		if !strings.Contains(s, alias+".") || w.imports[alias] == path {
			continue
		}
		if name := nameOf(w.imports, path); name != "" && name != "_" && name != "." {
			s = renameQualifier(s, alias, name) // imported already: use its name
			continue
		}
		w.need[alias] = path
	}
	return s, nil
}

func (w *fileWeaver) wrap(a *Aspect, adv Advice, expr ast.Expr, parent ast.Node) *edit {
	e := &edit{start: w.offset(expr.Pos()), end: w.offset(expr.End())}
	mj, isMethod := methodJoin(a.Join)
	sel, call := w.methodSelector(expr, parent)
	if isMethod && sel != nil && call == nil {
		return w.methodValue(a, adv, mj, sel, e)
	}
	e.render = func(inner func(ast.Node) string) (string, error) {
		d := exprData{text: srcText(inner(expr))}
		if call, ok := expr.(*ast.CallExpr); ok {
			for _, arg := range call.Args {
				d.AST.Args = append(d.AST.Args, srcText(inner(arg)))
			}
			if s, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
				x := inner(s.X)
				if isMethod {
					rp, _ := w.methodMatch(mj, s)
					for _, f := range rp.fields {
						x = "(" + x + ")." + f // a promoted call: on the embedded field
					}
				}
				d.AST.Fun = selData{X: srcText(x), Sel: srcText(s.Sel.Name)}
			}
		}
		return w.execute(a, adv, d)
	}
	return e
}

// methodValue weaves a method value: a function literal binding the
// receiver now (as a method value does) and making the advised call.
func (w *fileWeaver) methodValue(a *Aspect, adv Advice, mj MethodCall, sel *ast.SelectorExpr, e *edit) *edit {
	rp, _ := w.methodMatch(mj, sel)
	sig := w.p.info.Selections[sel].Type().(*types.Signature)
	var params, args []string
	for i := 0; i < sig.Params().Len(); i++ {
		t, ok := w.typeString(sig.Params().At(i).Type())
		if !ok {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: aspect %s: method value with a parameter type this file cannot name", w.p.fset.Position(sel.Pos()), a.ID))
			return nil
		}
		params = append(params, fmt.Sprintf("__qw_a%d %s", i, t))
		args = append(args, fmt.Sprintf("__qw_a%d", i))
	}
	var results []string
	for i := 0; i < sig.Results().Len(); i++ {
		t, ok := w.typeString(sig.Results().At(i).Type())
		if !ok {
			w.skipped = append(w.skipped, fmt.Sprintf("%s: aspect %s: method value with a result type this file cannot name", w.p.fset.Position(sel.Pos()), a.ID))
			return nil
		}
		results = append(results, t)
	}
	e.render = func(inner func(ast.Node) string) (string, error) {
		x := inner(sel.X)
		for _, f := range rp.fields {
			x = "(" + x + ")." + f
		}
		bind, use := "&("+x+")", "(*__qw_r)"
		if rp.pointer {
			bind, use = x, "__qw_r"
		}
		d := exprData{text: srcText(use + "." + sel.Sel.Name + "(" + strings.Join(args, ", ") + ")")}
		d.AST.Fun = selData{X: srcText(use), Sel: srcText(sel.Sel.Name)}
		for _, arg := range args {
			d.AST.Args = append(d.AST.Args, srcText(arg))
		}
		body, err := w.execute(a, adv, d)
		if err != nil {
			return "", err
		}
		res := strings.Join(results, ", ")
		if len(results) > 1 {
			res = "(" + res + ")"
		}
		ret := ""
		if len(results) > 0 {
			ret = "return "
		}
		fn := fmt.Sprintf("func(%s) %s", strings.Join(params, ", "), res)
		return fmt.Sprintf("func() %s { __qw_r := %s; return %s { %s%s } }()", fn, bind, fn, ret, body), nil
	}
	return e
}

// typeString names t in this file, or reports that it cannot.
func (w *fileWeaver) typeString(t types.Type) (string, bool) {
	ok := true
	s := types.TypeString(t, func(p *types.Package) string {
		if p.Path() == w.p.path {
			return ""
		}
		if n := nameOf(w.imports, p.Path()); n != "" {
			return n
		}
		ok = false
		return p.Name()
	})
	return s, ok
}

func (w *fileWeaver) prepend(a *Aspect, adv Advice, d *ast.FuncDecl) *edit {
	directive := functionBodyDirective(a.Join)
	argsText, _ := directiveArgs(d, directive)
	info := &function{decl: d, w: w}
	stmts, err := w.execute(a, adv, tctx{fn: info, args: splitArgs(argsText), name: directive})
	lbrace := w.p.fset.Position(d.Body.Lbrace)
	e := &edit{start: lbrace.Offset + 1, end: lbrace.Offset + 1}
	e.render = func(func(ast.Node) string) (string, error) {
		if err != nil {
			return "", err
		}
		return "\n" + stmts + lineDirective(w.name, lbrace.Line), nil
	}
	if info.renamed {
		var sig bytes.Buffer
		if err := format.Node(&sig, w.p.fset, d.Type); err != nil {
			return nil
		}
		text := sig.String()
		text = text[strings.Index(text, "("):]
		start, end := w.offset(d.Type.Params.Pos()), w.offset(d.Type.End())
		w.edits = append(w.edits, &edit{start: start, end: end, render: func(func(ast.Node) string) (string, error) { return text, nil }})
	}
	return e
}

// apply weaves the edits into the source: nested edits are rendered
// inside out, each from the woven text of what it contains.
func (w *fileWeaver) apply() ([]byte, error) {
	edits := w.edits
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		return edits[i].end > edits[j].end
	})
	// containment tree
	var roots []*edit
	var stack []*edit
	for _, e := range edits {
		for len(stack) > 0 && !(e.start >= stack[len(stack)-1].start && e.end <= stack[len(stack)-1].end && e != stack[len(stack)-1]) {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			if e.start == top.start && e.end == top.end {
				return nil, fmt.Errorf("%s: two aspects replace the same expression", w.p.fset.Position(w.file.Pos()))
			}
			top.children = append(top.children, e)
		} else {
			roots = append(roots, e)
		}
		if e.end > e.start {
			stack = append(stack, e)
		}
	}
	var text func(start, end int, children []*edit) (string, error)
	render := func(e *edit) (string, error) {
		inner := func(n ast.Node) string {
			s, e2 := w.offset(n.Pos()), w.offset(n.End())
			var kids []*edit
			for _, c := range e.children {
				if c.start >= s && c.end <= e2 {
					kids = append(kids, c)
				}
			}
			t, err := text(s, e2, kids)
			if err != nil {
				return string(w.src[s:e2])
			}
			return t
		}
		return e.render(inner)
	}
	text = func(start, end int, children []*edit) (string, error) {
		var b strings.Builder
		at := start
		for _, c := range children {
			if c.start < at {
				continue
			}
			b.Write(w.src[at:c.start])
			r, err := render(c)
			if err != nil {
				return "", err
			}
			b.WriteString(r)
			at = c.end
		}
		b.Write(w.src[at:end])
		return b.String(), nil
	}
	body, err := text(0, len(w.src), roots)
	if err != nil {
		return nil, err
	}
	if len(w.need) == 0 {
		return []byte(body), nil
	}
	// imports, after the last import declaration (or the package clause);
	// the offset is the same in the woven text, which edits only after it
	after := w.file.Name.End()
	for _, d := range w.file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			after = g.End()
		}
	}
	pos := w.p.fset.Position(after)
	var imp strings.Builder
	imp.WriteString("\nimport (\n")
	aliases := make([]string, 0, len(w.need))
	for a := range w.need {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		fmt.Fprintf(&imp, "\t%s %q\n", a, w.need[a])
	}
	imp.WriteString(")")
	imp.WriteString(lineDirective(w.name, pos.Line))
	return []byte(body[:pos.Offset] + imp.String() + body[pos.Offset:]), nil
}

// lineDirective resumes the original numbering: the line after it is line
// of filename, the rest of the original line the woven text was inserted
// into.
func lineDirective(filename string, line int) string {
	return fmt.Sprintf("\n//line %s:%d\n", filename, line)
}

func nameOf(imports map[string]string, path string) string {
	for name, p := range imports {
		if p == path {
			return name
		}
	}
	return ""
}

// renameQualifier replaces the qualifier alias. with name. where alias is
// not the end of a longer identifier.
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
