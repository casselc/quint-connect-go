package qtest

import (
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
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
	// Opts adds a row's options to Explore's, e.g. the model's constants
	// for the row's sizes (Constants).
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

// SweepExplore explores each covering row of sw as a subtest named by the
// row, checking its expectation there.
func SweepExplore(t *testing.T, bindingPath string, sw Sweep, scenario func(row qsweep.Row, c qenv.Chooser)) *Swept {
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
			if sw.Opts != nil {
				xc.Opts = append(append([]Option(nil), xc.Opts...), sw.Opts(row)...)
			}
			x := Explore(t, bindingPath, xc, func(c qenv.Chooser) { scenario(row, c) })
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
