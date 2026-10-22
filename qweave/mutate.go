package qweave

import (
	"fmt"
	"go/ast"
	"go/token"
	"hash/fnv"
	"path/filepath"
	"strings"
)

// qmutPath is the package woven mutants ask whether they are on.
const qmutPath = "github.com/casselc/quint-connect-go/qmut"

type mutant struct {
	line int
	fn   string
	op   string
	code string
}

// mutate weaves mutant schemata into the file: every mutant behind a
// switch (qmut.On), so that one build holds them all and a run turns one
// on. The operators: an if's condition negated; a comparison's boundary
// moved (< and <=, > and >=), for operands without side effects (both
// forms are evaluated); a call statement dropped.
func (w *fileWeaver) mutate() {
	alias := "quintqmut"
	if name := nameOf(w.imports, qmutPath); name != "" && name != "_" && name != "." {
		alias = name
	}
	h := fnv.New32a()
	h.Write([]byte(w.name))
	base := fmt.Sprintf("__qmut_%x", h.Sum32())
	var ms []mutant
	add := func(pos token.Pos, fn, op string, code ast.Node) string {
		ms = append(ms, mutant{line: w.line(pos), fn: fn, op: op, code: w.snippet(code)})
		return fmt.Sprintf("%s+%d", base, len(ms)-1)
	}
	// boundary is the comparison's mutated operator, if it is one to mutate
	boundary := func(e ast.Expr) (*ast.BinaryExpr, token.Token) {
		b, ok := e.(*ast.BinaryExpr)
		if !ok || !pure(b.X) || !pure(b.Y) {
			return nil, token.ILLEGAL
		}
		return b, map[token.Token]token.Token{token.LSS: token.LEQ, token.LEQ: token.LSS, token.GTR: token.GEQ, token.GEQ: token.GTR}[b.Op]
	}
	// pick renders a comparison (woven inside) as a Pick of its two forms
	pick := func(id string, b *ast.BinaryExpr, swap token.Token, inner func(ast.Node) string) string {
		x, y := inner(b.X), inner(b.Y)
		return fmt.Sprintf("%s.Pick(%s, %s %s %s, %s %s %s)", alias, id, x, b.Op, y, x, swap, y)
	}
	for _, d := range w.file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fn := w.p.path + "." + funcName(fd)
		conds := map[ast.Expr]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.IfStmt:
				cond := n.Cond
				conds[cond] = true
				id := add(cond.Pos(), fn, "negate if", cond)
				// a comparison that is the whole condition is mutated in the
				// same edit: two edits cannot replace one span
				b, swap := boundary(cond)
				cid := ""
				if swap != token.ILLEGAL {
					cid = add(b.OpPos, fn, fmt.Sprintf("%s for %s", swap, b.Op), b)
				}
				w.edits = append(w.edits, &edit{start: w.offset(cond.Pos()), end: w.offset(cond.End()), render: func(inner func(ast.Node) string) (string, error) {
					text := inner(cond)
					if cid != "" {
						text = pick(cid, b, swap, inner)
					}
					return fmt.Sprintf("%s.Cond(%s, %s)", alias, id, text), nil
				}})
			case *ast.BinaryExpr:
				if conds[n] {
					return true
				}
				if b, swap := boundary(n); swap != token.ILLEGAL {
					id := add(b.OpPos, fn, fmt.Sprintf("%s for %s", swap, b.Op), b)
					w.edits = append(w.edits, &edit{start: w.offset(n.Pos()), end: w.offset(n.End()), render: func(inner func(ast.Node) string) (string, error) {
						return pick(id, b, swap, inner), nil
					}})
				}
			case *ast.ExprStmt:
				call, ok := n.X.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "panic" || id.Name == "print" || id.Name == "println") {
					return true
				}
				mid := add(n.Pos(), fn, "drop call", n)
				w.edits = append(w.edits, &edit{start: w.offset(n.Pos()), end: w.offset(n.End()), render: func(inner func(ast.Node) string) (string, error) {
					return fmt.Sprintf("if !%s.On(%s) { %s }", alias, mid, inner(n)), nil
				}})
			}
			return true
		})
	}
	if len(ms) == 0 {
		return
	}
	if alias == "quintqmut" {
		w.need[alias] = qmutPath
	}
	rel := w.name
	if w.p.modDir != "" {
		if r, err := filepath.Rel(w.p.modDir, w.name); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	end := len(w.src)
	w.edits = append(w.edits, &edit{start: end, end: end, render: func(func(ast.Node) string) (string, error) {
		var reg strings.Builder
		fmt.Fprintf(&reg, "\nvar %s = %s.Register([]%s.Mutant{\n", base, alias, alias)
		for _, m := range ms {
			fmt.Fprintf(&reg, "\t{File: %q, Line: %d, Func: %q, Op: %q, Code: %q},\n", rel, m.line, m.fn, m.op, m.code)
		}
		reg.WriteString("})\n")
		return reg.String(), nil
	}})
}

// pure reports whether e can be evaluated twice: names, constants, field
// selections, parentheses, unary minus and not.
func pure(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return pure(e.X)
	case *ast.ParenExpr:
		return pure(e.X)
	case *ast.UnaryExpr:
		return (e.Op == token.SUB || e.Op == token.NOT) && pure(e.X)
	}
	return false
}

// snippet is a node's source, on one line and short, for reports.
func (w *fileWeaver) snippet(n ast.Node) string {
	s := strings.Join(strings.Fields(string(w.src[w.offset(n.Pos()):w.offset(n.End())])), " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
