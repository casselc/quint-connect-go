package qtest

import (
	"fmt"
	"strings"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/validate"
)

// runsClaim is a claim about the runs themselves (their choices), not
// their verdicts: judgeRuns says why it fails ("": it holds).
type runsClaim interface {
	Claim
	judgeRuns(runs []*Run) string
}

// A Reach is a claim that runs reached a fault kind: made a choice of it,
// faulted or not, in at least a share of the runs (Reaches). It is what an
// exploration must show before its verdicts mean anything about the kind:
// a bug behind a fault no run reached is a bug no run could find.
type Reach struct {
	kind    string
	share   float64
	faulted bool
}

// Reaches claims that some run reached the fault kind ("rpc.payments.request",
// as reports print it); InAtLeast and Faulted make it stronger.
func Reaches(kind string) Reach {
	return Reach{kind: strings.TrimPrefix(kind, "fault."), share: 0}
}

// InAtLeast wants the kind reached in at least share (0 to 1) of the runs.
func (r Reach) InAtLeast(share float64) Reach { r.share = share; return r }

// Faulted wants the kind's fault taken (an option other than nominal),
// not only its choice made.
func (r Reach) Faulted() Reach { r.faulted = true; return r }

func (r Reach) String() string {
	what := "reaching " + r.kind
	if r.faulted {
		what = "faulting " + r.kind
	}
	if r.share > 0 {
		return fmt.Sprintf("%s in at least %.0f%% of the runs", what, 100*r.share)
	}
	return "some run " + what
}

// judge is never called: an exploration judges a Reach over its runs.
func (r Reach) judge([]*validate.Report) judgment { return judgment{show: -1} }

func (r Reach) judgeRuns(runs []*Run) string {
	n := 0
	for _, run := range runs {
		for _, c := range run.Choices {
			if strings.TrimPrefix(c.Kind, "fault.") == r.kind && (!r.faulted || c.Value != 0) {
				n++
				break
			}
		}
	}
	need := 1
	if r.share > 0 {
		need = int(r.share*float64(len(runs)) + 0.999999)
	}
	if n >= need {
		return ""
	}
	var choices [][]qenv.Choice
	for _, run := range runs {
		choices = append(choices, run.Choices)
	}
	var kinds []string
	for _, p := range qenv.CoverageOf(choices).Points {
		if p.Runs > 0 {
			kinds = append(kinds, p.Kind)
		}
	}
	return fmt.Sprintf("want %s; %d of %d runs did (the kinds the runs reached: %s). Raise its rate (Rates.Set), run more seeds, "+
		"or check the kind's name", r, n, len(runs), strings.Join(kinds, ", "))
}
