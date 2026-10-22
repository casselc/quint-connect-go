package retrystorm

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
)

// The chain assumes exponential service. A measured service time with the
// same mean (100 ms) but no variance breaks that assumption, and Measure
// says so (CV 0): immediate retries at 8 requests/s recover from the spike
// in every seed with exponential service, and in none with constant
// service. The full queue (100) is more than the service rate times the
// timeout (90): with constant service every request in it times out, with
// certainty, so the server only works on requests nobody waits for.
func TestServiceTimeShape(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	var exp, flat []time.Duration
	for range 5000 {
		exp = append(exp, time.Duration(rnd.ExpFloat64()*float64(100*time.Millisecond)))
		flat = append(flat, 100*time.Millisecond)
	}
	for _, c := range []struct {
		name        string
		svc         qmeta.Service
		exponential bool
		recover     int
	}{{"exponential", qmeta.Measure(exp), true, 6}, {"constant", qmeta.Measure(flat), false, 0}} {
		if c.svc.Exponential() != c.exponential {
			t.Errorf("%s service: CV %.2f, Exponential() = %v", c.name, c.svc.CV, !c.exponential)
		}
		cfg := Policies[0].Config(8)
		cfg.Server.Service = c.svc.Draw
		if n := qload.Count(qload.OfSimulation(cfg, 8, 5*time.Minute), Spike, 6); n != c.recover {
			t.Errorf("%s service: %d of 6 seeds recover, want %d", c.name, n, c.recover)
		}
	}
}
