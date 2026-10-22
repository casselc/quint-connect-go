//go:build sim

package qgosim

import (
	"context"

	"github.com/jellevandenhooff/gosim"
	"github.com/jellevandenhooff/gosim/gosimruntime"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Chooser makes qenv's choices with gosim's, and so with the Chooser the
// host passed to Run: Seeded, Hegel, Replay.
type Chooser struct{}

// Choose implements qenv.Chooser.
func (Chooser) Choose(kind string, n int) int { return gosimruntime.Choose(kind, n) }

// emitter hands each line it is written to the host.
type emitter struct{}

func (emitter) Write(p []byte) (int, error) { gosimruntime.Emit(string(p)); return len(p), nil }

// Sink is a qobs sink sending each step to the host, which returns them
// from Run. A step is sent when it is recorded, so a machine's crash loses
// none.
func Sink() qobs.Sink { return qobs.NewJSONLSink(emitter{}) }

// New returns a native qenv simulation in gosim: its choices are the host's
// (Chooser), its processes run on gosim machines (Platform), and its
// context records steps to the host (Sink), so qobs.Record(ctx, ...) from
// its tasks and s.Context() reaches Run.
//
// The recorder is also qobs's global one, on this machine and on every
// machine the Platform boots (gosim gives each machine its own globals):
// code that records with a context of its own (context.Background() in a
// goroutine started with go) records into the run too.
func New() *qenv.Sim {
	rec := qobs.NewRecorder(Sink(), qobs.WithID("sim"))
	qobs.SetGlobal(rec)
	p := NewPlatform()
	p.Recorder = rec
	ctx := qobs.WithRecorder(context.Background(), rec)
	return qenv.NewNative(qenv.WithContext(Chooser{}, ctx), p)
}

// Platform runs each process of a simulation on a gosim machine of its own:
// a crash stops all its goroutines at once, without running deferred code.
type Platform struct {
	// Recorder, if set, is installed as qobs's global recorder on each
	// machine as it boots.
	Recorder *qobs.Recorder
	ops      chan func()
}

// NewPlatform returns a Platform. Its machines are crashed and restarted by
// a goroutine on the calling machine, in the order asked, so that a process
// can crash itself: gosim cannot stop the machine its caller runs on.
func NewPlatform() *Platform {
	p := &Platform{ops: make(chan func(), 1024)}
	// Not a qenv task: it serves the platform, outside any process, and
	// gosim simulates it like any goroutine.
	go func() { //qenv:ok
		for op := range p.ops {
			op()
		}
	}()
	return p
}

type machine struct {
	p *Platform
	m gosim.Machine
}

// Machine implements qenv.Platform.
func (p *Platform) Machine(name string, main func()) qenv.Machine {
	boot := main
	if p.Recorder != nil {
		boot = func() {
			qobs.SetGlobal(p.Recorder)
			main()
		}
	}
	return &machine{p: p, m: gosim.NewMachine(gosim.MachineConfig{Label: name, MainFunc: boot})}
}

func (m *machine) Crash() { m.do(m.m.Crash) }

func (m *machine) Restart() { m.do(m.m.Restart) }

// do runs op on the platform's goroutine, after the ops asked before it,
// and waits for it unless the caller runs on this machine.
func (m *machine) do(op func()) {
	done := make(chan struct{})
	m.p.ops <- func() { op(); close(done) }
	if gosim.CurrentMachine() != m.m {
		<-done
	}
}
