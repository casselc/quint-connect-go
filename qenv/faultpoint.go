package qenv

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// FaultPoint is a fault choice at a point of kind: nil (nominal), one of
// errs, or, with crash and inside a process, the crash of the calling
// task's process here, which is then restarted (as the nemesis does). With
// no errs and no crash its fault is an InjectedError. Outside a
// simulation, nil.
func FaultPoint(ctx context.Context, kind string, crash bool, errs ...error) error {
	t := taskOf(ctx)
	if t == nil {
		return nil
	}
	if len(errs) == 0 && !crash {
		errs = []error{&InjectedError{kind}}
	}
	n := 1 + len(errs)
	crash = crash && t.proc != nil
	if crash {
		n++
	}
	v := t.sim.Fault(kind, n)
	switch {
	case v == 0:
		return nil
	case v <= len(errs):
		return errs[v-1]
	}
	p := t.proc
	t.sim.spawn(t.sim.base, "restart."+p.Name, nil, func(context.Context) { p.Restart() })
	p.Crash() // the calling task stops here
	return nil
}

var (
	declMu   sync.Mutex
	declared = map[string][]string{}
)

// DeclareFault registers a fault point of kind at site (a function, a
// position): what woven code does at init, so a coverage report can name
// the points no run reached.
func DeclareFault(kind, site string) {
	declMu.Lock()
	defer declMu.Unlock()
	for _, s := range declared[kind] {
		if s == site {
			return
		}
	}
	declared[kind] = append(declared[kind], site)
}

// DeclaredFaults are the registered fault points: kind -> sites.
func DeclaredFaults() map[string][]string {
	declMu.Lock()
	defer declMu.Unlock()
	out := make(map[string][]string, len(declared))
	for k, v := range declared {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// PointCoverage is what runs did at one fault kind.
type PointCoverage struct {
	Kind    string
	Sites   []string // where it is declared (none: a double's own fault)
	Runs    int      // runs that reached it
	Reached int      // times it was reached, over all runs
	// Injected counts each option taken (index 0: nominal).
	Injected []int
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
// declared (DeclaredFaults) or met (a double's), how often runs reached it
// and which of its options they took.
type Coverage struct {
	Runs   int
	Points []*PointCoverage // by kind
}

// CoverageOf computes the coverage of runs from their choices (a
// Recorder's, or qtest.Explored.Choices) against the declared points.
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
	for kind, sites := range DeclaredFaults() {
		get(kind).Sites = sites
	}
	for _, run := range runs {
		seen := map[string]bool{}
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
		}
	}
	cov := &Coverage{Runs: len(runs)}
	for _, p := range byKind {
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
		fmt.Fprintf(&b, "  %-32s %-13s runs %d/%d, reached %d, options %v", p.Kind, state, p.Runs, c.Runs, p.Reached, p.Injected)
		if len(p.Sites) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(p.Sites, ", "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
