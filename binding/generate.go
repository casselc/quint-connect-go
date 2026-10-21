package binding

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/casselc/quint-connect-go/qtrace"
)

// Generated is a Quint module that replays one observed trace.
type Generated struct {
	Module   string         // module name
	Text     string         // the .qnt source
	LineStep map[int]int    // source line -> index into Calls
	Calls    []Call         // the replayed steps, in order, including inferred ones
	Tests    GeneratedTests // names of the runs in Text
}

// GeneratedTests names the runs a generated module holds.
type GeneratedTests struct {
	Conforms   string            // replays every step; checks each step's expect
	Invariants map[string]string // invariant -> run that checks it in every state
	// All, when there are invariants, replays the steps once checking each
	// step's expect and every invariant: the fast path. If it passes, so
	// do Conforms and every invariant run; if not, those runs say which
	// check failed, and where.
	All string
}

// Call is one step of the replay.
type Call struct {
	Quint    string            // e.g. writeTable({ payload: 1, ... }, Ok)
	Expect   string            // post-state check, or ""
	Step     *qtrace.Step      // the observed step; nil for inferred steps
	Inferred string            // why the step was inserted ("restart"), if inferred
	Action   string            // the model action
	Args     map[string]string // parameter -> the Quint expression passed
	Note     string            // description, for calls that come from an ITF state
}

// Plan is the binding applied to one actor's trace: the instance's
// constants and the calls, in model terms. It needs no Quint tooling.
type Plan struct {
	Actor         string
	Processes     []string
	ObservedSteps int
	Constants     map[string]string // constant -> Quint expression
	Calls         []Call
}

// ConstantList renders the constants as "NAME = expr", sorted.
func (p *Plan) ConstantList() []string {
	var out []string
	for _, c := range sortedKeys(p.Constants) {
		out = append(out, fmt.Sprintf("%s = %s", c, p.Constants[c]))
	}
	return out
}

// Generate builds the replay module for one actor's trace. qntPath is where
// the module will be written (the model import is made relative to it).
func (b *Binding) Generate(m *Model, t *qtrace.Trace, qntPath string) (*Generated, error) {
	p, err := b.Plan(m, t)
	if err != nil {
		return nil, err
	}
	return RenderModule(ModuleSpec{
		Spec: b.SpecPath(), Module: b.Module, Constants: p.ConstantList(), Defs: b.Defs, Invariants: b.Invariants,
		Header: []string{fmt.Sprintf("actor %q, %d observed steps, processes %v", t.Actor, len(t.Steps), t.Processes)},
	}, p.Calls, qntPath)
}

// Plan applies the binding to a trace. With a model, parameters are taken
// in the model's order and checked; with m == nil (emitting a model-level
// ITF in-process, without Quint) they are keyed by name only.
func (b *Binding) Plan(m *Model, t *qtrace.Trace) (*Plan, error) {
	// 1. Intern opaque identifiers, in trace order.
	type ikey struct{ arg, scope string }
	tables := map[ikey]map[string]int64{}
	procIdx := map[string]int{}
	for i, p := range t.Processes {
		procIdx[p] = i + 1
	}
	args := make([]map[string]any, len(t.Steps))
	for i := range t.Steps {
		s := &t.Steps[i]
		a := map[string]any{}
		for k, v := range s.Args {
			scope, ok := b.Intern[k]
			if !ok {
				a[k] = v
				continue
			}
			key := ikey{k, ""}
			if scope == "process" {
				key.scope = s.Process
			}
			tb := tables[key]
			if tb == nil {
				tb = map[string]int64{}
				tables[key] = tb
			}
			id := fmt.Sprint(v)
			if _, ok := tb[id]; !ok {
				tb[id] = int64(len(tb) + 1)
			}
			a[k] = tb[id]
		}
		args[i] = a
	}

	// 2. Constants, from statistics over the interned trace.
	funcs := template.FuncMap{
		"set": func(names ...string) string {
			seen := map[string]bool{}
			var vs []string
			for _, a := range args {
				for _, arg := range names {
					if v, ok := a[arg]; ok && !seen[quintLit(v)] {
						seen[quintLit(v)] = true
						vs = append(vs, quintLit(v))
					}
				}
			}
			sort.Strings(vs)
			return "Set(" + strings.Join(vs, ", ") + ")"
		},
		"toSet":  func(v any) (string, error) { return jsonQuint(v, "Set") },
		"toList": func(v any) (string, error) { return jsonQuint(v, "List") },
		"toMap":  func(v any) (string, error) { return jsonQuint(v, "Map") },
		"max": func(arg string) int64 {
			var mx int64
			for _, a := range args {
				if v, ok := a[arg].(int64); ok && v > mx {
					mx = v
				}
			}
			return mx
		},
		"count": func(action string) int {
			n := 0
			for _, s := range t.Steps {
				if s.Action == action {
					n++
				}
			}
			return n
		},
		"procs": func() int { return len(t.Processes) },
		"add":   func(a, b any) int64 { return toI(a) + toI(b) },
		"atLeast": func(lo, v any) int64 {
			if toI(v) < toI(lo) {
				return toI(lo)
			}
			return toI(v)
		},
	}
	plan := &Plan{Actor: t.Actor, Processes: t.Processes, ObservedSteps: len(t.Steps), Constants: map[string]string{}}
	for _, c := range sortedKeys(b.Constants) {
		v, err := render(b.Constants[c], nil, funcs)
		if err != nil {
			return nil, fmt.Errorf("constant %s: %w", c, err)
		}
		plan.Constants[c] = v
	}

	// 3. Calls.
	prevProc := ""
	for i := range t.Steps {
		s := &t.Steps[i]
		if prevProc != "" && s.Process != prevProc && b.Restart != "" {
			plan.Calls = append(plan.Calls, Call{Quint: b.Restart, Action: b.Restart, Args: map[string]string{},
				Inferred: fmt.Sprintf("restart: process %s -> %s", short(prevProc), short(s.Process))})
		} else if prevProc != "" && s.Process != prevProc {
			return nil, fmt.Errorf("step %d (%s): process changed from %s to %s and the binding has no restart action", i, s.Action, prevProc, s.Process)
		}
		prevProc = s.Process
		ab := b.Actions[s.Action]
		var params []Param
		if m != nil {
			ps, ok := m.Actions[s.Action]
			if !ok {
				return nil, fmt.Errorf("step %d: %s is not an action of %s", i, s.Action, m.Module)
			}
			params = ps
		} else {
			for _, p := range sortedKeys(ab.Args) {
				params = append(params, Param{Name: p})
			}
		}
		outcome, err := b.outcome(ab, s)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
		if outcome == "skip" {
			continue
		}
		data := stepData(s, args[i], procIdx[s.Process], outcome)
		c := Call{Step: s, Action: s.Action, Args: map[string]string{}}
		var vals []string
		for _, p := range params {
			tpl := ab.Template(p)
			v, err := render(tpl, data, funcs)
			if err != nil {
				return nil, fmt.Errorf("step %d (%s, %s): arg %s (template %s, signature %s): %w", i, s.Action, s.Source, p.Name, tpl, sig(m, s.Action), err)
			}
			c.Args[p.Name] = v
			vals = append(vals, v)
		}
		c.Quint = s.Action
		if len(vals) > 0 {
			c.Quint += "(" + strings.Join(vals, ", ") + ")"
		}
		if ab.Expect != "" {
			if c.Expect, err = render(ab.Expect, data, funcs); err != nil {
				return nil, fmt.Errorf("step %d (%s): expect: %w", i, s.Action, err)
			}
		}
		plan.Calls = append(plan.Calls, c)
	}
	return plan, nil
}

// ModuleSpec says what a generated replay module imports and checks.
type ModuleSpec struct {
	Spec       string   // the model file
	Module     string   // parameterised module, instantiated with Constants
	Constants  []string // "NAME = expr"
	Instance   string   // if set, import this (already instantiated) module instead
	Defs       string   // Quint helpers
	Invariants []string
	InitExpect string   // checked in the initial state, if set
	Header     []string // comment lines
	Tests      TestSet  // which runs to write (default: all)
}

// TestSet selects the runs of a replay module.
type TestSet int

const (
	// AllTests writes the fused run and the separate ones.
	AllTests TestSet = iota
	// FastTests writes only the fused run (allTest), or only conformsTest
	// when there are no invariants: one replay that decides a passing run.
	FastTests
	// DetailTests writes conformsTest and one run per invariant: they say
	// which check failed, and where.
	DetailTests
)

// segmentSteps is the longest `.then` chain written as one expression.
const segmentSteps = 20

// RenderModule writes the replay module for calls: a conformance run with
// each call's expect, and one run per invariant.
func RenderModule(ms ModuleSpec, calls []Call, qntPath string) (*Generated, error) {
	gs, err := RenderBatch(ms, [][]Call{calls}, qntPath)
	if err != nil {
		return nil, err
	}
	return gs[0], nil
}

// RenderBatch writes one module replaying several runs of the same
// instance (same ModuleSpec): the runs of run i are prefixed r<i>_, so one
// `quint test` process checks them all. Each returned Generated describes
// one run; all share Text. With a single run there is no prefix.
func RenderBatch(ms ModuleSpec, runs [][]Call, qntPath string) ([]*Generated, error) {
	rel, err := filepath.Rel(filepath.Dir(qntPath), strings.TrimSuffix(ms.Spec, ".qnt"))
	if err != nil {
		return nil, err
	}
	var w strings.Builder
	line := 1
	pr := func(format string, a ...any) {
		s := fmt.Sprintf(format, a...)
		w.WriteString(s)
		w.WriteString("\n")
		line += strings.Count(s, "\n") + 1
	}
	pr("// Generated by quintgo from observed telemetry. Do not edit.")
	for _, h := range ms.Header {
		pr("// %s", h)
	}
	if len(runs) > 1 {
		pr("// %d runs, checked together: the tests of run i are r<i>_*.", len(runs))
	}
	pr("module observed {")
	if ms.Instance != "" {
		pr("  import %s.* from %q", ms.Instance, rel)
	} else if len(ms.Constants) == 0 {
		pr("  import %s.* from %q", ms.Module, rel)
	} else {
		pr("  import %s(%s).* from %q", ms.Module, strings.Join(ms.Constants, ", "), rel)
	}
	if strings.TrimSpace(ms.Defs) != "" {
		pr("%s", indent(strings.TrimRight(ms.Defs, "\n"), "  "))
	}
	out := make([]*Generated, len(runs))
	for ri, calls := range runs {
		g := &Generated{Module: "observed", LineStep: map[int]int{}, Calls: calls}
		out[ri] = g
		prefix := ""
		if len(runs) > 1 {
			prefix = fmt.Sprintf("r%d_", ri)
			pr("")
			pr("  // ---- run %d ----", ri)
		}
		pr("")
		// chain writes a run of init and then every call, rendered by step.
		// Long chains are split into named segments, and long chains of
		// segments into segments of segments: Quint's Rust evaluator rejects
		// a `.then` chain nested much past 50 steps, so no chain is longer
		// than segmentSteps.
		chain := func(name, first string, step func(c Call) string, comment bool) {
			type item struct {
				call int    // index of the call, or -1 for a segment
				ref  string // the segment's name
				last int    // the last call it covers
			}
			// emit writes an item on its own line (quint's error positions map
			// back to the observed step by line): `.then(...)`, or bare as
			// the first item of a segment. A segment that evaluates to false
			// is reported at the line that uses it: its last call was the
			// disabled one (an earlier one would be reported inside it).
			emit := func(it item, then bool) {
				text := it.ref
				if it.call >= 0 {
					if comment {
						pr("    // [%d] %s", it.call, describe(calls[it.call]))
					}
					text = step(calls[it.call])
				}
				g.LineStep[line] = it.last
				if then {
					pr("    .then(%s)", text)
				} else {
					pr("    %s", text)
				}
			}
			items := make([]item, len(calls))
			for i := range calls {
				items[i] = item{call: i, last: i}
			}
			for level := 0; len(items) > segmentSteps; level++ {
				var next []item
				for s := 0; s < len(items); s += segmentSteps {
					// no "Test" in the name: quint test --match would take it for a test
					base := strings.TrimSuffix(name, "Test")
					seg := fmt.Sprintf("%s_seg%d", base, s/segmentSteps)
					if level > 0 {
						seg = fmt.Sprintf("%s_l%d_%d", base, level, s/segmentSteps)
					}
					group := items[s:min(s+segmentSteps, len(items))]
					pr("  action %s =", seg)
					for j, it := range group {
						emit(it, j > 0)
					}
					next = append(next, item{call: -1, ref: seg, last: group[len(group)-1].last})
				}
				items = next
			}
			pr("  run %s =", name)
			pr("    %s", first)
			for _, it := range items {
				emit(it, true)
			}
		}
		fused := len(ms.Invariants) > 0 && ms.Tests != DetailTests
		separate := ms.Tests != FastTests || len(ms.Invariants) == 0
		if separate {
			g.Tests.Conforms = prefix + "conformsTest"
			pr("  // The observed steps, each checked against the observed state.")
			first := "init"
			if ms.InitExpect != "" {
				first = fmt.Sprintf("init.expect(%s)", ms.InitExpect)
			}
			chain(g.Tests.Conforms, first, func(c Call) string {
				if c.Expect != "" {
					return fmt.Sprintf("%s.expect(%s)", c.Quint, c.Expect)
				}
				return c.Quint
			}, true)
		}
		if fused {
			g.Tests.All = prefix + "allTest"
			all := strings.Join(ms.Invariants, " and ")
			pr("")
			pr("  // The steps once, with every check: each step's expect and every invariant.")
			allFirst := fmt.Sprintf("init.expect(%s)", all)
			if ms.InitExpect != "" {
				allFirst = fmt.Sprintf("init.expect(%s and %s)", ms.InitExpect, all)
			}
			chain(g.Tests.All, allFirst, func(c Call) string {
				if c.Expect != "" {
					return fmt.Sprintf("%s.expect((%s) and %s)", c.Quint, c.Expect, all)
				}
				return fmt.Sprintf("%s.expect(%s)", c.Quint, all)
			}, false)
		}
		g.Tests.Invariants = map[string]string{}
		if separate {
			for _, inv := range ms.Invariants {
				name := prefix + inv + "InvTest"
				g.Tests.Invariants[inv] = name
				pr("")
				pr("  // The same steps; %s must hold in every state.", inv)
				chain(name, fmt.Sprintf("init.expect(%s)", inv), func(c Call) string {
					return fmt.Sprintf("%s.expect(%s)", c.Quint, inv)
				}, false)
			}
		}
	}
	pr("}")
	for _, g := range out {
		g.Text = w.String()
	}
	return out, nil
}

// ModuleSpec returns what a replay of plan p imports and checks.
func (b *Binding) ModuleSpec(p *Plan) ModuleSpec {
	return ModuleSpec{Spec: b.SpecPath(), Module: b.Module, Constants: p.ConstantList(), Defs: b.Defs, Invariants: b.Invariants,
		Header: []string{fmt.Sprintf("actor %q, %d observed steps, processes %v", p.Actor, p.ObservedSteps, p.Processes)}}
}

// Skips reports whether the binding drops the step: its outcome maps to
// "skip" for its action, so it becomes no model call.
func (b *Binding) Skips(s *qtrace.Step) bool {
	o, err := b.outcome(b.Actions[s.Action], s)
	return err == nil && o == "skip"
}

func (b *Binding) outcome(a Action, s *qtrace.Step) (string, error) {
	o := s.Outcome
	if o == "" {
		o = "ok"
	}
	if v, ok := a.Outcomes[o]; ok {
		if v == "call" {
			return "", nil // replay as if it succeeded
		}
		return v, nil
	}
	if o != "ok" && !a.usesOutcome() {
		// A failed step of an action whose call ignores the outcome would be
		// replayed as a success. Make the binding say what it means.
		return "", fmt.Errorf("%s failed (outcome %q: %s) but its templates do not use .outcome; map it in the action's outcomes (e.g. %s: skip)", s.Action, o, s.Error, o)
	}
	if v, ok := b.Outcomes[o]; ok {
		return v, nil
	}
	if o == "ok" {
		return "", nil // only an error if a template uses .outcome
	}
	return "", fmt.Errorf("%s: outcome %q has no mapping in the binding (error: %s)", s.Action, o, s.Error)
}

func describe(c Call) string {
	if c.Step == nil {
		if c.Inferred == "" {
			return c.Note
		}
		return "inferred " + c.Inferred
	}
	s := c.Step
	return fmt.Sprintf("seq %d %s %s", s.Seq, short(s.Process), s.String())
}

func short(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

func indent(s, pfx string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pfx + l
		}
	}
	return strings.Join(lines, "\n")
}

func toI(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

// jsonQuint renders a collection recorded as JSON text (qobs records slices
// as JSON) as a Quint Set(...) or List(...); nested arrays become tuples.
func jsonQuint(v any, kind string) (string, error) {
	text := fmt.Sprint(v)
	if q, ok := v.(qlit); ok {
		u, err := strconv.Unquote(string(q))
		if err != nil {
			return "", fmt.Errorf("to%s: %s is not a recorded JSON array", kind, q)
		}
		text = u
	}
	if kind == "Map" {
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return "", fmt.Errorf("toMap: %q is not a JSON object: %v", text, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = strconv.Quote(k) + " -> " + jsonElem(m[k])
		}
		return "Map(" + strings.Join(parts, ", ") + ")", nil
	}
	var xs []any
	if err := json.Unmarshal([]byte(text), &xs); err != nil {
		return "", fmt.Errorf("to%s: %q is not a JSON array: %v", kind, text, err)
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = jsonElem(x)
	}
	if kind == "List" {
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "Set(" + strings.Join(parts, ", ") + ")", nil
}

// jsonElem renders a decoded JSON value as Quint: arrays become tuples.
func jsonElem(x any) string {
	switch y := x.(type) {
	case []any:
		parts := make([]string, len(y))
		for i, e := range y {
			parts[i] = jsonElem(e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case float64:
		return strconv.FormatInt(int64(y), 10)
	case string:
		return strconv.Quote(y)
	case bool:
		return strconv.FormatBool(y)
	}
	return strconv.Quote(fmt.Sprint(x))
}

// sig is an action's signature, if the model is known.
func sig(m *Model, action string) string {
	if m == nil {
		return action
	}
	return m.Signature(action)
}
