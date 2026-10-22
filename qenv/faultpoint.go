package qenv

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/casselc/quint-connect-go/qenv/internal/choice"
)

// A fault point is a place in the code under test where a fault may
// happen: a call that can fail with one of its own errors, or the process
// crash there. Mark it with //quint:fault (the aspects of qenv/fault weave
// a call of FaultPoint at the function's entry), or call FaultPoint. In a
// simulation each point is a fault choice "fault.<kind>", so Hegel shrinks
// it away (option 0 is nominal) and runs can report which points they
// reached (Coverage); outside one it does nothing.

// InjectedError is the error of a fault point that names none.
type InjectedError struct{ Kind string }

func (e *InjectedError) Error() string { return "qenv: injected fault " + e.Kind }

// FaultName names the option of an error a fault point can return, in
// coverage and scripts (Scripted.Name).
func (e *InjectedError) FaultName() string { return "injected" }

// faultName is an error's option name: its FaultName, else its text.
func faultName(err error) string {
	if n, ok := err.(interface{ FaultName() string }); ok {
		return n.FaultName()
	}
	return err.Error()
}

// FaultPoint is a fault choice at a point of kind: nil (nominal), one of
// errs, or, with crash, the crash of the calling task's process here, which
// is then restarted (as the nemesis does); outside a process the crash
// option is nominal, so the option numbers are the same at every call.
// With no errs and no crash its fault is an InjectedError. The point
// declares its kind (Faults): its options are named after the errors
// (FaultName, else the error's text) and "crash", so coverage and scripts
// name them. Outside a simulation, nil.
func FaultPoint(ctx context.Context, kind string, crash bool, errs ...error) error {
	t := taskOf(ctx)
	if t == nil {
		return nil
	}
	if len(errs) == 0 && !crash {
		errs = []error{&InjectedError{kind}}
	}
	names := make([]string, 0, len(errs)+1)
	for _, err := range errs {
		names = append(names, faultName(err))
	}
	if crash {
		names = append(names, "crash")
	}
	k := Faults(kind, names...)
	v := t.sim.fault(k.Name, 1+len(k.Options))
	switch {
	case v == 0:
		return nil
	case v <= len(errs):
		return errs[v-1]
	case t.proc == nil:
		return nil // the crash option, with no process to crash
	}
	p := t.proc
	t.sim.spawn(t.sim.base, "restart."+p.Name, nil, func(context.Context) { p.Restart() })
	p.Crash() // the calling task stops here
	return nil
}

// DeclareFault registers a fault point of kind at site (a function, a
// position): what woven code does at init, so a coverage report can name
// the points no run reached.
func DeclareFault(kind, site string) { choice.DeclareSite(kind, site) }

// PointCoverage is what runs did at one fault kind.
type PointCoverage struct {
	Kind    string
	Sites   []string // where it is declared (none: a double's own fault)
	Runs    int      // runs that reached it
	Reached int      // times it was reached, over all runs
	// Injected counts each option taken (index 0: nominal).
	Injected []int
	// Names are the options' names, for a declared kind (Faults).
	Names []string
}

// Faulted is how often a fault (any option but nominal) was taken.
func (p *PointCoverage) Faulted() int {
	n := 0
	for i, c := range p.Injected {
		if i > 0 {
			n += c
		}
	}
	return n
}

// A Coverage is the fault coverage of a set of runs: for each fault kind,
// declared (DeclareFault) or met (a double's), how often runs reached it
// and which of its options they took.
type Coverage struct {
	Runs   int
	Points []*PointCoverage // by kind
	// faulted are each run's faulted kinds, in the order of their first
	// fault (Pairs)
	faulted [][]string
}

// CoverageOf computes the coverage of runs from their choices (a
// Recorder's, or qtest.Run.Choices) against the declared points.
func CoverageOf(runs [][]Choice) *Coverage {
	byKind := map[string]*PointCoverage{}
	get := func(kind string) *PointCoverage {
		p, ok := byKind[kind]
		if !ok {
			p = &PointCoverage{Kind: kind}
			byKind[kind] = p
		}
		return p
	}
	for kind, sites := range choice.Sites() {
		get(kind).Sites = sites
	}
	cov := &Coverage{Runs: len(runs)}
	for _, run := range runs {
		seen := map[string]bool{}
		var order []string
		for _, c := range run {
			if !IsFault(c.Kind) {
				continue
			}
			p := get(strings.TrimPrefix(c.Kind, "fault."))
			p.Reached++
			for len(p.Injected) <= c.Value {
				p.Injected = append(p.Injected, 0)
			}
			p.Injected[c.Value]++
			if !seen[p.Kind] {
				seen[p.Kind] = true
				p.Runs++
			}
			if c.Value != 0 && !slices.Contains(order, p.Kind) {
				order = append(order, p.Kind)
			}
		}
		cov.faulted = append(cov.faulted, order)
	}
	for _, p := range byKind {
		p.Names = choice.Options(p.Kind)
		cov.Points = append(cov.Points, p)
	}
	sort.Slice(cov.Points, func(i, j int) bool { return cov.Points[i].Kind < cov.Points[j].Kind })
	return cov
}

// Unreached are the declared points no run reached: code the runs never
// took to its failure, whatever their line coverage.
func (c *Coverage) Unreached() []*PointCoverage {
	var out []*PointCoverage
	for _, p := range c.Points {
		if len(p.Sites) > 0 && p.Reached == 0 {
			out = append(out, p)
		}
	}
	return out
}

// Unfaulted are the points reached but never faulted.
func (c *Coverage) Unfaulted() []*PointCoverage {
	var out []*PointCoverage
	for _, p := range c.Points {
		if p.Reached > 0 && p.Faulted() == 0 {
			out = append(out, p)
		}
	}
	return out
}

// A PairCoverage is how runs combined two fault kinds: the runs that
// faulted both, and those whose first fault of A came before B's and the
// reverse. Bugs of fault handling are often bugs of two faults together
// (a crash during the retry after a lost response).
type PairCoverage struct {
	A, B                     string
	Together, AFirst, BFirst int
}

// Pairs is the coverage of each pair of kinds that runs faulted (A < B).
func (c *Coverage) Pairs() []PairCoverage {
	var kinds []string
	for _, p := range c.Points {
		if p.Faulted() > 0 {
			kinds = append(kinds, p.Kind)
		}
	}
	at := map[[2]string]*PairCoverage{}
	var out []*PairCoverage
	for i, a := range kinds {
		for _, b := range kinds[i+1:] {
			pc := &PairCoverage{A: a, B: b}
			at[[2]string{a, b}] = pc
			out = append(out, pc)
		}
	}
	for _, order := range c.faulted {
		for i, x := range order {
			for _, y := range order[i+1:] {
				if pc, ok := at[[2]string{x, y}]; ok {
					pc.Together++
					pc.AFirst++
				} else if pc, ok := at[[2]string{y, x}]; ok {
					pc.Together++
					pc.BFirst++
				}
			}
		}
	}
	res := make([]PairCoverage, len(out))
	for i, pc := range out {
		res[i] = *pc
	}
	return res
}

// Untried are the pairs of faulted kinds no run faulted together.
func (c *Coverage) Untried() []PairCoverage {
	var out []PairCoverage
	for _, p := range c.Pairs() {
		if p.Together == 0 {
			out = append(out, p)
		}
	}
	return out
}

// OneWay are the pairs faulted together, but only ever in one order.
func (c *Coverage) OneWay() []PairCoverage {
	var out []PairCoverage
	for _, p := range c.Pairs() {
		if p.Together > 0 && (p.AFirst == 0 || p.BFirst == 0) {
			out = append(out, p)
		}
	}
	return out
}

// Point returns the coverage of kind, or nil.
func (c *Coverage) Point(kind string) *PointCoverage {
	for _, p := range c.Points {
		if p.Kind == kind {
			return p
		}
	}
	return nil
}

func (c *Coverage) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fault coverage over %d runs:\n", c.Runs)
	for _, p := range c.Points {
		state := "faulted"
		switch {
		case p.Reached == 0:
			state = "UNREACHED"
		case p.Faulted() == 0:
			state = "never faulted"
		}
		opts := fmt.Sprint(p.Injected)
		if len(p.Names) > 0 {
			var named []string
			for i, n := range p.Injected {
				name := "nominal"
				if i > 0 && i <= len(p.Names) {
					name = p.Names[i-1]
				}
				named = append(named, fmt.Sprintf("%s %d", name, n))
			}
			opts = "[" + strings.Join(named, ", ") + "]"
		}
		fmt.Fprintf(&b, "  %-32s %-13s runs %d/%d, reached %d, options %s", p.Kind, state, p.Runs, c.Runs, p.Reached, opts)
		if len(p.Sites) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(p.Sites, ", "))
		}
		b.WriteByte('\n')
	}
	if pairs := c.Pairs(); len(pairs) > 0 {
		fmt.Fprintf(&b, "  pairs of faulted kinds: %d, %d never together, %d in one order only\n", len(pairs), len(c.Untried()), len(c.OneWay()))
		for _, p := range c.Untried() {
			fmt.Fprintf(&b, "    never together: %s + %s\n", p.A, p.B)
		}
	}
	return b.String()
}
