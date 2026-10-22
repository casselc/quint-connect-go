package sim_test

import (
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/edgepublish/sim"
	"github.com/casselc/quint-connect-go/qmeta"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

var dip = qmeta.Trigger{Slowdown: 3, Duration: time.Minute}

// 3 requests/s (9 S3 writes/s against a capacity of 10) and an S3 three
// times slower for a minute: the exporter recovers, with 4 exporters or
// 16. Its concurrency is bounded and the collector pauses before a
// retry, so there is no retry storm to sustain a collapse.
func TestLoadRecovers(t *testing.T) {
	woven(t)
	for _, workers := range []int{4, 16} {
		cfg := sim.LoadConfig{Rate: 3, Workers: workers}
		if n := qmeta.Count(qmeta.Of(cfg.Experiment(nil), sim.Goodput, 5*time.Minute), dip, 3); n != 3 {
			t.Errorf("%d exporters: %d of 3 runs recover", workers, n)
		}
	}
}

// What load does do is turn S3's queueing delay into ambiguous writes. A
// write that times out may land, and the collector's retry commits the
// request again (the model's finding F3). With 16 exporters and a 2 s
// timeout this happens with no trigger at all; a timeout above S3's
// worst queueing delay removes it.
func TestTimeoutsMakeDuplicates(t *testing.T) {
	woven(t)
	dups := func(timeout time.Duration) int {
		c := sim.NewCommits(nil)
		qmeta.Assess(sim.LoadConfig{Rate: 3, Workers: 16, Timeout: timeout}.Experiment(c), sim.Goodput, qmeta.Trigger{}, 5*time.Minute, 1)
		return c.Duplicates()
	}
	short, long := dups(2*time.Second), dups(10*time.Second)
	t.Logf("requests committed twice: %d with a 2 s timeout, %d with 10 s", short, long)
	if short == 0 || long != 0 {
		t.Errorf("duplicates: %d with a 2 s timeout, %d with 10 s; want some, then none", short, long)
	}
}

// Half a minute of that load (with a 1 s timeout, so it duplicates soon),
// checked against the model: the protocol holds (every step a
// transition), and the duplicates are the model's F3. Checking cost grows
// with the run (about 375 s for a minute's 750 steps), so the run is
// short.
func TestLoadRunAgainstModel(t *testing.T) {
	woven(t)
	if testing.Short() {
		t.Skip("checks a run of several hundred steps")
	}
	mem := &qobs.MemorySink{}
	c := sim.NewCommits(mem)
	sim.LoadConfig{Rate: 3, Workers: 16, Timeout: time.Second}.Experiment(c).Run(1, qmeta.Trigger{}, 30*time.Second)
	t.Logf("%d steps, %d requests committed twice", len(mem.Steps()), c.Duplicates())
	if c.Duplicates() == 0 {
		t.Fatal("no duplicates in this minute: the check below would be vacuous")
	}
	qtest.Validate(t, binding, mem.Steps(), qtest.Invariants()).Expect(t, qtest.Conforms())
	qtest.Validate(t, binding, mem.Steps()).Expect(t, qtest.Violates("committedNoDuplicatePayload"))
}
