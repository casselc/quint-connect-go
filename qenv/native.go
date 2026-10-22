package qenv

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// The native runtime: the same Sim, its processes and doubles, on plain
// goroutines and the time package (DESIGN.md §15.1). In gosim those are
// simulated, every choice made by the Chooser on the host; on their own
// they are real, for soak tests and demos.
//
// Tasks keep qenv's semantics, one at a time, through a lock that the
// running task holds and gives up at every seam (Yield, Sleep, a wait). A
// wait is a condition variable broadcast whenever a task gives up the lock
// after running, so predicate waits (Wait) and Signals need nothing else.

// A Platform runs each process of a native simulation on a machine of its
// own, so that a crash stops all of the process's goroutines at once and
// none of their deferred code runs (gosim's machines). Without one, the
// tasks of a crashed process stop at their next seam.
type Platform interface {
	// Machine starts a machine running main. Crash stops it, and Restart,
	// after a Crash, runs main again.
	Machine(name string, main func()) Machine
}

// A Machine is a Platform's machine. Crash called from the machine itself
// may return before the machine stops: the stop must happen, in order with
// the Platform's later calls (a Restart), as soon as the caller gives up
// the simulation's lock.
type Machine interface {
	Crash()
	Restart()
}

type native struct {
	mu     sync.Mutex
	cond   *sync.Cond
	plat   Platform
	closed chan struct{}
	nemOn  bool // the nemesis task is running
}

// spawnQueue is how many tasks a process's machine can be handed before it
// runs them: tasks are started on the process's own machine, by its main
// goroutine.
const spawnQueue = 1 << 16

// NewNative returns a simulation on the native runtime, its choices made
// by c and its processes run on p's machines (p may be nil). The calling
// goroutine runs, like a task, until it calls Run or Settle: tasks run
// while it waits there.
//
// Differences from New's scheduler: the schedule is the runtime's (gosim
// asks its Chooser), so "sched.*" choices are not qenv's; a nemesis is
// considered once per NemesisEvery (default 100ms), never per step; Run
// runs for d, without stopping early when the system is quiet; and Settle
// makes fault choices nominal but not the runtime's schedule.
func NewNative(c Chooser, p Platform) *Sim {
	s := New(c)
	n := &native{plat: p, closed: make(chan struct{})}
	n.cond = sync.NewCond(&n.mu)
	s.nat = n
	s.now = time.Now()
	s.start = s.now
	n.mu.Lock()
	return s
}

// Native reports whether s runs on the native runtime.
func (s *Sim) Native() bool { return s.nat != nil }

// release gives up the lock after running: whatever a waiter waits for may
// have changed.
func (n *native) release() {
	n.cond.Broadcast()
	n.mu.Unlock()
}

// resume takes the lock back after a seam. A task of a crashed process
// stops here.
func (t *task) resume() {
	t.sim.nat.mu.Lock()
	if t.killed {
		t.frozen()
	}
}

// frozen holds a killed task, as a crash does, until Close unwinds it.
// Called with the lock held; it returns only by panicking, with it held.
func (t *task) frozen() {
	n := t.sim.nat
	n.mu.Unlock()
	<-n.closed
	n.mu.Lock()
	panic(killed{})
}

func (s *Sim) spawnNative(t *task, fn func(ctx context.Context)) {
	body := func() {
		id := goid()
		own(id, s, t)
		live.Add(1)
		defer func() { disown(id, s); live.Add(-1) }()
		s.nat.mu.Lock()
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(killed); !ok {
					s.panicked, s.stack = r, debug.Stack()
					if s.Log != nil {
						s.Log(fmt.Sprintf("task %s panicked: %v\n%s", t.name, r, s.stack))
					}
				}
			}
			t.done = true
			s.nat.release()
		}()
		if t.killed {
			t.frozen()
		}
		fn(t.ctx)
	}
	if p := t.proc; p != nil && p.queue != nil {
		select {
		case p.queue <- body:
		default:
			panic(fmt.Sprintf("qenv: process %s has %d tasks waiting to start on its machine", p.Name, spawnQueue))
		}
		return
	}
	go body()
}

// machineMain is the main function of a process's machine: it starts the
// tasks handed to the process, on this machine.
func (s *Sim) machineMain(p *Process) func() {
	return func() {
		s.nat.mu.Lock()
		q := p.queue
		s.nat.mu.Unlock()
		for f := range q {
			go f()
		}
	}
}

func (t *task) yieldNative() {
	t.sim.nat.release()
	runtime.Gosched()
	t.resume()
}

func (t *task) sleepNative(d time.Duration) {
	t.sim.nat.release()
	time.Sleep(d)
	t.resume()
}

// waitNative waits until ready holds (nil: no predicate), a Signal ends
// the wait (Notify), or deadline passes (zero: none).
func (t *task) waitNative(ready func() bool, deadline time.Time) {
	n := t.sim.nat
	t.waiting, t.notified = true, false
	gen := t.gen
	var tm *time.Timer
	if !deadline.IsZero() {
		tm = time.AfterFunc(time.Until(deadline), func() {
			n.mu.Lock()
			n.cond.Broadcast()
			n.mu.Unlock()
		})
		defer tm.Stop()
	}
	// This task ran before waiting: wake the others once. A waiter that
	// wakes and is not ready changed nothing, and does not wake them again
	// (they would wake each other forever, and virtual time never pass).
	n.cond.Broadcast()
	for t.waiting && t.gen == gen {
		if ready != nil && ready() {
			break
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			break
		}
		n.cond.Wait()
		if t.killed {
			t.frozen()
		}
	}
	if t.waiting && t.gen == gen {
		t.waiting = false
		t.gen++
	}
}

// runNative lets the tasks run for d.
func (s *Sim) runNative(d time.Duration) error {
	if !s.nat.nemOn && len(s.nemeses) > 0 {
		s.nat.nemOn = true
		every := s.NemesisEvery
		if every <= 0 {
			every = 100 * time.Millisecond
		}
		s.Spawn("qenv.nemesis", func(ctx context.Context) {
			for {
				Sleep(ctx, every)
				if !s.faultsOff {
					s.maybeNemesis()
				}
			}
		})
	}
	s.nat.release()
	time.Sleep(d)
	s.nat.mu.Lock()
	if s.panicked != nil {
		p := s.panicked
		s.panicked = nil
		panic(p)
	}
	return nil
}

// stopSelfNative stops a task that crashed its own process: its machine
// stops it, or it stays frozen until Close.
func (p *Process) stopSelfNative(t *task) {
	if p.machine != nil {
		p.sim.nat.release()
		select {} // the machine's crash stops this goroutine
	}
	t.frozen()
}

func (p *Process) restartNative() {
	if p.machine != nil {
		p.queue = make(chan func(), spawnQueue) // the new incarnation's
		p.machine.Restart()
	}
}

func (s *Sim) closeNative() {
	for _, t := range s.tasks {
		if !t.done {
			t.killed = true
		}
	}
	close(s.nat.closed)
	s.nat.release()
}
