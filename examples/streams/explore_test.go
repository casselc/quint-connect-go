package streams_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/streams"
	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qsweep"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

// Hegel makes every choice and shrinks a broken key order to a short run
// (24 steps): lost produce requests, retried behind the next record of
// their partition. The shrunk run replays outside Hegel, step for step.
func TestHegelShrinksReorder(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	sys := system(streams.Config{Mutation: pipeline.NonIdempotentProducer})
	v := quinthegel.Shrink(t, sys, faulty, hegel.WithTestCases(100), hegel.WithSeed(1))
	v.Expect(t, qtest.Violates("perKeyOrder"))
	t.Logf("shrunk to %d steps; %s", len(v.Steps), v.Report.Summary())

	replay := sys.Explore(t, qtest.ExploreConfig{Seeds: 1, Chooser: func(uint64) qenv.Chooser { return qenv.Replay(v.Choices) }})
	if got, want := quinthegel.Render(replay.Runs[0].Steps), quinthegel.Render(v.Steps); got != want {
		t.Errorf("the replayed run differs:\n%s\nfrom the shrunk run:\n%s", got, want)
	}
	replay.Expect(t, qtest.Violates("perKeyOrder"))
}

// The deployment's shape (partitions, consumers) and which fault kinds
// happen at all, pairwise: every pair of kinds together and each alone,
// at every size. The faithful pipeline conforms on every row.
func TestFaithfulSweep(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("sweeps hundreds of runs")
	}
	space := []qsweep.Factor{qsweep.Ints("partitions", 1, 2, 3), qsweep.Ints("consumers", 1, 2, 3)}
	for _, k := range []string{"log.produce", "log.commit", "log.fetch", "nemesis"} {
		space = append(space, qsweep.FaultKind(k))
	}
	sys := qtest.System{Binding: binding, Run: func(c qenv.Chooser) error {
		row, _ := qtest.RowOf(c) // the row's fault kinds that are off are off in c
		_, err := streams.Run(c, streams.Config{Partitions: row.Int("partitions"), Consumers: row.Int("consumers")})
		return err
	}}
	sw := sys.Sweep(t, qtest.Sweep{Factors: space, Explore: qtest.ExploreConfig{Seeds: 10, Rates: faulty}})
	t.Log(sw.Evidence())
}

// The faithful pipeline under Go's fuzzer: go test runs the corpus;
// go test -fuzz FuzzFaithful explores schedules and faults guided by
// coverage, the model judging every run.
func FuzzFaithful(f *testing.F) {
	qtest.RequireQuint(f)
	system(streams.Config{}).Fuzz(f, faulty)
}
