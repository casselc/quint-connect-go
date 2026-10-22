package qmeta_test

import (
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/quinthegel"

	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
	"hegel.dev/go/hegel"
)

func example(lambda float64) *qmeta.Chain {
	return qmeta.New(qmeta.Params{Lambda: lambda, Mu: 10, Timeout: 9, Retries: 3, Queue: 100, Orbit: 60})
}

// The chain predicts the paper's example: from a full queue, recovery at
// 9.5 requests/s takes orders of magnitude longer than at 8.
func TestRecoveryTimes(t *testing.T) {
	drained := func(s qmeta.State) bool { return s.U <= 10 }
	for _, lam := range []float64{8, 9, 9.5} {
		t0 := time.Now()
		c := example(lam)
		h := c.HittingTimes(drained)
		full := h[c.Index(qmeta.State{U: 100, V: 30})]
		half := h[c.Index(qmeta.State{U: 50, V: 0})]
		t.Logf("lambda %.1f: recovery from (100,30) %.0f s, from (50,0) %.0f s (%d states, %v)", lam, full, half, c.States(), time.Since(t0))
	}
}

func sim(lambda float64) qload.SimConfig {
	return qload.SimConfig{
		Server: qload.ServerConfig{Name: "server", ServiceRate: 10, QueueCap: 100},
		Client: qload.ClientConfig{Name: "client", Rate: qload.Constant(lambda), Timeout: 9 * time.Second, Retries: 3},
	}
}

// Calibrated on short simulations from a few states, the chain's
// predictions match longer simulations better than the nominal chain's.
func TestCalibrate(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration runs dozens of transient analyses")
	}
	times := []float64{10, 20, 40, 60}
	var data []qmeta.Trajectory
	for _, from := range []qmeta.State{{U: 100, V: 20}, {U: 60, V: 10}, {U: 20, V: 0}} {
		data = append(data, qload.SimulateTrajectory(sim(9.5), from, times, 20))
	}
	p := qmeta.Params{Lambda: 9.5, Mu: 10, Timeout: 9, Retries: 3, Queue: 100, Orbit: 60}
	t0 := time.Now()
	fit, loss := qmeta.Calibrate(p, data)
	t.Logf("nominal loss %.0f; calibrated lambda %.2f timeout %.2f, loss %.0f (%v)", qmeta.New(p).Loss(data), fit.Lambda, fit.Timeout, loss, time.Since(t0))
	if loss >= qmeta.New(p).Loss(data) {
		t.Fatal("calibration did not improve the fit")
	}
}

func TestSVG(t *testing.T) {
	var b strings.Builder
	if err := example(9.5).SVG(&b, 5, 4); err != nil || !strings.Contains(b.String(), "<line") {
		t.Fatalf("%v", err)
	}
}

// Hegel finds the smallest stressor that leaves the paper's example
// (9.5 req/s against 10) stuck, and none at 8 req/s that the system does
// not recover from within 15 minutes (from a full queue the chain predicts
// about 8).
func TestFindTrigger(t *testing.T) {
	if testing.Short() {
		t.Skip("searches hundreds of simulated runs")
	}
	space := qload.TriggerSpace{MaxExtraRate: 15, MaxDuration: 300 * time.Second, Seeds: 3}
	for _, base := range []float64{8, 9, 9.5} {
		ok, _ := qload.Mostly(qload.OfSimulation(sim(base), base, 300*time.Second), qload.Trigger{}, 5)
		t.Logf("%.1f req/s, no trigger: recovers in most runs: %v", base, ok)
	}
	t0 := time.Now()
	quinthegel.FindTrigger(t, qload.OfSimulation(sim(9.5), 9.5, 300*time.Second), space, hegel.WithTestCases(40), hegel.WithSeed(1)).Expect(t, qload.SomeTrigger())
	t.Logf("searched in %v", time.Since(t0))
	// at 8 req/s the system always recovers
	quinthegel.FindTrigger(t, qload.OfSimulation(sim(8), 8, 15*time.Minute), space, hegel.WithTestCases(30), hegel.WithSeed(1)).Expect(t, qload.NoTrigger())
}
