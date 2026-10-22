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
	"context"
	"fmt"
	"github.com/casselc/quint-connect-go/qobs"
	"math/rand/v2"
	"strings"
)

// A Chooser makes the environment's choices. Choose returns an index in
// [0, n), n >= 2. Kinds starting with "fault." are fault choices, where 0 is
// the nominal option; any other kind is a schedule choice, where every
// option is normal and 0 is the canonical one (the oldest, the first).
type Chooser interface {
	Choose(kind string, n int) int
}

// WithContext returns c carrying ctx: every task of a simulation made with
// it (New) has a context derived from ctx. Put what the whole run shares
// there, above all its step recorder (qobs.WithRecorder), so that runs in
// parallel do not share the global one.
func WithContext(c Chooser, ctx context.Context) Chooser { return ctxChooser{c, ctx} }

type ctxChooser struct {
	Chooser
	ctx context.Context
}

// ContextOf returns the context c carries (WithContext), or
// context.Background(): what a run driven by c elsewhere (in gosim) records
// into.
func ContextOf(c Chooser) context.Context {
	if cc, ok := As[ctxChooser](c); ok {
		return cc.ctx
	}
	return context.Background()
}

// A Wrapper is a chooser over another (a Recorder, a swarm, a Script over
// its Base, a user's own): the package sees through it with Unwrap, so a
// wrapper keeps what the chooser it wraps carries (the run's context, its
// entropy, nemeses by name).
type Wrapper interface {
	Chooser
	Unwrap() Chooser
}

// As finds the first chooser of type T in c's chain: c, then what it
// wraps (Wrapper), and so on.
func As[T any](c Chooser) (T, bool) {
	for c != nil {
		if t, ok := c.(T); ok {
			return t, true
		}
		w, ok := c.(Wrapper)
		if !ok {
			break
		}
		c = w.Unwrap()
	}
	var zero T
	return zero, false
}

// Unwrap implements Wrapper.
func (c ctxChooser) Unwrap() Chooser { return c.Chooser }

// Unwrap implements Wrapper.
func (r *Recorder) Unwrap() Chooser { return r.C }

// Unwrap implements Wrapper.
func (s *swarm) Unwrap() Chooser { return s.c }

// IsFault reports whether a choice kind is a fault choice.
func IsFault(kind string) bool { return strings.HasPrefix(kind, "fault.") }

// A Choice is one decision, as recorded by a Recorder. A chunk of entropy
// (EntropyOf) is a Choice of Kind "entropy", N bytes long, in Bytes.
type Choice struct {
	Kind  string
	N     int
	Value int
	Bytes []byte
	// Step is how many steps the run had recorded when the choice was made
	// (a Recorder's, through the run's step recorder): which step a fault
	// came after. Replay ignores it.
	Step int
}

func (c Choice) String() string {
	if c.Kind == "entropy" {
		return fmt.Sprintf("entropy/%d", c.N)
	}
	return fmt.Sprintf("%s=%d/%d", c.Kind, c.Value, c.N)
}

// Nominal makes every choice 0: no faults, canonical schedule.
type Nominal struct{}

// Choose implements Chooser.
func (Nominal) Choose(string, int) int { return 0 }

// Entropy is how a chooser hands a simulation that is not qenv's (gosim's,
// through qgosim) bytes from which to make its own choices other than
// faults: one call per chunk rather than one per choice. A byte (two above
// 256 options) modulo the number of options is the choice, so a zero byte
// is option 0, as quinthegel's Hegel chooser makes choices.
type Entropy interface {
	Entropy(n int) []byte
}

// EntropyOf returns c's source of entropy, through the wrappers of this
// package (WithContext, Recorder, Replay), or nil if it has none.
func EntropyOf(c Chooser) func(n int) []byte {
	for c != nil {
		switch x := c.(type) {
		case *Recorder: // records the chunks it passes on
			e := EntropyOf(x.C)
			if e == nil {
				return nil
			}
			return func(n int) []byte {
				b := e(n)
				x.Choices = append(x.Choices, Choice{Kind: "entropy", N: n, Bytes: b})
				return b
			}
		case *replay:
			return x.entropy
		case Entropy:
			return x.Entropy
		}
		w, ok := c.(Wrapper)
		if !ok {
			return nil
		}
		c = w.Unwrap()
	}
	return nil
}

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

// Entropy implements Entropy: seeded bytes.
func (s *Seeded) Entropy(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(s.r.Uint32())
	}
	return b
}

// Recorder records the choices of the Chooser it wraps, to replay a run.
type Recorder struct {
	C       Chooser
	Choices []Choice

	steps *qobs.Recorder // the run's step recorder, found on the first choice
}

// Choose implements Chooser.
func (r *Recorder) Choose(kind string, n int) int {
	v := r.C.Choose(kind, n)
	r.Choices = append(r.Choices, Choice{Kind: kind, N: n, Value: v, Step: r.progress()})
	return v
}

// progress is the run's step count: its recorder is the one the calling
// goroutine's run records into (qrun), the same for every choice of a run.
func (r *Recorder) progress() int {
	if r.steps == nil {
		r.steps = qobs.From(nil)
	}
	if r.steps == nil {
		return 0
	}
	return r.steps.Count()
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

// entropy replays a recorded chunk of entropy: the same simulation asks for
// the same chunks at the same points.
func (r *replay) entropy(n int) []byte {
	if r.i >= len(r.cs) {
		return make([]byte, n)
	}
	c := r.cs[r.i]
	if c.Kind != "entropy" || c.N != n {
		panic(fmt.Sprintf("qenv: replay diverged at choice %d: recorded %s/%d, asked for %d bytes of entropy (the run is not deterministic)", r.i, c.Kind, c.N, n))
	}
	r.i++
	return c.Bytes
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

// Without turns the given fault kinds (prefixes, as in Swarm) off for a
// whole run: a sweep's row says which kinds are on (qtest.SweepExplore).
func Without(c Chooser, kinds ...string) Chooser {
	s := &swarm{c: c, off: map[string]bool{}}
	for _, k := range kinds {
		s.off[k] = true
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
