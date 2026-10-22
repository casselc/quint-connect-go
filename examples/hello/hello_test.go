package hello

import (
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

// system is the counter and its client, with mutation m, as one System:
// explored by seeds here, and by Hegel, sweeps or the fuzzer the same way.
func system(m Mutation) qtest.System {
	return qtest.System{Binding: "hello.binding.yaml", Run: func(c qenv.Chooser) error { return Run(c, m) }}
}

// lossy loses or duplicates one message in five.
var lossy = qenv.Rates{Default: 0.2}

// Through lost requests, lost responses and duplicates, every request
// counts once.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	system(Faithful).Explore(t, qtest.ExploreConfig{Seeds: 30, Rates: lossy}).Expect(t, qtest.Conforms())
}

// Without dedupe a retried request is counted again: the model has no
// apply of a request already applied, so some run's apply is rejected.
// With no faults there is no retry, and the bug is invisible.
func TestNoDedupeCountsRetries(t *testing.T) {
	qtest.RequireQuint(t)
	system(NoDedupe).Explore(t, qtest.ExploreConfig{Seeds: 30, Rates: lossy}).
		Expect(t, qtest.Some(qtest.Rejects("apply")), qtest.Some(qtest.Conforms()))
	noFaults := qenv.Rates{Kinds: map[string]float64{"fault": 0}} // a zero Rates is the default rates
	system(NoDedupe).Explore(t, qtest.ExploreConfig{Seeds: 5, Rates: noFaults}).Expect(t, qtest.Conforms())
}

// Hegel makes every choice of the same System and shrinks NoDedupe's
// failure to the one fault it needs (a duplicated request, or a lost
// response and its retry); in the faithful counter it finds nothing.
func TestHegelShrinks(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinks against the model")
	}
	v := quinthegel.Shrink(t, system(NoDedupe), hegel.WithTestCases(50))
	v.Expect(t, qtest.Rejects("apply"))
	if f := v.Faults(); len(f) != 1 {
		t.Errorf("shrunk to faults %v, want one", f)
	}
	quinthegel.Shrink(t, system(Faithful), hegel.WithTestCases(20)).Expect(t, qtest.Conforms())
}
