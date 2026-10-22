//go:build sim

// The edgepublish publisher (../../edgepublish/publisher), unchanged and
// unwoven by Orchestrion, in its simulated environment
// (../../edgepublish/sim) on qenv's native runtime in gosim. Its
// sync.RWMutex, sync.WaitGroup and background goroutine are gosim's, so no
// qenv/weave is needed; its //quint:action annotations are woven by the
// translator from qobs's orchestrion.yml (../gosim.tool.go).
package edgepublish_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/examples/edgepublish/sim"
	"github.com/casselc/quint-connect-go/qgosim"
)

func env(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func TestPublisher(t *testing.T) {
	if !sim.Woven() {
		t.Fatal("the publisher's //quint:action annotations were not woven: is gosim.tool.go importing qobs?")
	}
	cfg := sim.Config{Mutation: publisher.Mutation(env("EDGE_MUTATION")), NoCrashes: env("EDGE_NOCRASHES") == 1,
		NoAmbiguous: env("EDGE_NOAMBIGUOUS") == 1, Requests: env("EDGE_REQUESTS"), Workers: env("EDGE_WORKERS")}
	if err := sim.RunOn(qgosim.New(), cfg); err != nil {
		t.Fatal(err)
	}
}
