package qenv

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// Sim runs tasks one at a time. A task runs until it reaches a seam (Yield,
// Wait, Sleep, or an operation of a simulated component); there the
// scheduler picks, with a schedule choice, which runnable task goes next.
// Code between seams is atomic, so a run is a sequence of choices, and the
// same choices give the same run.
//
// Time is virtual: it stands still while any task can run and jumps to the
// next deadline when none can.
//
// Code under test spawns goroutines with Go (not the go statement) and
// reads time with Now and Sleep, so they stay inside the simulation. Real
// goroutines, real time and map iteration order are outside it: a run that
// depends on them is not reproducible (Replay detects that).
type Sim struct {
	// MaxSteps bounds a run's scheduling steps (default 100000): a
	// livelock is reported, not looped.
	MaxSteps int
	// Log, if set, receives one line per fault and crash.
	Log func(string)

	c         Chooser
	now       time.Time
	start     time.Time
	tasks     []*task
	back      chan struct{}
	procs     []*Process
	nemeses   []*nemesis
	onCrash   []func(*Process)
	onSettle  []func()
	faultsOff bool
	settling  bool
	steps     int
	panicked  any
	closed    bool
}

type task struct {
	sim      *Sim
	name     string
	proc     *Process
	inc      int // the process incarnation it belongs to
	wake     chan struct{}
	ready    func() bool // nil: runnable
	deadline time.Time   // zero: none
	started  bool
	done     bool
	killed   bool
}

type nemesis struct {
	name    string
	enabled func() bool
	do      func()
}

// Epoch is the virtual time a simulation starts at.
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// New returns a simulation whose choices c makes.
func New(c Chooser) *Sim {
	return &Sim{c: c, now: Epoch, start: Epoch, back: make(chan struct{}), MaxSteps: 100000}
}

// Choose makes a choice of the environment. While the simulation settles,
// fault choices are nominal.
func (s *Sim) Choose(kind string, n int) int {
	if n <= 1 || s.settling || (s.faultsOff && IsFault(kind)) {
		return 0
	}
	v := s.c.Choose(kind, n)
	if v < 0 || v >= n {
		panic(fmt.Sprintf("qenv: chooser returned %d for %s with %d options", v, kind, n))
	}
	if v != 0 && IsFault(kind) && s.Log != nil {
		s.Log(fmt.Sprintf("%v %s=%d", s.Elapsed(), kind, v))
	}
	return v
}

// Fault makes a fault choice: whether (and which) fault of a kind happens
// here. 0 is nominal.
func (s *Sim) Fault(kind string, n int) int { return s.Choose("fault."+kind, n) }

// Now is the virtual time.
func (s *Sim) Now() time.Time { return s.now }

// Elapsed is the virtual time since the start.
func (s *Sim) Elapsed() time.Duration { return s.now.Sub(s.start) }

// ---- tasks -------------------------------------------------------------------

type ctxKey struct{}

func taskOf(ctx context.Context) *task {
	t, _ := ctx.Value(ctxKey{}).(*task)
	return t
}

// From returns the simulation ctx runs in, or nil outside one.
func From(ctx context.Context) *Sim {
	if t := taskOf(ctx); t != nil {
		return t.sim
	}
	return nil
}

// Go starts fn as a task of the simulation, or, outside one, as a
// goroutine. A task started from another task belongs to the same process
// and dies with it.
func Go(ctx context.Context, name string, fn func(ctx context.Context)) {
	t := taskOf(ctx)
	if t == nil {
		go fn(ctx)
		return
	}
	t.sim.spawn(ctx, name, t.proc, fn)
}

// Spawn starts a task outside any process (a client, a test driver).
func (s *Sim) Spawn(name string, fn func(ctx context.Context)) {
	s.spawn(context.Background(), name, nil, fn)
}

func (s *Sim) spawn(parent context.Context, name string, p *Process, fn func(ctx context.Context)) {
	t := &task{sim: s, name: name, proc: p, wake: make(chan struct{})}
	if p != nil {
		t.inc = p.Incarnation
	}
	s.tasks = append(s.tasks, t)
	ctx := context.WithValue(parent, ctxKey{}, t)
	go func() {
		<-t.wake
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(killed); !ok {
					t.sim.panicked = fmt.Sprintf("task %s: %v\n%s", t.name, r, debug.Stack())
				}
			}
			t.done = true
			t.sim.back <- struct{}{}
		}()
		if t.killed {
			panic(killed{})
		}
		fn(ctx)
	}()
}

type killed struct{}

// Yield lets the scheduler run another task. Seams call it; code outside a
// simulation is unaffected.
func Yield(ctx context.Context) {
	if t := taskOf(ctx); t != nil {
		t.park()
	}
}

func (t *task) park() {
	t.sim.back <- struct{}{}
	<-t.wake
	if t.killed {
		panic(killed{})
	}
}

// Wait blocks the task until ready holds. ready is evaluated by the
// scheduler, between tasks, and must only read simulation state.
func Wait(ctx context.Context, ready func() bool) {
	WaitUntil(ctx, ready, time.Time{})
}

// WaitUntil blocks until ready holds or the virtual time reaches deadline
// (zero: no deadline). It reports whether ready held.
func WaitUntil(ctx context.Context, ready func() bool, deadline time.Time) bool {
	t := taskOf(ctx)
	if t == nil {
		panic("qenv: Wait outside a simulation")
	}
	if ready() {
		return true
	}
	t.ready, t.deadline = ready, deadline
	t.park()
	t.ready, t.deadline = nil, time.Time{}
	return ready()
}

// Now is the virtual time in a simulation, else the real time.
func Now(ctx context.Context) time.Time {
	if t := taskOf(ctx); t != nil {
		return t.sim.now
	}
	return time.Now()
}

// Sleep waits d of virtual time in a simulation, else of real time (or
// until ctx is done).
func Sleep(ctx context.Context, d time.Duration) {
	t := taskOf(ctx)
	if t == nil {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
		return
	}
	WaitUntil(ctx, func() bool { return false }, t.sim.now.Add(d))
}

// ---- processes ---------------------------------------------------------------

// A Process is a group of tasks that crash together. Its start function
// is its main task, run on boot and again after every restart, with
// Incarnation one higher: what a restarted process remembers is what start
// rebuilds from durable components (a Store), nothing else.
type Process struct {
	Name        string
	Incarnation int
	sim         *Sim
	start       func(ctx context.Context)
	down        bool
}

// ID is the process name and incarnation, e.g. "payments#2".
func (p *Process) ID() string { return fmt.Sprintf("%s#%d", p.Name, p.Incarnation) }

// Down reports whether the process is crashed and not yet restarted.
func (p *Process) Down() bool { return p.down }

// Process creates and boots a process. If crashable, the scheduler may
// crash it at any step (fault "fault.crash.<name>"), and restart it after
// the crash.
func (s *Sim) Process(name string, crashable bool, start func(ctx context.Context)) *Process {
	p := &Process{Name: name, Incarnation: 1, sim: s, start: start}
	s.procs = append(s.procs, p)
	s.spawn(context.Background(), name+".main", p, start)
	if crashable {
		s.Nemesis("crash."+name, func() bool { return !p.down }, func() { p.Crash(); p.Restart() })
	}
	return p
}

// ProcessOf returns the process the task ctx runs in, or nil.
func ProcessOf(ctx context.Context) *Process {
	if t := taskOf(ctx); t != nil {
		return t.proc
	}
	return nil
}

// Crash kills every task of the process: none runs again, and no deferred
// code of theirs runs (as in a real crash). Components holding state for
// the process (open transactions, unacknowledged deliveries) release it
// through their OnCrash hooks.
func (p *Process) Crash() {
	if p.down {
		return
	}
	p.down = true
	for _, t := range p.sim.tasks {
		if t.proc == p && !t.done {
			t.killed = true
		}
	}
	if p.sim.Log != nil {
		p.sim.Log(fmt.Sprintf("%v crash %s", p.sim.Elapsed(), p.ID()))
	}
	for _, h := range p.sim.onCrash {
		h(p)
	}
}

// Restart boots a crashed process as a new incarnation.
func (p *Process) Restart() {
	if !p.down {
		return
	}
	p.down = false
	p.Incarnation++
	p.sim.spawn(context.Background(), p.Name+".main", p, p.start)
}

// OnCrash registers a hook run when a process crashes.
func (s *Sim) OnCrash(h func(*Process)) { s.onCrash = append(s.onCrash, h) }

// Nemesis registers a fault the scheduler may inject at any step while
// enabled holds (a crash, a partition). The choice is "fault.nemesis",
// among the enabled ones; its rate is that kind's.
func (s *Sim) Nemesis(name string, enabled func() bool, do func()) {
	s.nemeses = append(s.nemeses, &nemesis{name: name, enabled: enabled, do: do})
}

// OnSettle registers a hook run when the simulation settles: a fault
// that persists (a partition) is healed there.
func (s *Sim) OnSettle(h func()) { s.onSettle = append(s.onSettle, h) }

// ---- running -----------------------------------------------------------------

// ErrMaxSteps is returned by Run when a run takes more than MaxSteps steps.
var ErrMaxSteps = errors.New("qenv: too many scheduling steps (livelock?)")

// Run schedules tasks until none can run and no deadline is within d of
// virtual time (the system is quiet), or the virtual time has advanced by
// d. Tasks still waiting then (workers waiting for messages) stay parked.
// A panic in a task is re-raised here, with the task's stack.
func (s *Sim) Run(d time.Duration) error {
	horizon := s.now.Add(d)
	for {
		if s.steps >= s.MaxSteps {
			return ErrMaxSteps
		}
		run := s.runnable()
		if len(run) == 0 {
			next, ok := s.nextDeadline()
			if !ok || next.After(horizon) {
				if s.now.Before(horizon) && ok {
					s.now = horizon
				}
				return nil
			}
			s.now = next
			continue
		}
		s.steps++
		if !s.faultsOff {
			s.maybeNemesis()
			if run = s.runnable(); len(run) == 0 {
				continue
			}
		}
		t := run[s.Choose("sched.task", len(run))]
		t.ready, t.deadline = nil, time.Time{}
		t.started = true
		t.wake <- struct{}{}
		<-s.back
		if s.panicked != nil {
			p := s.panicked
			s.panicked = nil
			panic(p)
		}
	}
}

// Settle turns faults off, runs the OnSettle hooks (healing what
// persists), and runs until quiet or d has passed: the system's chance to
// converge, after which properties conditioned on quiescence apply. It
// runs the canonical schedule (every choice 0): what a run explores is
// what happened before it, and a settle draws nothing from the Chooser.
func (s *Sim) Settle(d time.Duration) error {
	s.faultsOff, s.settling = true, true
	defer func() { s.settling = false }()
	for _, h := range s.onSettle {
		h()
	}
	return s.Run(d)
}

// Faults turns fault choices off or back on (Settle turns them off).
func (s *Sim) Faults(on bool) { s.faultsOff = !on }

func (s *Sim) runnable() []*task {
	var out []*task
	for _, t := range s.tasks {
		if t.done || t.killed {
			continue
		}
		if !t.deadline.IsZero() && !s.now.Before(t.deadline) {
			out = append(out, t)
			continue
		}
		if t.ready == nil || t.ready() {
			out = append(out, t)
		}
	}
	return out
}

func (s *Sim) nextDeadline() (time.Time, bool) {
	var next time.Time
	for _, t := range s.tasks {
		if t.done || t.killed || t.deadline.IsZero() {
			continue
		}
		if next.IsZero() || t.deadline.Before(next) {
			next = t.deadline
		}
	}
	return next, !next.IsZero()
}

func (s *Sim) maybeNemesis() {
	var on []*nemesis
	for _, n := range s.nemeses {
		if n.enabled() {
			on = append(on, n)
		}
	}
	if len(on) == 0 {
		return
	}
	if k := s.Choose("fault.nemesis", len(on)+1); k > 0 {
		if s.Log != nil {
			s.Log(fmt.Sprintf("%v nemesis %s", s.Elapsed(), on[k-1].name))
		}
		on[k-1].do()
	}
}

// Close unwinds the tasks still parked (their goroutines exit). Call it
// when the run is over; the simulation cannot run afterwards.
func (s *Sim) Close() {
	if s.closed {
		return
	}
	s.closed = true
	for _, t := range s.tasks {
		if !t.done {
			t.killed = true
			t.wake <- struct{}{}
			<-s.back
		}
	}
	s.panicked = nil
}

// Tasks lists the tasks that have not finished, with their state, for
// debugging a run that did not quiesce.
func (s *Sim) Tasks() string {
	var lines []string
	for _, t := range s.tasks {
		if t.done || t.killed {
			continue
		}
		st := "runnable"
		if t.ready != nil {
			st = "waiting"
		}
		if !t.deadline.IsZero() {
			st += " until " + t.deadline.Sub(s.start).String()
		}
		lines = append(lines, t.name+": "+st)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
