package quinthegel

import (
	"fmt"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/qenv"
)

// EnvChooser makes a qenv simulation's choices from a Hegel test case, so Hegel
// explores schedules and faults and shrinks them. It draws entropy in
// chunks of bytes (hegel.Binary), one draw per chunk rather than per
// choice: a simulated run makes hundreds of choices, and each draw is a
// call into Hegel's engine. Chunks have a fixed length, and a zero byte is
// the nominal (or canonical) option, so Hegel's shrinking of the bytes
// toward zeros is shrinking toward fewer faults and the canonical
// schedule. Faults are noted on the test case, so a shrunk
// failure lists the faults it needs.
type EnvChooser struct {
	tc    hegel.TestCase
	Rates qenv.Rates
	// Chunk is the number of bytes drawn at a time (default 512).
	Chunk int
	b     *qenv.Bytes
}

// Chooser returns a qenv chooser drawing from tc, with qenv.DefaultRates.
func Chooser(tc hegel.TestCase) *EnvChooser { return &EnvChooser{tc: tc, Rates: qenv.DefaultRates} }

// bytes is the shared byte rule (qenv.Bytes) over the test case's draws,
// made on the first choice so that Rates and Chunk set after Chooser
// apply.
func (h *EnvChooser) bytes() *qenv.Bytes {
	if h.b == nil {
		h.b = &qenv.Bytes{Rates: h.Rates, Chunk: h.Chunk,
			Next:    func(n int) []byte { return hegel.Draw(h.tc, hegel.Binary(n, n)) },
			OnFault: func(kind string, v int) { h.tc.Note(fmt.Sprintf("qenv: %s=%d", kind, v)) }}
	}
	return h.b
}

// Entropy implements qenv.Entropy: a chunk drawn from the test case, which
// Hegel shrinks toward zeros, the nominal choices.
func (h *EnvChooser) Entropy(n int) []byte { return hegel.Draw(h.tc, hegel.Binary(n, n)) }

// Choose implements qenv.Chooser, by the byte rule all byte-driven
// choosers share (qenv.Bytes): a shrunk failure's choices encode to a fuzz
// corpus entry (qenv.EncodeChoices).
func (h *EnvChooser) Choose(kind string, n int) int { return h.bytes().Choose(kind, n) }
