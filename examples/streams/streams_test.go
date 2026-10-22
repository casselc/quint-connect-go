package streams_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/streams"
	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

const binding = "streams.binding.yaml"

// system is the pipeline as cfg deploys it, bound to the model.
func system(cfg streams.Config) qtest.System {
	return qtest.System{Binding: binding, Run: func(c qenv.Chooser) error {
		_, err := streams.Run(c, cfg)
		return err
	}}
}

// seeds is n, or half of it under -short: every bug here shows in well
// under half of its seeds.
func seeds(n int) int {
	if testing.Short() {
		return n / 2
	}
	return n
}

// faulty raises every fault kind to one choice in ten, nemeses (crashes,
// evictions) to one step in twenty.
var faulty = qenv.Rates{Default: 0.1, Kinds: map[string]float64{"fault.nemesis": 0.05}}

// The faithful pipeline delivers every event exactly once, in key order,
// through lost produce requests and acknowledgements, lost commits,
// rewound fetches, evictions mid-batch and consumer crashes.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	x := system(streams.Config{}).Explore(t, qtest.ExploreConfig{Seeds: seeds(80), Rates: faulty})
	x.Expect(t, qtest.Conforms())
	t.Log(x.Evidence())
}

// Which check catches which bug. Each mutant runs with the faults that
// make its bug possible; the model names the property it breaks.
func TestMutants(t *testing.T) {
	qtest.RequireQuint(t)
	cases := []struct {
		m     pipeline.Mutation
		seeds int
		rates qenv.Rates
		want  qtest.Claim
	}{
		// committing a batch before applying it is past unapplied records
		// at once, crash or not: every run shows it
		{pipeline.CommitFirst, 10, qenv.DefaultRates, qtest.All(qtest.Violates("committedApplied"))},
		// a redelivery (a lost commit, a rewind, an eviction, a crash, a
		// duplicate write) applies an event twice
		{pipeline.NoDedupe, 20, qenv.DefaultRates, qtest.Violates("appliedAtMostOnce")},
		// a lost request retried behind the next one: the key's later event
		// is applied first
		{pipeline.NonIdempotentProducer, 60, faulty, qtest.Violates("perKeyOrder")},
		// each partition's first record skipped, in every run
		{pipeline.OffByOne, 10, qenv.DefaultRates, qtest.All(qtest.Violates("committedApplied"))},
	}
	for _, c := range cases {
		t.Run(c.m.String(), func(t *testing.T) {
			system(streams.Config{Mutation: c.m}).Explore(t, qtest.ExploreConfig{Seeds: seeds(c.seeds), Rates: c.rates}).Expect(t, c.want)
		})
	}
}

// A plain test of the projection, on the nominal run (no faults, the
// canonical schedule), sees one bug of four: the others need a fault to
// show, which the model then names.
func TestPlainTestSeesOnlyOffByOne(t *testing.T) {
	for _, m := range []pipeline.Mutation{pipeline.Faithful, pipeline.CommitFirst, pipeline.NoDedupe, pipeline.NonIdempotentProducer, pipeline.OffByOne} {
		d, err := streams.Run(qenv.Nominal{}, streams.Config{Mutation: m})
		if err != nil {
			t.Fatal(err)
		}
		ok := true
		for _, k := range d.Keys {
			if v, _ := d.Projection.Read("total/" + k); v != 1+2+3 {
				ok = false
			}
		}
		if want := m != pipeline.OffByOne; ok != want {
			t.Errorf("%s: projection right %v, want %v", m, ok, want)
		}
	}
}
