// Not built inside gosim (the sim tag): Hegel runs on the host, and its
// native library cannot be translated into a simulation. There, the host's
// Chooser, Hegel's included, makes the choices (DESIGN.md §14).

//go:build !sim

package qenv

import (
	"fmt"

	"hegel.dev/go/hegel"
)

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
