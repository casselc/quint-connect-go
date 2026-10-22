package twophase_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/twophase"
	"github.com/casselc/quint-connect-go/examples/twophase/tpc"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

// system is the protocol as cfg deploys it, bound to the Choreo model.
func system(cfg twophase.Config) qtest.System {
	return qtest.System{Binding: binding, Opts: []qtest.Option{qtest.Constants(cfg.Constants())}, Run: func(c qenv.Chooser) error {
		_, err := twophase.Run(c, cfg)
		return err
	}}
}

// seeds is n, or a third of it under -short.
func seeds(n int) int {
	if testing.Short() {
		return n / 3
	}
	return n
}

// crashy crashes a process at one scheduling step in twenty, and loses or
// duplicates one message in five.
var crashy = qenv.Rates{Default: 0.2, Kinds: map[string]float64{"fault.nemesis": 0.05}}

// The protocol is correct through crashes, lost and duplicated votes and
// decisions, and lost responses: every run is a behaviour of the Choreo
// model, its invariants hold, and runs both commit and abort.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	x := system(twophase.Config{}).Explore(t, qtest.ExploreConfig{Seeds: seeds(60), Rates: crashy})
	x.Expect(t, qtest.Conforms())
	t.Log(x.Evidence())
}

// Which check catches which bug. Committing on the first vote shows in a
// run where it reaches the coordinator before another participant has
// prepared (most, not all: the participants prepare as they boot). The others need faults: a participant
// gives up only if its decision is late, and a coordinator decides twice
// only if it crashes between deciding and telling everyone; without
// faults both conform.
func TestMutants(t *testing.T) {
	qtest.RequireQuint(t)
	none := qenv.Rates{Default: 1e-9}
	cases := []struct {
		name  string
		m     tpc.Mutation
		rates qenv.Rates
		want  []qtest.Claim
	}{
		{"early-commit", tpc.EarlyCommit, none, []qtest.Claim{qtest.Rejects("decideCommit")}},
		{"impatient/no-faults", tpc.ImpatientParticipant, none, []qtest.Claim{qtest.Conforms()}},
		{"impatient", tpc.ImpatientParticipant, crashy, []qtest.Claim{qtest.Rejects("abort")}},
		{"volatile/no-faults", tpc.VolatileDecision, none, []qtest.Claim{qtest.Conforms()}},
		{"volatile", tpc.VolatileDecision, crashy, []qtest.Claim{qtest.Rejects("decideAbort")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if testing.Short() && c.rates.Default > 1e-6 {
				t.Skip("a bug that needs faults takes tens of seeds")
			}
			system(twophase.Config{Mutation: c.m}).Explore(t, qtest.ExploreConfig{Seeds: 60, Rates: c.rates}).Expect(t, c.want...)
		})
	}
}

// Hegel shrinks the impatient participant's failure to the faults it
// needs: the decision late enough that a prepared participant gives up.
func TestHegelShrinksImpatience(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinks against the model")
	}
	v := quinthegel.Shrink(t, system(twophase.Config{Mutation: tpc.ImpatientParticipant}), crashy, hegel.WithTestCases(100))
	v.Expect(t, qtest.Rejects("abort"))
	t.Logf("shrunk to %d steps, faults %v", len(v.Steps), (&qenv.ChoiceLog{Choices: v.Choices}).Faults())
}
