package retrystorm

import (
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
)

// The alert tier: the application's own load roles (qrole's u and v,
// sampled each second) calibrate the policy's chain; the chain's region of
// states that take over two minutes to recover becomes the alert. After the
// spike, the alert holds with backoff alone (the system stays in the
// storm) and clears with a budget (it recovers); without a spike it never
// fires.
func TestAlertsFromTheChain(t *testing.T) {
	if testing.Short() {
		t.Skip("load runs")
	}
	const lam = 9
	healthy := func(s qmeta.State) bool { return s.U <= 10 }
	for _, c := range []struct {
		p     Policy
		fires bool
	}{{Policies[1], true}, {Policies[2], false}} {
		t.Run(c.p.Name, func(t *testing.T) {
			x := c.p.Instrumented(lam)
			storm := Observations(x.Run(1, Spike, 10*time.Minute))
			params, loss := qmeta.CalibrateTimeout(c.p.Params(lam), qmeta.Trajectories(storm, []float64{5, 10, 20}, 10))
			region := qmeta.New(params).Region(healthy, 120)
			t.Logf("calibrated λ=%.2f timeout=%.2fs (loss %.3g); %d bands", params.Lambda, params.Timeout, loss, len(region.Bands(10)))

			in := 0
			last := storm[len(storm)-60:]
			for _, o := range last {
				if region.Contains(qmeta.State{U: o.U, V: o.V}) {
					in++
				}
			}
			end := last[len(last)-1]
			t.Logf("last minute: %d of %d samples in the region; ends at u=%d v=%d", in, len(last), end.U, end.V)
			if c.fires && in < len(last)*9/10 {
				t.Errorf("the system stays in the storm, but the alert holds for %d of %d samples of the last minute", in, len(last))
			}
			if !c.fires && in > 0 {
				t.Errorf("the system recovered, but %d samples of the last minute are in the region", in)
			}

			for _, o := range Observations(x.Run(2, qmeta.Trigger{}, 5*time.Minute)) {
				if region.Contains(qmeta.State{U: o.U, V: o.V}) {
					t.Fatalf("without a spike, the alert fires at %.0fs (u=%d v=%d)", o.T, o.U, o.V)
				}
			}
			rules := region.Rules(qmeta.RuleConfig{Server: "server", Service: "server"}, "u <= 10")
			if c.fires && !strings.Contains(rules, `and on() quint_orbit{service="server"} >=`) {
				t.Errorf("no band in the rules:\n%s", rules)
			}
			t.Logf("rules:\n%s", rules)
		})
	}
}
