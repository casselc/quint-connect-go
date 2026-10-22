package hello

import (
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
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
