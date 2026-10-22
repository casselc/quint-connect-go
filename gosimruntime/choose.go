package gosimruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// A Chooser makes the simulation's bounded decisions: which goroutine runs
// next ("sched"), which ready case a select takes ("select"), where a map
// iteration starts ("map"), other bounded picks ("rand"), and whatever the
// simulated program asks for with Choose (faults, by convention kinds
// starting with "fault."). It returns a value in [0, n). Option 0 is
// nominal: the first runnable goroutine, the first ready case, no fault.
//
// Unbounded randomness (math/rand, crypto/rand, hash seeds) comes from a
// generator seeded by one choice of kind "seed".
//
// A qenv.Chooser is a Chooser.
type Chooser interface {
	Choose(kind string, n int) int
}

// Choose asks the run's chooser for a value in [0, n). Without a chooser it
// draws from the seeded generator, as before.
func Choose(kind string, n int) int {
	if n <= 1 {
		return 0
	}
	return int(gs.get().choose(kind, uint32(n)))
}

// Emit sends a line to the host running this simulation (a metatest
// runner), for example a recorded step. Outside a metatest it is dropped.
func Emit(line string) {
	if c, ok := gs.get().chooser.(*remoteChooser); ok {
		c.emit(line)
	}
}

func (s *scheduler) choose(kind string, n uint32) uint32 {
	if s.chooser == nil || inControlledNondeterminism() {
		return s.fastrander.fastrandn(n)
	}
	if n <= 1 {
		return 0
	}
	v := s.chooser.Choose(kind, int(n))
	if v < 0 || v >= int(n) {
		panic(fmt.Sprintf("gosim: chooser returned %d for %s/%d", v, kind, n))
	}
	return uint32(v)
}

// The metatest protocol, between a gosim test binary and its runner. During
// a run with Remote set, the binary sends childMsg lines on stdout and reads
// a choiceReply after each Choose; the run ends with a Result.
type childMsg struct {
	Choose  *choiceReq `json:",omitempty"`
	Entropy int        `json:",omitempty"` // bytes wanted (Entropy mode)
	Emit    *string    `json:",omitempty"`
	Result  *runResult `json:",omitempty"`
}

type choiceReq struct {
	Kind string
	N    int
}

type choiceReply struct {
	Value int
	Bytes []byte `json:",omitempty"` // reply to an Entropy request
}

// entropyChunk is how many bytes an Entropy request asks for.
const entropyChunk = 512

type remoteChooser struct {
	mu  sync.Mutex
	in  *json.Decoder
	out *json.Encoder
	// entropy: the runner hands out bytes, and choices other than faults
	// are made from them here, as quinthegel's Hegel chooser makes them (a
	// byte, two above 256 options, modulo n), not one round trip each.
	entropy bool
	buf     []byte
}

func (c *remoteChooser) byte() int {
	if len(c.buf) == 0 {
		if err := c.out.Encode(childMsg{Entropy: entropyChunk}); err != nil {
			panic(err)
		}
		var reply choiceReply
		if err := c.in.Decode(&reply); err != nil {
			panic(err)
		}
		if len(reply.Bytes) == 0 {
			panic("gosim: the runner sent no entropy")
		}
		c.buf = reply.Bytes
	}
	b := c.buf[0]
	c.buf = c.buf[1:]
	return int(b)
}

func (c *remoteChooser) Choose(kind string, n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entropy && !strings.HasPrefix(kind, "fault.") {
		v := c.byte()
		if n > 256 {
			v = v<<8 | c.byte()
		}
		return v % n
	}
	if err := c.out.Encode(childMsg{Choose: &choiceReq{Kind: kind, N: n}}); err != nil {
		panic(err)
	}
	var reply choiceReply
	if err := c.in.Decode(&reply); err != nil {
		panic(err)
	}
	return reply.Value
}

func (c *remoteChooser) emit(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.out.Encode(childMsg{Emit: &line}); err != nil {
		panic(err)
	}
}
