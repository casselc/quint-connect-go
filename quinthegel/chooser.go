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
	buf   []byte
}

// Chooser returns a qenv chooser drawing from tc, with qenv.DefaultRates.
func Chooser(tc hegel.TestCase) *EnvChooser { return &EnvChooser{tc: tc, Rates: qenv.DefaultRates} }

func (h *EnvChooser) byte() int {
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

// Entropy implements qenv.Entropy: a chunk drawn from the test case, which
// Hegel shrinks toward zeros, the nominal choices.
func (h *EnvChooser) Entropy(n int) []byte { return hegel.Draw(h.tc, hegel.Binary(n, n)) }

// Choose implements qenv.Chooser.
func (h *EnvChooser) Choose(kind string, n int) int {
	if !qenv.IsFault(kind) {
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
