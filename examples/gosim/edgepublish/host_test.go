//go:build !sim

// The edgepublish model checks on gosim: the binding, model and expected
// verdicts of ../../edgepublish/sim/sim_test.go, with the publisher
// running on gosim's runtime instead of woven into qenv's scheduler.
package edgepublish_test

import (
	"fmt"
	"testing"

	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/qgosim"
	"github.com/casselc/quint-connect-go/qtest"
)

const (
	pkg     = "github.com/casselc/quint-connect-go/examples/gosim/edgepublish"
	binding = "../../edgepublish/binding.yaml"
)

type config struct {
	mutation               publisher.Mutation
	noCrashes, noAmbiguous bool
}

func explore(t *testing.T, c config, seeds int, opts ...qtest.Option) *qtest.Exploration {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	return qtest.Explore(t, binding, qtest.ExploreConfig{Seeds: seeds, Opts: opts}, qgosim.Scenario(t, pkg, "TestPublisher",
		fmt.Sprintf("EDGE_MUTATION=%d", c.mutation), fmt.Sprintf("EDGE_NOCRASHES=%d", b(c.noCrashes)),
		fmt.Sprintf("EDGE_NOAMBIGUOUS=%d", b(c.noAmbiguous))))
}

// Every interleaving of pushes, rotations and background seals, with S3
// writes that fail or fail ambiguously and crashes of the exporter's
// machine, is a behaviour of the model.
func TestExploredRunsConformOnGosim(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, config{}, 10, qtest.Invariants()).Expect(t, qtest.Conforms())
}

// Without ambiguous writes, the invariants hold too.
func TestInvariantsHoldWithoutAmbiguousWritesOnGosim(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, config{noAmbiguous: true}, 10).Expect(t, qtest.Conforms())
}

// The mutants the woven qenv runs catch, caught on gosim.
func TestMutantsOnGosim(t *testing.T) {
	qtest.RequireQuint(t)
	t.Run("ManifestBeforeTable", func(t *testing.T) {
		explore(t, config{mutation: publisher.ManifestBeforeTable, noCrashes: true}, 10, qtest.Invariants()).
			Expect(t, qtest.Rejects("writeManifest"))
	})
	t.Run("NoRotationLock", func(t *testing.T) {
		explore(t, config{mutation: publisher.NoRotationLock, noCrashes: true}, 30, qtest.Invariants()).
			Expect(t, qtest.AnyOf(qtest.Rejects("rotateGen"), qtest.Rejects("writeTable"), qtest.Rejects("writeParquet"), qtest.Rejects("writeManifest")))
	})
}
