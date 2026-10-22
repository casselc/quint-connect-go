// Package qweave weaves Orchestrion aspects (https://datadoghq.dev/orchestrion)
// into Go source, from their own aspect files (orchestrion.yml), with type
// information: the woven files are handed to the go command as an overlay
// (go build -overlay), or to gosim's translator (GOSIM_OVERLAY). Woven code
// may import what the original did not, and keeps the original's positions
// (//line directives).
//
// It reads the aspect files of the packages a module's tool file imports
// (gosim.tool.go or orchestrion.tool.go) and supports the join points and
// advice quint-connect-go's aspects use:
//   - join points: function-body (with a directive), method-call (receiver,
//     name, value-only or pointer-only), function-call, function (a
//     function literal), directive (on the enclosing statement),
//     package-filter (root), and all-of, one-of, not;
//   - advice: prepend-statements and wrap-expression, with Orchestrion's
//     template data (.Function.ArgumentOfType and .ResultOfType,
//     .DirectiveArgs, .AST.Fun.X, .AST.Args, and the expression itself).
//
// With types, it weaves what Orchestrion's syntactic matching misses: a
// method value (return mu.RUnlock) becomes a function literal around the
// advised call, and a call promoted through an embedded field
// (t.Lock() on struct{ sync.Mutex }) matches the field's type and advises
// the call on the field (t.Mutex).
//
// Aspects using anything else are skipped and reported.
package qweave

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// An Aspect is a join point and its advice.
type Aspect struct {
	ID     string
	Join   Join
	Advice []Advice
	File   string // the aspect file it came from
}

// Join is a join point: a predicate on a node in its context.
type Join interface{ join() }

// The join points.
type (
	// AllOf, OneOf and Not combine join points.
	AllOf []Join
	OneOf []Join
	Not   struct{ J Join }
	// FunctionBody is a function declaration whose doc carries Directive.
	FunctionBody struct{ Directive string }
	// MethodCall is a call (or, with types, a method value) of Name on a
	// receiver of type Receiver ("sync.Mutex"); Match is "value-only"
	// (the receiver expression is a T), "pointer-only" (a *T) or "" (both).
	MethodCall struct{ Receiver, Name, Match string }
	// FunctionCall is a call of a package-level function ("pkg/path.Name").
	FunctionCall struct{ Path, Name string }
	// Function is a function literal.
	Function struct{}
	// Directive holds when the enclosing statement is preceded by the
	// directive (//qenv:go before a go statement).
	Directive struct{ Name string }
	// PackageFilter holds in the main module's packages (Root).
	PackageFilter struct{ Root bool }
)

func (AllOf) join()         {}
func (OneOf) join()         {}
func (Not) join()           {}
func (FunctionBody) join()  {}
func (MethodCall) join()    {}
func (FunctionCall) join()  {}
func (Function) join()      {}
func (Directive) join()     {}
func (PackageFilter) join() {}

// Advice is what is done at a join point: prepend statements to a
// function's body, or replace an expression.
type Advice struct {
	Kind    string            // "prepend-statements" or "wrap-expression"
	Imports map[string]string // alias -> import path the template may use
	tmpl    *template.Template
	text    string
}

// A Skipped aspect uses what qweave does not support.
type Skipped struct {
	File, ID, Why string
}

func (s Skipped) String() string { return fmt.Sprintf("%s: aspect %s: %s", s.File, s.ID, s.Why) }

type unsupported string

func (u unsupported) Error() string { return string(u) }

func parseJoin(n *yaml.Node) (Join, error) {
	if n.Kind == yaml.SequenceNode {
		return parseJoins(n, func(js []Join) Join { return AllOf(js) })
	}
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return nil, unsupported("a join point is a mapping with one key")
	}
	key, val := n.Content[0].Value, n.Content[1]
	switch key {
	case "all-of":
		return parseJoins(val, func(js []Join) Join { return AllOf(js) })
	case "one-of":
		return parseJoins(val, func(js []Join) Join { return OneOf(js) })
	case "not":
		j, err := parseJoin(val)
		return Not{j}, err
	case "function-body":
		var fb struct {
			Directive string `yaml:"directive"`
		}
		if err := val.Decode(&fb); err != nil || fb.Directive == "" {
			return nil, unsupported("function-body without a directive")
		}
		return FunctionBody{fb.Directive}, nil
	case "method-call":
		var mc struct{ Receiver, Name, Match string }
		if err := val.Decode(&mc); err != nil {
			return nil, err
		}
		if mc.Match != "" && mc.Match != "value-only" && mc.Match != "pointer-only" {
			return nil, unsupported("method-call match " + mc.Match)
		}
		return MethodCall{mc.Receiver, mc.Name, mc.Match}, nil
	case "function-call":
		i := strings.LastIndex(val.Value, ".")
		if val.Kind != yaml.ScalarNode || i < 0 {
			return nil, unsupported("function-call is pkg/path.Name")
		}
		return FunctionCall{val.Value[:i], val.Value[i+1:]}, nil
	case "function":
		if val.Kind != yaml.SequenceNode || len(val.Content) > 0 {
			return nil, unsupported("function with options")
		}
		return Function{}, nil
	case "directive":
		return Directive{val.Value}, nil
	case "package-filter":
		var pf struct {
			Root bool `yaml:"root"`
		}
		if err := val.Decode(&pf); err != nil || !pf.Root {
			return nil, unsupported("package-filter other than root")
		}
		return PackageFilter{true}, nil
	}
	return nil, unsupported("join point " + key)
}

func parseJoins(n *yaml.Node, mk func([]Join) Join) (Join, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, unsupported("all-of/one-of take a list")
	}
	var js []Join
	for _, c := range n.Content {
		j, err := parseJoin(c)
		if err != nil {
			return nil, err
		}
		js = append(js, j)
	}
	return mk(js), nil
}

// Load reads an aspect file. Aspects it cannot weave are Skipped.
func Load(path string) ([]*Aspect, []Skipped, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var f struct {
		Aspects []struct {
			ID        string                 `yaml:"id"`
			JoinPoint yaml.Node              `yaml:"join-point"`
			Advice    []map[string]yaml.Node `yaml:"advice"`
		} `yaml:"aspects"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []*Aspect
	var skipped []Skipped
	for _, a := range f.Aspects {
		j, err := parseJoin(&a.JoinPoint)
		if err != nil {
			if _, ok := err.(unsupported); !ok {
				return nil, nil, fmt.Errorf("%s: aspect %s: %w", path, a.ID, err)
			}
			skipped = append(skipped, Skipped{path, a.ID, err.Error()})
			continue
		}
		asp := &Aspect{ID: a.ID, Join: j, File: path}
		var why string
		for _, adv := range a.Advice {
			for kind, node := range adv {
				if kind != "prepend-statements" && kind != "wrap-expression" {
					why = "advice " + kind
					continue
				}
				var body struct {
					Imports  map[string]string `yaml:"imports"`
					Template string            `yaml:"template"`
				}
				if err := node.Decode(&body); err != nil {
					return nil, nil, fmt.Errorf("%s: aspect %s: %w", path, a.ID, err)
				}
				t, err := template.New(a.ID).Parse(body.Template)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: aspect %s: %w", path, a.ID, err)
				}
				asp.Advice = append(asp.Advice, Advice{Kind: kind, Imports: body.Imports, tmpl: t, text: body.Template})
			}
		}
		if why != "" || len(asp.Advice) != 1 {
			if why == "" {
				why = "more than one advice"
			}
			skipped = append(skipped, Skipped{path, a.ID, why})
			continue
		}
		out = append(out, asp)
	}
	return out, skipped, nil
}

// Hash identifies a set of aspects, for caches of woven output.
func Hash(aspects []*Aspect) string {
	var b strings.Builder
	for _, a := range aspects {
		fmt.Fprintf(&b, "%s\x00%s\x00%#v\x00", a.File, a.ID, a.Join)
		for _, adv := range a.Advice {
			keys := make([]string, 0, len(adv.Imports))
			for k := range adv.Imports {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(&b, "%s\x00%s\x00", adv.Kind, adv.text)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s=%s\x00", k, adv.Imports[k])
			}
		}
	}
	return hash(b.String())
}
