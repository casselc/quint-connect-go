package qsweep_test

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qsweep"
)

func threes(n int) []qsweep.Factor {
	var fs []qsweep.Factor
	for i := range n {
		fs = append(fs, qsweep.Factor{Name: string(rune('a' + i)), Values: []string{"0", "1", "2"}})
	}
	return fs
}

// Pairwise rows of four 3-valued factors cover every pair in close to the
// optimum 9 rows of the 81; three-way rows cover every triple.
func TestCover(t *testing.T) {
	fs := threes(4)
	for _, c := range []struct{ t, most int }{{2, 11}, {3, 40}} {
		rows, err := qsweep.Cover(fs, c.t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > c.most {
			t.Errorf("t=%d: %d rows, want at most %d", c.t, len(rows), c.most)
		}
		if miss, _ := qsweep.Missing(fs, c.t, nil, rows); len(miss) > 0 {
			t.Errorf("t=%d: missing %v", c.t, miss)
		}
		t.Logf("t=%d: %d rows of 81", c.t, len(rows))
	}
	// deterministic
	a, _ := qsweep.Cover(fs, 2, nil)
	b, _ := qsweep.Cover(fs, 2, nil)
	for i := range a {
		if a[i].String() != b[i].String() {
			t.Fatal("two generations differ")
		}
	}
}

// A constraint rules rows out, and the pairs only those rows had are not
// required; every row returned is allowed.
func TestCoverConstraint(t *testing.T) {
	fs := []qsweep.Factor{qsweep.Ints("retries", 0, 1, 3), qsweep.Bool("budget"), qsweep.Factor{Name: "iso", Values: []string{"ser", "si", "rc"}}}
	allow := func(r qsweep.Row) bool { return r.Int("retries") > 0 || !r.On("budget") }
	rows, err := qsweep.Cover(fs, 2, allow)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !allow(r) {
			t.Errorf("row %s is not allowed", r)
		}
	}
	if miss, _ := qsweep.Missing(fs, 2, allow, rows); len(miss) > 0 {
		t.Errorf("missing %v", miss)
	}
	// rows chosen by hand: Missing names what they leave out
	miss, _ := qsweep.Missing(fs, 2, allow, rows[:1])
	if len(miss) == 0 || strings.Contains(strings.Join(miss, " "), "budget=on,retries=0") {
		t.Errorf("missing from one row: %v", miss)
	}
	if _, err := qsweep.Cover(fs, 2, func(qsweep.Row) bool { return false }); err == nil {
		t.Error("no allowed row, no error")
	}
}

// Every ordered pair, and every ordered triple, of events appears as a
// subsequence of some order.
func TestSequences(t *testing.T) {
	ev := []string{"crash", "lose", "dup", "delay", "partition"}
	for _, k := range []int{2, 3} {
		seqs := qsweep.Sequences(ev, k)
		pos := func(seq []string, e string) int {
			for i, x := range seq {
				if x == e {
					return i
				}
			}
			return -1
		}
		var check func(prefix []string)
		check = func(prefix []string) {
			if len(prefix) == k {
				for _, s := range seqs {
					ok := true
					for i := 1; i < k; i++ {
						ok = ok && pos(s, prefix[i-1]) < pos(s, prefix[i])
					}
					if ok {
						return
					}
				}
				t.Errorf("t=%d: no order has %v", k, prefix)
				return
			}
			for _, e := range ev {
				if pos(prefix, e) < 0 {
					check(append(append([]string(nil), prefix...), e))
				}
			}
		}
		check(nil)
		t.Logf("t=%d: %d orders of %d events", k, len(seqs), len(ev))
	}
}
