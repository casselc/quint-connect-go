package qload_test

import (
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qload"
)

// The running example of Alvaro et al.: one server (10 requests/s, queue
// 100), a client at 9.5 requests/s with a 9 s timeout and 3 retries. A
// spike to 20 requests/s between 200 s and 400 s fills the queue, and it
// stays full long after the spike: a metastable failure. Throttled to 8
// requests/s, the same spike drains away.
func example(rate float64) qload.Config {
	return qload.Config{
		Server: qload.ServerConfig{Name: "server", ServiceRate: 10, QueueCap: 100},
		Client: qload.ClientConfig{Name: "client", Rate: qload.Spike(rate, 20, 200*time.Second, 400*time.Second),
			Timeout: 9 * time.Second, Retries: 3},
		Horizon: 1000 * time.Second, Seed: 1,
	}
}

func queue(s qload.Sample) float64   { return float64(s.Queue) }
func goodput(s qload.Sample) float64 { return float64(s.Successes) }

func TestRetryStormIsMetastable(t *testing.T) {
	t0 := time.Now()
	stuck := qload.Simulate(example(9.5))
	t.Logf("simulated 1000 s in %v", time.Since(t0))
	gBefore := stuck.Mean(100*time.Second, 200*time.Second, goodput)
	gAfter := stuck.Mean(800*time.Second, 1000*time.Second, goodput)
	after := stuck.Mean(800*time.Second, 1000*time.Second, queue)
	t.Logf("9.5 rps: goodput %.1f/s before the spike, %.1f/s long after; queue %.0f long after", gBefore, gAfter, after)
	if gBefore < 8.5 || gAfter > 0.7*gBefore || after < 80 {
		t.Errorf("want full goodput before the spike, and a full queue and lost goodput long after")
	}
	recovers := qload.Simulate(example(8))
	after = recovers.Mean(800*time.Second, 1000*time.Second, queue)
	t.Logf("8 rps: queue %.0f long after", after)
	if after > 20 {
		t.Errorf("throttled to 8 rps, the queue should drain")
	}
}
