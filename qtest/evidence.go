package qtest

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/validate"
)

// Evidence is what an exploration, a sweep or a mutation run shows, in one
// report: the verdicts, the faults reached, the configurations covered, the
// code run and the mutants killed. Each part is what a passing test leaves
// unsaid: that runs conformed says nothing of the faults never tried, the
// code never run or the code the model does not constrain.
type Evidence struct {
	Runs    int `json:"runs"`
	Conform int `json:"conform"`
	// Failing counts the failing runs by reason: "violates I", "rejects a
	// (step the model cannot take)", "mismatch after a", "issue".
	Failing map[string]int  `json:"failing"`
	Faults  *qenv.Coverage  `json:"faults,omitempty"`
	Rows    int             `json:"rows,omitempty"`    // a sweep's rows (0: not a sweep)
	Code    *qcover.Summary `json:"code,omitempty"`    // when the code is woven with qweave -cover (nil: not)
	Mutants *MutationReport `json:"mutants,omitempty"` // from System.Mutate, if set
}

// WriteJSON writes the evidence to path as JSON, for tools that read a
// run's report (quintgo cover, actionmap). Explorations write theirs under
// QUINTGO_KEEP (evidence.json in the test's directory).
func (e *Evidence) WriteJSON(path string) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Evidence is the exploration's report.
func (x *Exploration) Evidence() *Evidence {
	e := evidenceOf(x.Runs)
	e.Faults = x.Coverage()
	if x.hasCode() {
		e.Code = x.CodeCoverage()
	}
	return e
}

// Evidence is the sweep's report over every row's runs.
func (s *Swept) Evidence() *Evidence {
	all := &Exploration{}
	for _, x := range s.Runs {
		if x != nil {
			all.Runs = append(all.Runs, x.Runs...)
		}
	}
	e := all.Evidence()
	e.Rows = len(s.Rows)
	return e
}

func (x *Exploration) hasCode() bool {
	for _, r := range x.Runs {
		if r.Code != nil {
			return true
		}
	}
	return false
}

func evidenceOf(runs []*Run) *Evidence {
	e := &Evidence{Runs: len(runs), Failing: map[string]int{}}
	for _, r := range runs {
		if r.Report.OK() {
			e.Conform++
			continue
		}
		e.Failing[reason(r.Report)]++
	}
	return e
}

// reason names why a run failed, by its first failing actor.
func reason(rep *validate.Report) string {
	for _, a := range rep.Actors {
		if a.OK() {
			continue
		}
		if a.Conforms != validate.Pass {
			action := "a step"
			if a.Failure != nil && a.Failure.Step != nil {
				action = a.Failure.Step.Action
			}
			if a.Failure != nil && a.Failure.Kind == "state" {
				return "mismatch after " + action
			}
			return "rejects " + action
		}
		for _, inv := range a.Invariants {
			if inv.Verdict != validate.Pass {
				return "violates " + inv.Name
			}
		}
		return "issue"
	}
	return "issue"
}

func (e *Evidence) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "evidence: %d runs", e.Runs)
	if e.Rows > 0 {
		fmt.Fprintf(&b, ", %d rows", e.Rows)
	}
	fmt.Fprintf(&b, "\n  verdicts  %d conform", e.Conform)
	reasons := make([]string, 0, len(e.Failing))
	for r := range e.Failing {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Fprintf(&b, " · %d %s", e.Failing[r], r)
	}
	b.WriteByte('\n')
	if c := e.Faults; c != nil {
		faulted := 0
		for _, p := range c.Points {
			if p.Faulted() > 0 {
				faulted++
			}
		}
		pairs := c.Pairs()
		fmt.Fprintf(&b, "  faults    %d of %d kinds faulted", faulted, len(c.Points))
		if len(pairs) > 0 {
			fmt.Fprintf(&b, ", pairs %d/%d together, %d one way", len(pairs)-len(c.Untried()), len(pairs), len(c.OneWay()))
		}
		if u := c.Unreached(); len(u) > 0 {
			fmt.Fprintf(&b, "; unreached: %s", kinds(u))
		}
		if u := c.Unfaulted(); len(u) > 0 {
			fmt.Fprintf(&b, "; never faulted: %s", kinds(u))
		}
		b.WriteByte('\n')
		if u := c.Untried(); len(u) > 0 {
			fmt.Fprintf(&b, "            never together (each faulted, never in one run): %s\n", pairList(u, false))
		}
		if o := c.OneWay(); len(o) > 0 {
			fmt.Fprintf(&b, "            one way (together, but always in this order): %s\n", pairList(o, true))
		}
	}
	if s := e.Code; s != nil {
		fmt.Fprintf(&b, "  code      %.1f%% of statements, %d blocks never run, %d outcome pairs ran the same code\n",
			100*s.Ratio(), len(s.Uncovered()), len(s.Indistinct()))
	}
	if m := e.Mutants; m != nil {
		fmt.Fprintf(&b, "  mutants   %d reached, %d killed (%.0f%%), %d survive, %d unreached\n",
			len(m.Killed)+len(m.Survived), len(m.Killed), 100*m.Score(), len(m.Survived), len(m.Unreached))
	}
	return b.String()
}

// pairList names pairs ("a+b"), up to eight; ordered, as the order they
// ran in ("a, then b").
func pairList(ps []qenv.PairCoverage, ordered bool) string {
	var out []string
	for i, p := range ps {
		if i == 8 {
			out = append(out, fmt.Sprintf("and %d more", len(ps)-8))
			break
		}
		switch {
		case !ordered:
			out = append(out, p.A+"+"+p.B)
		case p.BFirst == 0:
			out = append(out, p.A+", then "+p.B)
		default:
			out = append(out, p.B+", then "+p.A)
		}
	}
	return strings.Join(out, "; ")
}

func kinds(ps []*qenv.PointCoverage) string {
	var ks []string
	for _, p := range ps {
		ks = append(ks, p.Kind)
	}
	return strings.Join(ks, ", ")
}
