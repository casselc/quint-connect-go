// The tests that drive the simulated cluster (cluster/cluster_test.go)
// from outside gosim: a qenv Chooser makes every choice of the run (the
// schedule, select cases, map order, the client's picks and the faults)
// over gosim's metatest protocol, and the steps the nodes record come back
// to be checked against ../../glomers/model/broadcast.qnt with the Glomers
// binding, as for the qenv-free Maelstrom simulator.
//
// Run ../../../qgosim/fetch-gosim.sh first. The gosim test binary is built
// on demand (gosim build-tests ./cluster) the first time a test needs it.
package broadcast_test

import (
	"bytes"
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qgosim"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/quinthegel"
)

const (
	clusterPkg = "github.com/casselc/quint-connect-go/examples/gosim/broadcast/cluster"
	binding    = "../../glomers/broadcast/broadcast.binding.yaml"
)

// run runs the simulated cluster once, with mutation ("" for the faithful
// node), every choice made by c, and returns the recorded steps.
func run(t *testing.T, c qenv.Chooser, mutation string) []qtrace.Step {
	t.Helper()
	steps, err := qgosim.Run(t, clusterPkg, "TestBroadcast", c, "BROADCAST_MUTATION="+mutation)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// Seeded runs, faults at qenv's default rates: the faithful node converges
// under partitions, and every read returns what the model's node holds.
func TestSeededRunsConform(t *testing.T) {
	qtest.RequireQuint(t)
	for seed := uint64(1); seed <= 5; seed++ {
		c := qenv.NewSeeded(seed)
		c.Rates.Kinds = map[string]float64{"fault.partition": 0.3}
		steps := run(t, c, "")
		qtest.Check(t, binding, steps).Expect(t, qtest.Conforms())
		t.Logf("seed %d: %d steps", seed, len(steps))
	}
}

// The same choices give the same run: a recorded run replays exactly, and
// qenv.Replay would panic at the first choice that differs.
func TestReplayIsDeterministic(t *testing.T) {
	rec := &qenv.Recorder{C: qenv.NewSeeded(7)}
	first := run(t, rec, "")
	second := run(t, qenv.Replay(rec.Choices), "")
	if len(rec.Choices) == 0 || len(first) != len(second) {
		t.Fatalf("%d choices; %d steps, then %d on replay", len(rec.Choices), len(first), len(second))
	}
	for i := range first {
		a, _ := first[i].MarshalLine()
		b, _ := second[i].MarshalLine()
		if !bytes.Equal(a, b) {
			t.Fatalf("step %d differs on replay:\n%s\n%s", i, a, b)
		}
	}
	t.Logf("%d choices, %d steps, replayed identically", len(rec.Choices), len(first))
}

// A read that misses gossiped values is caught at the read.
func TestReadOwnOnlyRejected(t *testing.T) {
	qtest.RequireQuint(t)
	steps := run(t, qenv.NewSeeded(1), "ReadOwnOnly")
	qtest.Validate(t, binding, steps).Expect(t, qtest.Mismatch("read"))
}

// Hegel makes the choices and the model judges: Hegel finds a run in which
// fire-and-forget gossip (NoRetry) leaves a node without a value after the
// cluster settles, and shrinks it toward the nominal schedule and the
// fewest partitions.
func TestHegelFindsNoRetry(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: binding, Global: true}, func(tc hegel.TestCase, rec *quinthegel.Recorder) {
		c := quinthegel.Chooser(tc)
		c.Rates.Kinds = map[string]float64{"fault.partition": 0.3}
		rec.Add(run(t, c, "NoRetry")...)
	}, hegel.WithTestCases(60), hegel.WithSeed(1))
	if !strings.Contains(v.Summary, "convergedWhenSettled") {
		t.Fatalf("want convergence to fail:\n%s", v.Summary)
	}
}
