package qweave

import (
	"fmt"
	"go/ast"
	"go/token"
	"hash/fnv"
	"path/filepath"
	"strings"
)

// qcoverPath is the package woven blocks report to.
const qcoverPath = "github.com/casselc/quint-connect-go/qcover"

// coverBlock is a block found for instrumentation.
type coverBlock struct {
	at                 int // offset of the inserted call
	line, col, endLine int
	stmts              int
	fn                 string
}

// instrument adds block coverage to the file: a call of qcover.Hit at the
// start of every straight-line run of statements (a new one after each
// compound or branching statement, as cmd/cover splits them), and the
// file's blocks registered at package initialization. Insertions stay on
// their lines, so positions do not move.
func (w *fileWeaver) instrument() {
	var bs []coverBlock
	fn := "init"
	list := func(stmts []ast.Stmt, open token.Pos) {
		if len(stmts) == 0 {
			p := w.p.fset.Position(open)
			bs = append(bs, coverBlock{at: p.Offset + 1, line: p.Line, col: p.Column, endLine: p.Line, fn: fn})
			return
		}
		start := 0
		for i, s := range stmts {
			last := i == len(stmts)-1
			if !last && !splits(s) {
				continue
			}
			first := w.p.fset.Position(stmts[start].Pos())
			end := w.p.fset.Position(s.End())
			bs = append(bs, coverBlock{at: first.Offset, line: first.Line, col: first.Column, endLine: end.Line, stmts: i - start + 1, fn: fn})
			start = i + 1
		}
	}
	for _, d := range w.file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			fn = w.p.path + "." + funcName(fd)
		} else {
			fn = w.p.path + ".init"
		}
		// a switch's or select's body holds clauses, not statements: the
		// clauses are instrumented, not the body
		clauses := map[*ast.BlockStmt]bool{}
		ast.Inspect(d, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SwitchStmt:
				clauses[n.Body] = true
			case *ast.TypeSwitchStmt:
				clauses[n.Body] = true
			case *ast.SelectStmt:
				clauses[n.Body] = true
			case *ast.BlockStmt:
				if !clauses[n] {
					list(n.List, n.Lbrace)
				}
			case *ast.CaseClause:
				list(n.Body, n.Colon)
			case *ast.CommClause:
				list(n.Body, n.Colon)
			}
			return true
		})
	}
	if len(bs) == 0 {
		return
	}
	alias := "quintqcover"
	if name := nameOf(w.imports, qcoverPath); name != "" && name != "_" && name != "." {
		alias = name
	} else {
		w.need[alias] = qcoverPath
	}
	h := fnv.New32a()
	h.Write([]byte(w.name))
	base := fmt.Sprintf("__qcov_%x", h.Sum32())
	rel := w.name
	if w.p.modDir != "" {
		if r, err := filepath.Rel(w.p.modDir, w.name); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	var reg strings.Builder
	fmt.Fprintf(&reg, "\nvar %s = %s.Register([]%s.Block{\n", base, alias, alias)
	for i, b := range bs {
		text := fmt.Sprintf("%s.Hit(%s+%d); ", alias, base, i)
		w.edits = append(w.edits, &edit{start: b.at, end: b.at, render: func(func(ast.Node) string) (string, error) { return text, nil }})
		fmt.Fprintf(&reg, "\t{File: %q, Line: %d, Col: %d, EndLine: %d, Func: %q, Stmts: %d},\n", rel, b.line, b.col, b.endLine, b.fn, b.stmts)
	}
	reg.WriteString("})\n")
	end := len(w.src)
	text := reg.String()
	w.edits = append(w.edits, &edit{start: end, end: end, render: func(func(ast.Node) string) (string, error) { return text, nil }})
}

// splits reports whether a block ends after s: control may leave or
// branch there.
func splits(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt,
		*ast.SelectStmt, *ast.BlockStmt, *ast.LabeledStmt, *ast.ReturnStmt, *ast.BranchStmt, *ast.GoStmt, *ast.DeferStmt:
		return true
	case *ast.ExprStmt:
		if c, ok := s.X.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	if ix, ok := t.(*ast.IndexListExpr); ok {
		t = ix.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return "(" + id.Name + ")." + fd.Name.Name
	}
	return fd.Name.Name
}
