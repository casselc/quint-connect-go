package sim_test

import (
	"context"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/examples/edgepublish/sim"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

const binding = "../binding.yaml"

// woven skips unless the build wove the publisher (orchestrion go test):
// without it nothing records, and its real locks would block the
// simulation's scheduler.
func woven(t *testing.T) {
	t.Helper()
	qtest.RequireQuint(t)
	if !sim.Woven() {
		t.Skip("needs the build woven: go run github.com/DataDog/orchestrion go test ./sim/")
	}
}

func explore(t *testing.T, cfg sim.Config, seeds int, opts ...qtest.Option) *qtest.Exploration {
	return qtest.Explore(t, binding, qtest.ExploreConfig{Seeds: seeds, Opts: opts}, func(c qenv.Chooser) {
		if err := sim.Run(c, cfg); err != nil {
			t.Error(err)
		}
	})
}

// Every interleaving of pushes, rotations and background seals, with S3
// writes that fail or fail ambiguously and crashes, is a behaviour of the
// model: the publisher's protocol conforms (checked without invariants).
func TestExploredRunsConform(t *testing.T) {
	woven(t)
	explore(t, sim.Config{}, 40, qtest.Invariants()).Expect(t, qtest.Conforms())
}

// With plain failures only, the design's invariants hold too.
func TestInvariantsHoldWithoutAmbiguousWrites(t *testing.T) {
	woven(t)
	explore(t, sim.Config{NoAmbiguous: true}, 40).Expect(t, qtest.Conforms())
}

// With ambiguous writes, exploration finds the model's known finding on
// its own: a manifest PUT that lands but reports failure is retried as a
// new batch, so the payload is committed twice (F3), and the seal, counted
// in memory, misses the batch it never knew of (F1). The hand-written
// ambiguous-seal scenario reproduced this; here random runs find it.
func TestExplorationFindsAmbiguousFindings(t *testing.T) {
	woven(t)
	explore(t, sim.Config{}, 60).Expect(t, qtest.AnyOf(qtest.Violates("committedNoDuplicatePayload"), qtest.Violates("sealMatchesManifests")))
}

// The mutants, found by exploration rather than by a scenario built for
// each: a manifest written before its data, and a rotation that does not
// wait for in-flight pushes (an interleaving the woven RWMutex makes the
// scheduler choose).
func TestMutants(t *testing.T) {
	woven(t)
	t.Run("ManifestBeforeTable", func(t *testing.T) {
		explore(t, sim.Config{Mutation: publisher.ManifestBeforeTable, NoCrashes: true}, 20, qtest.Invariants()).
			Expect(t, qtest.Rejects("writeManifest"))
	})
	t.Run("NoRotationLock", func(t *testing.T) {
		explore(t, sim.Config{Mutation: publisher.NoRotationLock, NoCrashes: true}, 40, qtest.Invariants()).
			Expect(t, qtest.AnyOf(qtest.Rejects("rotateGen"), qtest.Rejects("writeTable"), qtest.Rejects("writeParquet"), qtest.Rejects("writeManifest")))
	})
	// The stand-in's acquire before it caught up with publish.go's fix:
	// a push that read the clock, then waited for the exclusive lock past
	// a boundary another push rotated across, rotates back into a sealed
	// generation. Exploration found it in the stand-in. The window is
	// narrow (about 3 runs in 100 with 3 exporters and 12 requests), so
	// it takes more seeds than the other mutants.
	t.Run("BackwardRotation", func(t *testing.T) {
		if testing.Short() {
			t.Skip("150 seeds")
		}
		explore(t, sim.Config{Mutation: publisher.BackwardRotation, NoCrashes: true, NoAmbiguous: true, Workers: 3, Requests: 12}, 150, qtest.Invariants()).
			Expect(t, qtest.Rejects("rotateGen"))
	})
}

// Hegel makes every choice (interleaving, latencies, faults) and shrinks
// NoRotationLock to a rotation while a push is between its writes.
// (BackwardRotation's window is too narrow for Hegel's budget here: a few
// runs in a hundred, each case a quint check.)
func TestHegelShrinksNoRotationLock(t *testing.T) {
	woven(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: binding}, func(tc hegel.TestCase, rec *quinthegel.Recorder) {
		c := qenv.WithContext(qenv.Hegel(tc), rec.Context(context.Background()))
		if err := sim.Run(c, sim.Config{Mutation: publisher.NoRotationLock, NoCrashes: true, NoAmbiguous: true, Requests: 4}); err != nil {
			tc.Errorf("%v", err)
		}
	}, hegel.WithTestCases(80))
	if !strings.Contains(v.Summary, "quint:    rotateGen") {
		t.Errorf("want the model to reject a rotation:\n%s", v.Summary)
	}
	t.Logf("shrunk to %d steps:\n%s", len(v.Steps), quinthegel.Render(v.Steps))
}
