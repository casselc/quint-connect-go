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
// A wait is a condition variable, broadcast at every seam (Yield, Sleep, a
// wait, Notify, a task's end), so predicate waits (Wait) and Signals need
// nothing else. Tasks keep qenv's semantics, one at a time between seams:
//   - with a Platform, the runtime's (gosim runs one goroutine at a time
//     and switches only where one blocks), so the code under test may take
//     its own locks. qenv takes none there: waits are a sync.Cond with no
//     lock. A real lock shared across machines can be lost to a crash (a
//     mutex handed to a waiter whose machine then stops stays locked), and
//     a broadcast must be one step: a loop of channel sends switches
//     goroutines at each send, and a waiter that re-registers meanwhile is
//     forgotten;
//   - without, on the wall clock, through that lock, which the running task
//     holds and gives up at every seam. Code under test that blocks on its
//     own locks or channels can deadlock there: run it with a Platform.

// A Platform runs each process of a native simulation on a machine of its
// own, so that a crash stops all of the process's goroutines at once and
// none of their deferred code runs (gosim's machines). Without one, the
// tasks of a crashed process stop at their next seam. A Platform's runtime
// must run one goroutine at a time, switching only where one blocks.
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
	coop   bool // a Platform's cooperative runtime: no lock, ccond below
	// ccond is the waits' condition on a cooperative runtime (nopLocker):
	// Broadcast wakes them all in one step of the runtime
	ccond *sync.Cond
	// running are the tasks' goroutines (without a Platform): Close waits
	// for them, so none outlives the simulation (a testing/synctest bubble
	// fails if one does)
	running sync.WaitGroup
}

// nopLocker is the Locker of ccond: a cooperative runtime runs one
// goroutine at a time, so a readiness check and the Wait after it are one
// step without a lock.
type nopLocker struct{}

func (nopLocker) Lock()   {}
func (nopLocker) Unlock() {}

// broadcast wakes every parked wait (cooperative).
func (n *native) broadcast() { n.ccond.Broadcast() }

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
	n := &native{plat: p, closed: make(chan struct{}), coop: p != nil}
	n.cond = sync.NewCond(&n.mu)
	n.ccond = sync.NewCond(nopLocker{})
	s.nat = n
	s.now = nativeNow()
	s.start = s.now
	if !n.coop {
		n.mu.Lock()
	}
	return s
}

// nativeNow is the time on the native runtime, without a monotonic
// reading: times read on different gosim machines compare by their wall
// clock, which is gosim's one clock. A monotonic reading is relative to an
// origin in the time package's globals, which are per machine there, so
// deadlines passed between machines would compare wrongly.
func nativeNow() time.Time { return time.Now().Round(0) }

// Native reports whether s runs on the native runtime.
func (s *Sim) Native() bool { return s.nat != nil }

// release is a seam: whatever a waiter waits for may have changed. Without
// a Platform it gives up the lock the running task holds.
func (n *native) release() {
	if n.coop {
		n.broadcast()
		return
	}
	n.cond.Broadcast()
	n.mu.Unlock()
}

// resume takes the lock back after a seam (without a Platform). A task of a
// crashed process stops here.
func (t *task) resume() {
	if !t.sim.nat.coop {
		t.sim.nat.mu.Lock()
	}
	if t.killed {
		t.frozen()
	}
}

// frozen holds a killed task, as a crash does, until Close unwinds it. It
// returns only by panicking, with the lock as it found it.
func (t *task) frozen() {
	n := t.sim.nat
	if !n.coop {
		n.mu.Unlock()
	}
	<-n.closed
	if !n.coop {
		n.mu.Lock()
	}
	panic(killed{})
}

func (s *Sim) spawnNative(t *task, fn func(ctx context.Context)) {
	if !s.nat.coop {
		s.nat.running.Add(1)
	}
	body := func() {
		if !s.nat.coop {
			defer s.nat.running.Done()
		}
		id := goid()
		own(id, s, t)
		live.Add(1)
		defer func() { disown(id, s); live.Add(-1) }()
		if !s.nat.coop {
			s.nat.mu.Lock()
		}
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
		q := p.queue // the incarnation it boots
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
	n := t.sim.nat
	n.release()
	if n.coop {
		time.Sleep(d)
	} else {
		// a sleep ends at Close too, which waits for every task
		tm := time.NewTimer(d)
		select {
		case <-tm.C:
		case <-n.closed:
			tm.Stop()
		}
	}
	t.resume()
}

// waitNative waits until ready holds (nil: no predicate), a Signal ends
// the wait (Notify), or deadline passes (zero: none).
func (t *task) waitNative(ready func() bool, deadline time.Time) {
	n := t.sim.nat
	if n.coop {
		t.waitCoop(ready, deadline)
		return
	}
	t.waiting, t.notified = true, false
	gen := t.gen
	var tm *time.Timer
	if !deadline.IsZero() {
		tm = time.AfterFunc(deadline.Sub(nativeNow()), func() {
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
		if !deadline.IsZero() && !nativeNow().Before(deadline) {
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

// waitCoop is waitNative on a cooperative runtime: the task waits on
// ccond until a broadcast, its deadline, or a crash.
func (t *task) waitCoop(ready func() bool, deadline time.Time) {
	n := t.sim.nat
	t.waiting, t.notified = true, false
	gen := t.gen
	if !deadline.IsZero() {
		tm := time.AfterFunc(deadline.Sub(nativeNow()), n.broadcast)
		defer tm.Stop()
	}
	n.broadcast() // this task ran before waiting (as in waitNative)
	for t.waiting && t.gen == gen {
		if ready != nil && ready() {
			break
		}
		if !deadline.IsZero() && !nativeNow().Before(deadline) {
			break
		}
		n.ccond.Wait()
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
	if !s.nat.coop {
		s.nat.mu.Lock()
	}
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
	if !s.nat.coop {
		s.nat.running.Wait()
	}
}
