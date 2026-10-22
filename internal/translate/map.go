package translate

import (
	"go/ast"
	"go/token"
	"go/types"
	"log"

	"github.com/dave/dst"
	"github.com/dave/dst/dstutil"
)

type MapType struct {
	Type  *types.Map
	Named types.Type
}

func isTypMapType(typ types.Type) (MapType, bool) {
	if mapTyp, ok := typ.Underlying().(*types.Map); ok {
		return MapType{Type: mapTyp, Named: typ}, true
	}

	if param, ok := typ.(*types.TypeParam); ok {
		iface := param.Constraint().Underlying().(*types.Interface)
		if iface.NumEmbeddeds() == 1 {
			if union, ok := iface.EmbeddedType(0).(*types.Union); ok {
				if union.Len() == 1 {
					if mapType, ok := union.Term(0).Type().(*types.Map); ok {
						return MapType{Type: mapType, Named: typ}, true
					}
				}
			}
		}
	}

	return MapType{}, false
}

func (t *packageTranslator) isMapType(expr dst.Expr) (MapType, bool) {
	if astExpr, ok := t.astMap.Nodes[expr].(ast.Expr); ok {
		if convertedType, ok := t.implicitConversions[astExpr]; ok {
			// check here in case we pass a map[string]string to an any field
			if typ, ok := isTypMapType(convertedType); ok {
				return typ, ok
			}
		}
	}

	if typ, ok := t.getType(expr); ok {
		return isTypMapType(typ)
	}

	return MapType{}, false
}

func (t *packageTranslator) maybeConvertMapType(x dst.Expr, typ MapType) dst.Expr {
	if typ.Named == typ.Type {
		return x
	}

	return &dst.CallExpr{
		Fun: t.makeTypeExpr(typ.Named),
		Args: []dst.Expr{
			x,
		},
	}
}

// mapForRewrite checks if dst.Expr has a map type and prepares it for
// rewriting
func (t *packageTranslator) mapForRewrite(x dst.Expr) (dst.Expr, bool) {
	typ, ok := t.isMapType(x)
	if !ok {
		return nil, false
	}
	x = t.apply(x).(dst.Expr)
	if typ.Named != typ.Type {
		x = &dst.CallExpr{Fun: t.newRuntimeSelector("ExtractMap"), Args: []dst.Expr{x}}
	}
	return x, true
}

// mapForRewrite checks if dst.Expr is dst.IndexExpr into a map type and
// prepares it for rewriting
func (t *packageTranslator) mapIndexForRewrite(node dst.Node) (dst.Expr, dst.Expr, bool) {
	expr, ok := node.(dst.Expr)
	if !ok {
		return nil, nil, false
	}
	expr = dstutil.Unparen(expr)
	if indexExpr, ok := expr.(*dst.IndexExpr); ok {
		mp, ok := t.mapForRewrite(indexExpr.X)
		if !ok {
			return nil, nil, false
		}

		return mp, t.apply(indexExpr.Index).(dst.Expr), true
	}
	return nil, nil, false
}

func (t *packageTranslator) rewriteMapRange(c *dstutil.Cursor) {
	// map range
	if rangeStmt, ok := c.Node().(*dst.RangeStmt); ok {
		if x, ok := t.mapForRewrite(rangeStmt.X); ok {
			rangeStmt.X = &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   x,
					Sel: dst.NewIdent("Range"),
				},
			}
		}
	}
}

func (t *packageTranslator) rewriteMapDelete(c *dstutil.Cursor) {
	// delete from map
	if callExpr, ok := c.Node().(*dst.CallExpr); ok && t.isNamedBuiltIn(callExpr.Fun, "delete") {
		if x, ok := t.mapForRewrite(callExpr.Args[0]); ok {
			c.Replace(&dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   x,
					Sel: dst.NewIdent("Delete"),
				},
				Args: []dst.Expr{
					t.apply(callExpr.Args[1]).(dst.Expr),
				},
			})
		}
	}
}

func (t *packageTranslator) rewriteMapClear(c *dstutil.Cursor) {
	// delete from map
	if callExpr, ok := c.Node().(*dst.CallExpr); ok && t.isNamedBuiltIn(callExpr.Fun, "clear") {
		if x, ok := t.mapForRewrite(callExpr.Args[0]); ok {
			c.Replace(&dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   x,
					Sel: dst.NewIdent("Clear"),
				},
			})
		}
	}
}

func (t *packageTranslator) rewriteMakeMap(c *dstutil.Cursor) {
	if makeExpr, ok := c.Node().(*dst.CallExpr); ok && t.isNamedBuiltIn(makeExpr.Fun, "make") {
		if typ, ok := t.isMapType(makeExpr); ok {
			c.Replace(t.maybeConvertMapType(&dst.CallExpr{
				Fun: &dst.IndexListExpr{
					X: t.newRuntimeSelector("NewMap"),
					Indices: []dst.Expr{
						t.makeTypeExpr(typ.Type.Key()),
						t.makeTypeExpr(typ.Type.Elem()),
					},
				},
				Args: []dst.Expr{},
			}, typ))
		}
	}
}

func (t *packageTranslator) rewriteMapNil(c *dstutil.Cursor) {
	// map literal
	if ident, ok := c.Node().(*dst.Ident); ok && ident.Name == "nil" {
		if mapType, ok := t.isMapType(ident); ok {
			c.Replace(t.maybeConvertMapType(&dst.CallExpr{
				Fun: &dst.IndexListExpr{
					X: t.newRuntimeSelector("NilMap"),
					Indices: []dst.Expr{
						t.makeTypeExpr(mapType.Type.Key()),
						t.makeTypeExpr(mapType.Type.Elem()),
					},
				},
			}, mapType))
		}
	}
}

func (t *packageTranslator) rewriteMapLiteral(c *dstutil.Cursor) {
	// map literal
	compositeLit, ok := c.Node().(*dst.CompositeLit)
	if !ok {
		return
	}
	typ, ok := t.isMapType(compositeLit)
	if !ok {
		return
	}

	var pairs []dst.Expr
	for _, elt := range compositeLit.Elts {
		kv, ok := elt.(*dst.KeyValueExpr)
		if !ok {
			log.Fatal("map composite lit elt is not keyvalueexpr")
		}
		nodeDecs := kv.Decs.NodeDecs
		kv.Decs.NodeDecs = dst.NodeDecs{
			Start: nodeDecs.Start,
		}
		nodeDecs.Start = nil

		key := t.apply(kv.Key).(dst.Expr)
		value := t.apply(kv.Value).(dst.Expr)

		if nestedLit, ok := key.(*dst.CompositeLit); ok && nestedLit.Type == nil {
			nestedLit.Type = t.makeTypeExpr(typ.Type.Key())
		}
		if nestedLit, ok := value.(*dst.CompositeLit); ok && nestedLit.Type == nil {
			// XXX: janky hack
			if ptr, ok := typ.Type.Elem().Underlying().(*types.Pointer); ok {
				nestedLit.Type = t.makeTypeExpr(ptr.Elem())
				value = &dst.UnaryExpr{
					Op: token.AND,
					X:  value,
				}
			} else {
				nestedLit.Type = t.makeTypeExpr(typ.Type.Elem())
			}
		}

		pairs = append(pairs, &dst.CompositeLit{
			Elts: []dst.Expr{
				&dst.KeyValueExpr{
					Key:   dst.NewIdent("K"),
					Value: key,
				},
				&dst.KeyValueExpr{
					Key:   dst.NewIdent("V"),
					Value: value,
				},
			},
			Decs: dst.CompositeLitDecorations{
				NodeDecs: nodeDecs,
			},
		})
	}

	c.Replace(t.maybeConvertMapType(&dst.CallExpr{
		Fun: t.newRuntimeSelector("MapLiteral"),
		Args: []dst.Expr{
			&dst.CompositeLit{
				Type: &dst.ArrayType{
					Elt: &dst.IndexListExpr{
						X: t.newRuntimeSelector("KV"),
						Indices: []dst.Expr{
							t.makeTypeExpr(typ.Type.Key()),
							t.makeTypeExpr(typ.Type.Elem()),
						},
					},
				},
				Elts: pairs,
			},
		},
	}, typ))
}

func (t *packageTranslator) rewriteMapImplicitConversion(c *dstutil.Cursor) {
	if expr, ok := c.Node().(dst.Expr); ok {
		astExpr, _ := t.astMap.Nodes[expr].(ast.Expr)
		if typ, ok := t.implicitConversions[astExpr]; ok {
			if _, ok := isTypMapType(typ); ok {
				c.Replace(&dst.CallExpr{
					Fun:  t.makeTypeExpr(typ),
					Args: []dst.Expr{expr},
				})
			}
		}
	}
}

func (t *packageTranslator) rewriteMapLen(c *dstutil.Cursor) {
	if lenExpr, ok := c.Node().(*dst.CallExpr); ok && t.isNamedBuiltIn(lenExpr.Fun, "len") {
		if x, ok := t.mapForRewrite(lenExpr.Args[0]); ok {
			lenExpr.Fun = &dst.SelectorExpr{
				X:   x,
				Sel: dst.NewIdent("Len"),
			}
			lenExpr.Args = nil
		}
	}
}

func (t *packageTranslator) rewriteMapType(c *dstutil.Cursor) {
	// internal/sync's HashTrieMap (sync.Map since Go 1.24) declares a
	// map[K]V only to take the runtime's hasher from its type descriptor.
	// It must stay a real map type.
	// TODO: the runtime hasher is seeded per process, so sync.Map.Range
	// order is not deterministic across runs.
	if t.pkgPath == "internal/sync" {
		return
	}
	// map type
	if mapType, ok := c.Node().(*dst.MapType); ok {
		c.Replace(&dst.IndexListExpr{
			X: t.newRuntimeSelector("Map"),
			Indices: []dst.Expr{
				mapType.Key,
				t.apply(mapType.Value).(dst.Expr),
			},
		})
	}

	// for generics, ~map[K]V becomes ~struct{ Impl *MapImpl[K, V] }, the
	// underlying type of every translated map type, so that named map types
	// (like textproto.MIMEHeader passed to maps.Keys) still satisfy it.
	if unary, ok := c.Node().(*dst.UnaryExpr); ok && unary.Op == token.TILDE {
		if mapType, ok := unary.X.(*dst.MapType); ok {
			unary.X = &dst.StructType{Fields: &dst.FieldList{List: []*dst.Field{{
				Names: []*dst.Ident{dst.NewIdent("Impl")},
				Type: &dst.StarExpr{X: &dst.IndexListExpr{
					X: t.newRuntimeSelector("MapImpl"),
					Indices: []dst.Expr{
						mapType.Key,
						t.apply(mapType.Value).(dst.Expr),
					},
				}},
			}}}}
		}
	}
}

func (t *packageTranslator) rewriteMapIndex(c *dstutil.Cursor) {
	if x, index, ok := t.mapIndexForRewrite(c.Node()); ok {
		// we catch (val, ok) cases earlier
		c.Replace(&dst.CallExpr{
			Fun: &dst.SelectorExpr{
				X:   x,
				Sel: dst.NewIdent("Get"),
			},
			Args: []dst.Expr{
				index,
			},
		})
	}
}

func (t *packageTranslator) rewriteMapGetOk(c *dstutil.Cursor) {
	// get from map in v, ok = m[k]

	rhs, ok := isDualAssign(c)
	if !ok {
		return
	}

	if x, index, ok := t.mapIndexForRewrite(*rhs); ok {
		*rhs = &dst.CallExpr{
			Fun: &dst.SelectorExpr{
				X:   x,
				Sel: dst.NewIdent("GetOk"),
			},
			Args: []dst.Expr{
				index,
			},
		}
	}
}

func isOpAssign(tok token.Token) (token.Token, bool) {
	ops := map[token.Token]token.Token{
		token.ADD_ASSIGN:     token.ADD,
		token.SUB_ASSIGN:     token.SUB,
		token.MUL_ASSIGN:     token.MUL,
		token.QUO_ASSIGN:     token.QUO,
		token.REM_ASSIGN:     token.REM,
		token.AND_ASSIGN:     token.AND,
		token.OR_ASSIGN:      token.OR,
		token.XOR_ASSIGN:     token.XOR,
		token.SHL_ASSIGN:     token.SHL,
		token.SHR_ASSIGN:     token.SHR,
		token.AND_NOT_ASSIGN: token.AND_NOT,
	}
	out, ok := ops[tok]
	return out, ok
}

func (t *packageTranslator) replaceWithMapModify(c *dstutil.Cursor, x, index dst.Expr, op token.Token, val dst.Expr) {
	suffix := t.suffix()
	keyName := "key" + suffix
	mapName := "map" + suffix

	copyVarsStmt := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent(mapName), dst.NewIdent(keyName)},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{x, index},
	}
	c.InsertBefore(copyVarsStmt)

	writeExpr := &dst.CallExpr{
		Fun: &dst.SelectorExpr{
			X:   dst.NewIdent(mapName),
			Sel: dst.NewIdent("Set"),
		},
		Args: []dst.Expr{
			dst.NewIdent(keyName),
			&dst.BinaryExpr{
				X: &dst.CallExpr{
					Fun: &dst.SelectorExpr{
						X:   dst.NewIdent(mapName),
						Sel: dst.NewIdent("Get"),
					},
					Args: []dst.Expr{
						dst.NewIdent(keyName),
					},
				},
				Op: op,
				Y:  t.apply(val).(dst.Expr),
			},
		},
	}
	c.Replace(&dst.ExprStmt{
		Decs: dst.ExprStmtDecorations{
			NodeDecs: *c.Node().Decorations(),
		},
		X: writeExpr,
	})
}

func (t *packageTranslator) rewriteMapAssign(c *dstutil.Cursor) {
	// "m[x]++""
	if incDecStmt, ok := c.Node().(*dst.IncDecStmt); ok {
		x, index, ok := t.mapIndexForRewrite(incDecStmt.X)
		if !ok {
			return
		}

		op := map[token.Token]token.Token{
			token.INC: token.ADD,
			token.DEC: token.SUB,
		}[incDecStmt.Tok]

		t.replaceWithMapModify(c, x, index, op, &dst.BasicLit{Kind: token.INT, Value: "1"})
		return
	}

	// "m[x] =" or "m[x] +="
	assignStmt, ok := c.Node().(*dst.AssignStmt)
	if !ok {
		return
	}

	if op, ok := isOpAssign(assignStmt.Tok); ok {
		// m[x] +=
		if len(assignStmt.Lhs) != 1 || len(assignStmt.Rhs) != 1 {
			panic("special assign with multiple lhs or rhs?")
		}
		x, index, ok := t.mapIndexForRewrite(assignStmt.Lhs[0])
		if !ok {
			return
		}
		t.replaceWithMapModify(c, x, index, op, assignStmt.Rhs[0])
		return
	}

	if len(assignStmt.Lhs) == 1 {
		// m[x] =
		x, index, ok := t.mapIndexForRewrite(assignStmt.Lhs[0])
		if !ok {
			return
		}
		c.Replace(&dst.ExprStmt{
			Decs: dst.ExprStmtDecorations{
				NodeDecs: assignStmt.Decs.NodeDecs,
			},
			X: &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   x,
					Sel: dst.NewIdent("Set"),
				},
				Args: []dst.Expr{
					index,
					t.apply(assignStmt.Rhs[0]).(dst.Expr),
				},
			},
		})
		return
	}

	// m[x], m[y], a, b =
	for i, lhs := range assignStmt.Lhs {
		x, index, ok := t.mapIndexForRewrite(lhs)
		if !ok {
			continue
		}

		if assignStmt.Tok != token.ASSIGN {
			panic("non-name on left side of := ?")
		}

		// get the type of the map again, now for a temp var
		indexExpr := lhs.(*dst.IndexExpr)
		mapType, _ := t.isMapType(indexExpr.X)
		elemType := mapType.Type.Elem()

		tmp := "val" + t.suffix()
		c.InsertBefore(&dst.DeclStmt{
			Decl: &dst.GenDecl{
				Tok: token.VAR,
				Specs: []dst.Spec{
					&dst.ValueSpec{
						Names: []*dst.Ident{
							dst.NewIdent(tmp),
						},
						Type: t.makeTypeExpr(elemType),
					},
				},
			},
		})
		assignStmt.Lhs[i] = dst.NewIdent(tmp)
		c.InsertAfter(&dst.ExprStmt{
			Decs: dst.ExprStmtDecorations{
				NodeDecs: assignStmt.Decs.NodeDecs,
			},
			X: &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   x,
					Sel: dst.NewIdent("Set"),
				},
				Args: []dst.Expr{
					index,
					dst.NewIdent(tmp),
				},
			},
		})
	}
}
