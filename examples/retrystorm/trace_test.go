package retrystorm

import (
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/sktrace"
)

// A production workload as the load: SimKube's scaling trace, a real
// cluster's desired replicas minute by minute (27, up to 52, back to 42),
// at 0.2 requests/s per replica. Its scale-up passes the server's
// capacity for two minutes, then settles at 8.4 requests/s. Backoff alone
// does not come back from that in two of three seeds; immediate retries
// and the budgets do in all three: the spike the policies were compared
// on was synthetic, this one is a cluster's own.
func TestProductionScaleUp(t *testing.T) {
	tr, err := sktrace.ReadFile("../../sktrace/testdata/dsb-scaling.sktrace")
	if err != nil {
		t.Fatal(err)
	}
	replicas := tr.Gauge(time.Minute, sktrace.Replicas)
	const perReplica, settled = 0.2, 8.4
	goodput := func(s qload.Sample) float64 { return float64(s.Successes) }
	for _, p := range Policies {
		recovered := 0
		for seed := uint64(1); seed <= 3; seed++ {
			cfg := p.Config(0)
			cfg.Client.Rate = qload.Piecewise(time.Minute, perReplica, replicas...)
			cfg.Horizon, cfg.Seed = 40*time.Minute, seed
			if g := qload.Simulate(cfg).Mean(35*time.Minute, 40*time.Minute, goodput); g >= 0.9*settled {
				recovered++
			}
		}
		t.Logf("%s: recovered in %d of 3 seeds", p.Name, recovered)
		backoffOnly := p.Backoff && !p.Budget
		if backoffOnly && recovered > 1 || !backoffOnly && recovered < 3 {
			t.Errorf("%s: recovered in %d of 3 seeds", p.Name, recovered)
		}
	}
}
