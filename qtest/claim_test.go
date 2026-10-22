package qtest

import (
	"testing"

	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
)

// Claims over runs: a bare expectation is about some run (Conforms about
// every run); several are judged one by one; Some(a, b) wants one run
// with both; None wants no run with either.
func TestClaims(t *testing.T) {
	// three runs: one shows a, one shows a and b, one shows neither
	a := Expectation{desc: "a", check: func(r *validate.Report) bool { return len(r.Issues) == 1 || len(r.Issues) == 3 }}
	b := Expectation{desc: "b", check: func(r *validate.Report) bool { return len(r.Issues) == 3 }}
	c := Expectation{desc: "c", check: func(r *validate.Report) bool { return false }}
	runs := []*validate.Report{{Issues: make([]qtrace.Issue, 1)}, {Issues: make([]qtrace.Issue, 3)}, {Issues: make([]qtrace.Issue, 5)}}
	for _, k := range []struct {
		claim Claim
		ok    bool
		show  int
	}{
		{a, true, 0},
		{c, false, -1},
		{Some(a, b), true, 1},
		{Some(b, c), false, -1},
		{All(a), false, 2},
		{None(c), true, -1},
		{None(b), false, 1},
		{None(c, a), false, 0},
	} {
		j := k.claim.judge(runs)
		if j.ok != k.ok || j.show != k.show {
			t.Errorf("%s: ok %v show %d, want %v %d", k.claim, j.ok, j.show, k.ok, k.show)
		}
	}
	every := Expectation{desc: "every", check: func(r *validate.Report) bool { return len(r.Issues) > 0 }, every: true}
	if !every.judge(runs).ok {
		t.Error("an every-run expectation alone is not judged over every run")
	}
}
