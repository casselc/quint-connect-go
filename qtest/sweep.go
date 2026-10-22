package qtest

import (
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qrun"
	"github.com/casselc/quint-connect-go/qsweep"
)

// A Sweep explores a parameter space by covering rows (qsweep.Cover):
// every combination of Strength factors' values (default 2, pairwise) is
// in some row, each row explored with its own seeds. Make fault kinds
// factors (qsweep.Bool, then qenv.Without for the kinds a row turns off)
// and the sweep is swarm testing with a guarantee: every pair of kinds
// runs together, and alone, with no other kind masking it.
type Sweep struct {
	Factors  []qsweep.Factor
	Strength int
	// Allow rules rows out (nil: all allowed); combinations only ruled-out
	// rows have are not required.
	Allow func(qsweep.Row) bool
	// Explore is each row's exploration (Seeds per row).
	Explore ExploreConfig
	// Opts adds a row's options to Explore's. A factor that names its
	// model constant (qsweep.Factor.Constant) needs none: each row is
	// checked with the constants of its factors (Row.Constants).
	Opts func(qsweep.Row) []Option
	// Expect is what each row's runs should show (nil: Conforms): a
	// mutant's bug is real under some rows only.
	Expect func(qsweep.Row) []Claim
}

// Swept is a sweep's rows and their explorations.
type Swept struct {
	Rows []qsweep.Row
	Runs []*Exploration
}

// Coverage is the fault coverage over every row's runs.
func (s *Swept) Coverage() *qenv.Coverage {
	var runs [][]qenv.Choice
	for _, x := range s.Runs {
		for _, r := range x.Runs {
			runs = append(runs, r.Choices)
		}
	}
	return qenv.CoverageOf(runs)
}

var rowKey = qrun.NewKey[qsweep.Row]("sweep.row")

// RowOf is the sweep row a run's chooser was made for (System.Sweep with
// no run function): a System's Run configures itself from it.
func RowOf(c qenv.Chooser) (qsweep.Row, bool) { return qrun.In(qenv.ContextOf(c), rowKey) }

// SweepExplore explores each covering row of sw as a subtest named by the
// row, checking its expectation there.
//
// Deprecated: use System.Sweep.
func SweepExplore(t *testing.T, bindingPath string, sw Sweep, scenario func(row qsweep.Row, c qenv.Chooser)) *Swept {
	t.Helper()
	return sweep(t, bindingPath, sw, func(row qsweep.Row, c qenv.Chooser) error { scenario(row, c); return nil })
}

func sweep(t *testing.T, bindingPath string, sw Sweep, scenario func(row qsweep.Row, c qenv.Chooser) error) *Swept {
	t.Helper()
	strength := sw.Strength
	if strength == 0 {
		strength = 2
	}
	rows, err := qsweep.Cover(sw.Factors, strength, sw.Allow)
	if err != nil {
		t.Fatal(err)
	}
	s := &Swept{Rows: rows, Runs: make([]*Exploration, len(rows))}
	t.Logf("qtest: %d rows cover every %d-way combination of %d factors", len(rows), strength, len(sw.Factors))
	for i, row := range rows {
		t.Run(row.String(), func(t *testing.T) {
			xc := sw.Explore
			if cs := row.Constants(); len(cs) > 0 {
				xc.Opts = append(append([]Option(nil), xc.Opts...), Constants(cs))
			}
			if sw.Opts != nil {
				xc.Opts = append(append([]Option(nil), xc.Opts...), sw.Opts(row)...)
			}
			x := explore(t, bindingPath, xc, func(c qenv.Chooser) error {
				return scenario(row, qenv.WithContext(c, qrun.With(qenv.ContextOf(c), rowKey, row)))
			})
			s.Runs[i] = x
			want := []Claim{Conforms()}
			if sw.Expect != nil {
				want = sw.Expect(row)
			}
			x.Expect(t, want...)
		})
	}
	return s
}
