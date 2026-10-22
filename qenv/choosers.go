package qenv

import (
	"context"

	"github.com/casselc/quint-connect-go/qenv/choice"
	"github.com/casselc/quint-connect-go/qenv/profile"
)

// The choosers, re-exported from qenv/choice (see there).
type (
	Chooser = choice.Chooser
	Choice  = choice.Choice
	Wrapper = choice.Wrapper
	Entropy = choice.Entropy
	Nominal = choice.Nominal
	Rates   = choice.Rates
	Seeded  = choice.Seeded
	// Recorder logs a run's choices; see choice.Recorder.
	Recorder = choice.Recorder
	Bytes    = choice.Bytes
	// Profile is where a simulation's virtual time goes (qenv/profile).
	Profile = profile.Profile
)

// NewProfile is profile.New.
func NewProfile() *Profile { return profile.New() }

// DefaultRates are choice.DefaultRates.
var DefaultRates = choice.DefaultRates

// WithContext is choice.WithContext.
func WithContext(c Chooser, ctx context.Context) Chooser { return choice.WithContext(c, ctx) }

// ContextOf is choice.ContextOf.
func ContextOf(c Chooser) context.Context { return choice.ContextOf(c) }

// As is choice.As.
func As[T any](c Chooser) (T, bool) { return choice.As[T](c) }

// IsFault is choice.IsFault.
func IsFault(kind string) bool { return choice.IsFault(kind) }

// EntropyOf is choice.EntropyOf.
func EntropyOf(c Chooser) func(n int) []byte { return choice.EntropyOf(c) }

// NewSeeded is choice.NewSeeded.
func NewSeeded(seed uint64) *Seeded { return choice.NewSeeded(seed) }

// Replay is choice.Replay.
func Replay(choices []Choice) Chooser { return choice.Replay(choices) }

// Swarm is choice.Swarm.
func Swarm(c Chooser, kinds ...string) Chooser { return choice.Swarm(c, kinds...) }

// Without is choice.Without.
func Without(c Chooser, kinds ...string) Chooser { return choice.Without(c, kinds...) }

// FromBytes is choice.FromBytes.
func FromBytes(data []byte, rates Rates) *Bytes { return choice.FromBytes(data, rates) }

// EncodeChoices is choice.EncodeChoices.
func EncodeChoices(choices []Choice, rates Rates) ([]byte, error) {
	return choice.EncodeChoices(choices, rates)
}
