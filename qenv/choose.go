// Package qenv simulates the environment a system runs in (scheduling,
// time, crashes, stores, queues, remote calls) deterministically, with every
// nondeterministic choice made by one Chooser. A fault is a choice the
// environment's contract allows: a lost response, a redelivered message, a
// read from an older snapshot. Option 0 of every choice is the nominal one
// (no fault, first in line), so a Hegel-driven Chooser shrinks a failing
// run toward fewer faults and a canonical schedule.
//
// The same run can be driven by Hegel (Hegel), a seed (NewSeeded), a
// recorded list of choices (Replay), or no faults at all (Nominal).
package qenv

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"hegel.dev/go/hegel"
)

// A Chooser makes the environment's choices. Choose returns an index in
// [0, n), n >= 2. Kinds starting with "fault." are fault choices, where 0 is
// the nominal option; any other kind is a schedule choice, where every
// option is normal and 0 is the canonical one (the oldest, the first).
type Chooser interface {
	Choose(kind string, n int) int
}

// IsFault reports whether a choice kind is a fault choice.
func IsFault(kind string) bool { return strings.HasPrefix(kind, "fault.") }

// A Choice is one decision, as recorded by a Recorder.
type Choice struct {
	Kind  string
	N     int
	Value int
}

func (c Choice) String() string { return fmt.Sprintf("%s=%d/%d", c.Kind, c.Value, c.N) }

// Nominal makes every choice 0: no faults, canonical schedule.
type Nominal struct{}

// Choose implements Chooser.
func (Nominal) Choose(string, int) int { return 0 }

// Rates gives the probability that a fault choice is not nominal, by kind.
// A kind matches its own entry, else the longest entry that is a prefix of
// it ("fault.rpc" covers "fault.rpc.response"), else Default.
type Rates struct {
	Default float64
	Kinds   map[string]float64
}

// Of returns the rate of a kind.
func (r Rates) Of(kind string) float64 {
	best, rate := -1, r.Default
	for k, v := range r.Kinds {
		if strings.HasPrefix(kind, k) && len(k) > best {
			best, rate = len(k), v
		}
	}
	return rate
}

// DefaultRates inject a fault at one fault choice in twenty, except a
// nemesis (a crash, a partition), which is considered at every scheduling
// step and so happens at one step in two hundred.
var DefaultRates = Rates{Default: 0.05, Kinds: map[string]float64{"fault.nemesis": 0.005}}

// Seeded makes choices from a seeded generator: schedule choices uniformly,
// fault choices at their Rates.
type Seeded struct {
	Rates Rates
	r     *rand.Rand
}

// NewSeeded returns a Seeded chooser with DefaultRates.
func NewSeeded(seed uint64) *Seeded {
	return &Seeded{Rates: DefaultRates, r: rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))}
}

// Choose implements Chooser.
func (s *Seeded) Choose(kind string, n int) int {
	if !IsFault(kind) {
		return s.r.IntN(n)
	}
	if s.r.Float64() >= s.Rates.Of(kind) {
		return 0
	}
	return 1 + s.r.IntN(n-1)
}

// HegelChooser draws the choices from a Hegel test case, so Hegel
// explores schedules and faults and shrinks them. It draws entropy in
// chunks of bytes (hegel.Binary), one draw per chunk rather than per
// choice: a simulated run makes hundreds of choices, and each draw is a
// call into Hegel's engine. Chunks have a fixed length, and a zero byte is
// the nominal (or canonical) option, so Hegel's shrinking of the bytes
// toward zeros is shrinking toward fewer faults and the canonical
// schedule. Faults are noted on the test case, so a shrunk
// failure lists the faults it needs.
type HegelChooser struct {
	tc    hegel.TestCase
	Rates Rates
	// Chunk is the number of bytes drawn at a time (default 512).
	Chunk int
	buf   []byte
}

// Hegel returns a chooser drawing from tc, with DefaultRates.
func Hegel(tc hegel.TestCase) *HegelChooser { return &HegelChooser{tc: tc, Rates: DefaultRates} }

func (h *HegelChooser) byte() int {
	if len(h.buf) == 0 {
		n := h.Chunk
		if n == 0 {
			n = 512
		}
		h.buf = hegel.Draw(h.tc, hegel.Binary(n, n))
	}
	b := h.buf[0]
	h.buf = h.buf[1:]
	return int(b)
}

// Choose implements Chooser.
func (h *HegelChooser) Choose(kind string, n int) int {
	if !IsFault(kind) {
		v := h.byte()
		if n > 256 {
			v = v<<8 | h.byte()
		}
		return v % n
	}
	if h.byte() < int(256*(1-h.Rates.Of(kind))) {
		return 0
	}
	v := 1
	if n > 2 {
		v = 1 + h.byte()%(n-1)
	}
	h.tc.Note(fmt.Sprintf("qenv: %s=%d", kind, v))
	return v
}

// Recorder records the choices of the Chooser it wraps, to replay a run.
type Recorder struct {
	C       Chooser
	Choices []Choice
}

// Choose implements Chooser.
func (r *Recorder) Choose(kind string, n int) int {
	v := r.C.Choose(kind, n)
	r.Choices = append(r.Choices, Choice{Kind: kind, N: n, Value: v})
	return v
}

// Faults returns the recorded fault choices that were not nominal.
func (r *Recorder) Faults() []Choice {
	var out []Choice
	for _, c := range r.Choices {
		if IsFault(c.Kind) && c.Value != 0 {
			out = append(out, c)
		}
	}
	return out
}

// Replay repeats recorded choices, then makes every later choice 0. A run
// that asks for a different kind or option count than was recorded is not
// deterministic (it reads time, maps or goroutines outside the simulation):
// Replay panics, naming the first difference.
func Replay(choices []Choice) Chooser { return &replay{cs: choices} }

type replay struct {
	cs []Choice
	i  int
}

func (r *replay) Choose(kind string, n int) int {
	if r.i >= len(r.cs) {
		return 0
	}
	c := r.cs[r.i]
	if c.Kind != kind || c.N != n {
		panic(fmt.Sprintf("qenv: replay diverged at choice %d: recorded %s/%d, asked %s/%d (the run is not deterministic)", r.i, c.Kind, c.N, kind, n))
	}
	r.i++
	return c.Value
}

// Swarm turns some fault kinds off for a whole run, chosen once from c:
// runs with fewer kinds of faults reach deeper into each kind (swarm
// testing). kinds are prefixes such as "fault.rpc"; each is on in about
// half of the runs, and a Hegel-driven swarm shrinks toward all off.
func Swarm(c Chooser, kinds ...string) Chooser {
	s := &swarm{c: c, off: map[string]bool{}}
	for _, k := range kinds {
		s.off[k] = c.Choose("swarm."+k, 2) == 0
	}
	return s
}

type swarm struct {
	c   Chooser
	off map[string]bool
}

func (s *swarm) Choose(kind string, n int) int {
	if IsFault(kind) {
		for k, off := range s.off {
			if off && strings.HasPrefix(kind, k) {
				return 0
			}
		}
	}
	return s.c.Choose(kind, n)
}
