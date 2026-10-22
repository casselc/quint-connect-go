package qtest

import (
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qsweep"
)

// A System's own Run serves a sweep: with no run function, the row is in
// the chooser (RowOf), and the Run deposits the row's amount. Past LIMIT
// (100) the row violates underLimit, below it the row conforms.
func TestSweepWithTheSystemsRun(t *testing.T) {
	RequireQuint(t)
	sys := System{Binding: "../validate/testdata/account.yaml", Run: func(c qenv.Chooser) error {
		row, ok := RowOf(c)
		if !ok {
			t.Error("no row in the chooser")
		}
		n := row.Int("amount")
		qobs.Record(qenv.ContextOf(c), "deposit", nil, "amount", n, "obs.balance", n)
		return nil
	}}
	sys.Sweep(t, Sweep{Factors: []qsweep.Factor{qsweep.Ints("amount", 50, 150)}, Explore: ExploreConfig{Seeds: 1},
		Expect: func(row qsweep.Row) []Claim {
			if row.Int("amount") > 100 {
				return []Claim{Violates("underLimit")}
			}
			return []Claim{Conforms()}
		}}, nil)
}
