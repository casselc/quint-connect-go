package choice

import "fmt"

// Bytes makes choices from a stream of bytes, by one rule shared by every
// byte-driven chooser (Hegel's, Go fuzzing's, a corpus entry):
//   - a schedule choice of n options is a byte (two above 256 options),
//     modulo n;
//   - a fault choice is nominal when a byte is below 256 × (1 − its rate);
//     otherwise its option is 1 plus a byte modulo n−1 (no byte for n=2).
//
// A zero byte is the nominal or canonical option, so a shrinker's zeros are
// fewer faults and the canonical schedule; past the end of the stream
// every choice is nominal.
type Bytes struct {
	// Next returns the next n bytes (fewer, or none, at the end).
	Next  func(n int) []byte
	Rates Rates
	// Chunk is how many bytes Next is asked for at a time (default 512):
	// a Hegel draw per chunk, not per choice.
	Chunk int
	// OnFault, if set, is told each fault taken (Hegel notes them).
	OnFault func(kind string, v int)
	buf     []byte
	ended   bool
	err     error
}

// BytesVersion is the version of the byte rule. EncodeChoices writes it in
// a header, so a corpus entry encoded by another rule is recognised
// (Err), not replayed as another run.
const BytesVersion = 1

// header marks an encoded run: 0xff, "qc", the version.
var header = []byte{0xff, 'q', 'c', BytesVersion}

// FromBytes makes choices from data (a fuzz input, a corpus entry) at the
// given rates. An encoded run's header (EncodeChoices) is skipped; one of
// another version makes every choice nominal and is reported by Err.
func FromBytes(data []byte, rates Rates) *Bytes {
	rest := data
	var err error
	if len(rest) >= len(header) && rest[0] == header[0] && rest[1] == header[1] && rest[2] == header[2] {
		if rest[3] != BytesVersion {
			err = fmt.Errorf("qenv: choices encoded by byte rule version %d, not %d: encode the run again (EncodeChoices) from its choices", rest[3], BytesVersion)
			rest = nil
		} else {
			rest = rest[len(header):]
		}
	}
	return &Bytes{Rates: rates, err: err, Next: func(n int) []byte {
		n = min(n, len(rest))
		out := rest[:n]
		rest = rest[n:]
		return out
	}}
}

// Err is why the bytes cannot be replayed (an encoding of another
// version), or nil.
func (b *Bytes) Err() error { return b.err }

func (b *Bytes) byte() int {
	if len(b.buf) == 0 && !b.ended {
		n := b.Chunk
		if n == 0 {
			n = 512
		}
		b.buf = b.Next(n)
		if len(b.buf) == 0 {
			b.ended = true
		}
	}
	if len(b.buf) == 0 {
		return 0
	}
	v := b.buf[0]
	b.buf = b.buf[1:]
	return int(v)
}

// Choose implements Chooser.
func (b *Bytes) Choose(kind string, n int) int {
	if !IsFault(kind) {
		v := b.byte()
		if n > 256 {
			v = v<<8 | b.byte()
		}
		return v % n
	}
	if b.byte() < int(256*(1-b.Rates.Of(kind))) {
		return 0
	}
	v := 1
	if n > 2 {
		v = 1 + b.byte()%(n-1)
	}
	if b.OnFault != nil {
		b.OnFault(kind, v)
	}
	return v
}

// Entropy implements Entropy: the next n bytes, zeros past the end.
func (b *Bytes) Entropy(n int) []byte {
	out := make([]byte, n)
	copy(out, b.Next(n))
	return out
}

// EncodeChoices is the byte stream from which Bytes, at the given rates,
// makes the recorded choices again: a recorded run, or a shrunk Hegel
// failure (quinthegel.Violation.Choices), as a fuzz corpus entry. A fault
// of a kind whose rate is 0 cannot be encoded, and neither can entropy
// chunks; both are reported. The bytes start with a header naming the
// byte rule's version (BytesVersion).
func EncodeChoices(choices []Choice, rates Rates) ([]byte, error) {
	out := append([]byte(nil), header...)
	for _, c := range choices {
		switch {
		case c.Kind == "entropy":
			return nil, fmt.Errorf("qenv: entropy chunks (a gosim run's) are not encoded; replay them with Replay")
		case !IsFault(c.Kind):
			if c.N > 256 {
				out = append(out, byte(c.Value>>8), byte(c.Value))
			} else {
				out = append(out, byte(c.Value))
			}
		case c.Value == 0:
			if rates.Of(c.Kind) >= 1 {
				return nil, fmt.Errorf("qenv: nominal %s at rate 1 cannot be encoded: lower its rate", c.Kind)
			}
			out = append(out, 0)
		default:
			if rates.Of(c.Kind) <= 0 {
				return nil, fmt.Errorf("qenv: fault %s=%d at rate 0 cannot be encoded: give its kind a rate", c.Kind, c.Value)
			}
			out = append(out, 255)
			if c.N > 2 {
				out = append(out, byte(c.Value-1))
			}
		}
	}
	return out, nil
}
