package qmeta

import (
	"math"
	"math/rand/v2"
	"time"
)

// A Service is a measured service-time distribution: the latencies of a
// handler run alone (a benchmark, or requests that found the server idle),
// which give the chain its service rate and a simulation its service
// times, instead of guessed ones.
type Service struct {
	Samples []time.Duration
	Mean    time.Duration
	// CV is the coefficient of variation (standard deviation / mean): 1 for
	// the exponential service the chain assumes, 0 for a constant one.
	CV float64
}

// Measure summarizes service-time samples.
func Measure(samples []time.Duration) Service {
	s := Service{Samples: append([]time.Duration(nil), samples...)}
	if len(samples) == 0 {
		return s
	}
	var sum float64
	for _, d := range samples {
		sum += float64(d)
	}
	mean := sum / float64(len(samples))
	var sq float64
	for _, d := range samples {
		sq += (float64(d) - mean) * (float64(d) - mean)
	}
	s.Mean = time.Duration(mean)
	if mean > 0 && len(samples) > 1 {
		s.CV = math.Sqrt(sq/float64(len(samples)-1)) / mean
	}
	return s
}

// Mu is the service rate of one worker, per second: the chain's Params.Mu.
func (s Service) Mu() float64 {
	if s.Mean <= 0 {
		return 0
	}
	return 1 / s.Mean.Seconds()
}

// Exponential reports whether the chain's exponential service is a fair
// approximation (CV within a third of 1). When it is not, check the
// chain's verdicts against a simulation with the measured times (Draw).
// Regular service (a CV near 0) keeps a healthy queue shorter, but a queue
// holding more than the service rate times the timeout then times out
// every request in it, with certainty: the full queue is absorbing, while
// under exponential service some requests still finish in time and it
// drains (examples/retrystorm TestServiceTimeShape: recovery 6 of 6 seeds
// becomes 0 of 6 at the same mean).
func (s Service) Exponential() bool { return math.Abs(s.CV-1) <= 1.0/3 }

// Draw draws a service time from the samples: the measured distribution,
// not its exponential approximation, for a load simulation
// (qload.ServerConfig.Service) or a qenv.Capacity's Time.
func (s Service) Draw(rnd *rand.Rand) time.Duration {
	if len(s.Samples) == 0 {
		return s.Mean
	}
	return s.Samples[rnd.IntN(len(s.Samples))]
}
