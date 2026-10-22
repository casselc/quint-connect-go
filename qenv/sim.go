package qenv

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"github.com/casselc/quint-connect-go/qrun"
	"os"
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
	// Profile, if set, accumulates the virtual time tasks spend waiting at
	// each seam, by stack (Profile.WriteTo: pprof's format).
	Profile *Profile
	// NemesisEvery, if set, makes a nemesis (a crash) a choice once per
	// period of virtual time, at the first step after each period ends,
	// rather than at every scheduling step. A per-step rate means more
	// crashes per second the busier the system: under load, steps per
	// second grow a hundredfold. Set it for load runs.
	NemesisEvery time.Duration
	// Check makes the scheduler verify its own invariants at every step
	// and panic on a violation (also on with QENV_CHECK=1). For tests of
	// qenv and of code that extends it; it costs O(tasks) per step.
	Check bool

	c         Chooser
	base      context.Context // every task's context derives from it
	nextNem   time.Time       // with NemesisEvery: when the next nemesis choice is due
	lastNow   time.Time       // Check: time never goes backwards
	creator   int64           // the goroutine that created it (owners)
	now       time.Time
	start     time.Time
	tasks     []*task   // every task, in creation order (task id-1)
	runq      fenwick   // the queued runnable tasks, by id: k-th in O(log n)
	pred      []*task   // tasks waiting on a predicate (evaluated every step)
	timers    timerHeap // deadlines of waiting tasks
	nextID    int
	back      chan struct{}
	procs     []*Process
	nemeses   []*nemesis
	onCrash   []func(*Process)
	keyed     map[any]any // keyed locks and wait groups (keyed.go)
	onSettle  []func()
	faultsOff bool
	settling  bool
	steps     int
	panicked  any
	stack     []byte
	closed    bool
	nat       *native // the native runtime (native.go), if it runs on one
}

type task struct {
	sim      *Sim
	id       int // creation order: the order runnable tasks are offered in
	name     string
	proc     *Process
	inc      int // the process incarnation it belongs to
	wake     chan struct{}
	ctx      context.Context // its context (Current)
	waiting  bool            // parked until ready, a signal, or deadline
	ready    func() bool     // the predicate waited for, if any
	deadline time.Time       // zero: none
	gen      int             // bumped when a wait ends: stale timers are skipped
	notified bool            // a Signal it waited on fired
	queued   bool            // in runq
	done     bool
	killed   bool
}

type timer struct {
	at  time.Time
	id  int // tie-break: creation order
	t   *task
	gen int
}

type timerHeap []timer

func (h timerHeap) Len() int { return len(h) }
func (h timerHeap) Less(i, j int) bool {
	if !h[i].at.Equal(h[j].at) {
		return h[i].at.Before(h[j].at)
	}
	return h[i].id < h[j].id
}
func (h timerHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *timerHeap) Push(x any)   { *h = append(*h, x.(timer)) }
func (h *timerHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
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
	base := context.Background()
	if cc, ok := As[ctxChooser](c); ok { // through any wrapper
		base = cc.ctx
	}
	s := &Sim{c: c, base: base, now: Epoch, start: Epoch, back: make(chan struct{}), MaxSteps: 100000,
		Check: os.Getenv("QENV_CHECK") == "1", creator: goid()}
	own(s.creator, s, nil)
	live.Add(1)
	return s
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

// Now is the virtual time (on the native runtime, the time package's).
func (s *Sim) Now() time.Time {
	if s.nat != nil {
		return nativeNow()
	}
	return s.now
}

// ToVirtual translates a time read from the real clock into virtual time,
// as if the real clock had advanced with the simulation since. Libraries
// that read the real clock (time.Now in code qenv cannot reach) write their
// timestamps through a seam that translates them with ToVirtual, and read
// them back with ToReal: their time.Since then measures virtual time.
func (s *Sim) ToVirtual(real time.Time) time.Time {
	if s.nat != nil {
		return real // the library's clock is the simulation's
	}
	return s.now.Add(real.Sub(time.Now()))
}

// ToReal translates a virtual time into the real clock's frame (the inverse
// of ToVirtual, as of now).
func (s *Sim) ToReal(virtual time.Time) time.Time {
	if s.nat != nil {
		return virtual
	}
	return time.Now().Add(virtual.Sub(s.now))
}

// Context is the context every task derives from (see WithContext): for
// code that runs outside any task, such as a scenario's final step.
func (s *Sim) Context() context.Context { return s.base }

// Elapsed is the virtual time since the start.
func (s *Sim) Elapsed() time.Duration { return s.Now().Sub(s.start) }

// ---- tasks -------------------------------------------------------------------

type ctxKey struct{}

// taskOf is the simulation task ctx belongs to; for a context that
// carries none (context.Background() in a library, or in code woven by
// qenv/weave), the task running on the calling goroutine. So Sleep, Wait
// and Go called with any context from a task stay in its simulation:
// virtual time, not real.
func taskOf(ctx context.Context) *task {
	if ctx != nil {
		if t, ok := ctx.Value(ctxKey{}).(*task); ok {
			return t
		}
	}
	if live.Load() == 0 {
		return nil
	}
	if o, ok := ownerOf(goid()); ok {
		return o.t
	}
	return nil
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
	s.spawn(s.base, name, nil, fn)
}

func (s *Sim) spawn(parent context.Context, name string, p *Process, fn func(ctx context.Context)) {
	s.nextID++
	t := &task{sim: s, id: s.nextID, name: name, proc: p, wake: make(chan struct{})}
	if p != nil {
		t.inc = p.Incarnation
	}
	s.tasks = append(s.tasks, t)
	ctx := qrun.With(context.WithValue(parent, ctxKey{}, t), qrun.Task, any(t))
	t.ctx = ctx
	if s.nat != nil {
		s.spawnNative(t, fn)
		return
	}
	s.enqueue(t)
	go func() {
		id := goid()
		own(id, s, t)
		defer disown(id, s)
		<-t.wake
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(killed); !ok {
					// re-raised by Run with its own value: a test
					// framework's abort (Hegel's) must reach it intact
					t.sim.panicked, t.sim.stack = r, debug.Stack()
					if t.sim.Log != nil {
						t.sim.Log(fmt.Sprintf("task %s panicked: %v\n%s", t.name, r, t.sim.stack))
					}
				}
			}
			t.done = true
			t.sim.unqueue(t)
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

// park hands control back to the scheduler until the task is picked again.
// A task that is not waiting (Yield) is runnable at once.
func (t *task) park() {
	if t.sim.nat != nil {
		t.yieldNative()
		return
	}
	if !t.waiting {
		t.sim.enqueue(t)
	}
	t.sim.back <- struct{}{}
	<-t.wake
	if t.killed {
		panic(killed{})
	}
}

// wait parks the task until ready holds (nil: no predicate), a Signal
// wakes it, or deadline passes (zero: none).
func (t *task) wait(ready func() bool, deadline time.Time) {
	s := t.sim
	if s.nat != nil {
		t.waitNative(ready, deadline)
		return
	}
	var waited func()
	if s.Profile != nil {
		waited = s.Profile.waited(t.name, s.now, func() time.Time { return s.now })
	}
	t.waiting, t.ready, t.deadline, t.notified = true, ready, deadline, false
	if ready != nil {
		s.pred = append(s.pred, t)
	}
	if !deadline.IsZero() {
		heap.Push(&s.timers, timer{at: deadline, id: t.id, t: t, gen: t.gen})
	}
	t.park() // a killed task does not return: its wait is not counted
	if waited != nil {
		waited()
	}
	t.ready, t.deadline = nil, time.Time{}
}

// enqueue makes a task runnable.
func (s *Sim) enqueue(t *task) {
	if t.waiting {
		t.waiting = false
		t.gen++
	}
	if s.nat != nil {
		if s.nat.coop {
			s.nat.release() // wake it now: the notifier holds no lock
		}
		return // it wakes when the notifier gives up the lock
	}
	if !t.queued && !t.done && !t.killed {
		t.queued = true
		s.runq.add(t.id, 1, s)
	}
}

// unqueue takes a task off the run queue (it runs, finished or was killed).
func (s *Sim) unqueue(t *task) {
	if t.queued {
		t.queued = false
		s.runq.add(t.id, -1, s)
	}
}

// fenwick counts queued task ids, to find the k-th runnable task (in
// creation order) in O(log n) however many tasks are runnable.
type fenwick struct{ tree []int }

func (f *fenwick) add(id, d int, s *Sim) {
	if id >= len(f.tree) {
		f.grow(id, s) // counts the tasks already flagged queued, this one included
		return
	}
	for i := id; i < len(f.tree); i += i & -i {
		f.tree[i] += d
	}
}

// grow rebuilds the tree for ids up to at least id, from the tasks' queued
// flags.
func (f *fenwick) grow(id int, s *Sim) {
	n := 2 * len(f.tree)
	if n <= id {
		n = id + 1
	}
	f.tree = make([]int, n)
	for _, t := range s.tasks {
		if t.queued {
			for i := t.id; i < n; i += i & -i {
				f.tree[i]++
			}
		}
	}
}

// count is the number of queued ids <= id.
func (f *fenwick) count(id int) int {
	if id >= len(f.tree) {
		id = len(f.tree) - 1
	}
	c := 0
	for i := id; i > 0; i -= i & -i {
		c += f.tree[i]
	}
	return c
}

func (f *fenwick) total() int { return f.count(len(f.tree) - 1) }

// kth is the id of the k-th (0-based) queued id.
func (f *fenwick) kth(k int) int {
	pos, step := 0, 1
	for step*2 < len(f.tree) {
		step *= 2
	}
	for ; step > 0; step /= 2 {
		if next := pos + step; next < len(f.tree) && f.tree[next] <= k {
			pos = next
			k -= f.tree[next]
		}
	}
	return pos + 1
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
	t.wait(ready, deadline)
	return ready()
}

// A Signal wakes the tasks waiting on it. Waiting on a Signal costs the
// scheduler nothing until Notify, unlike Wait's predicate, which it
// evaluates at every step: use Signals where many tasks wait (requests
// waiting for responses, workers waiting for work).
type Signal struct {
	waiters []waiter
}

// waiter is a task's wait on a Signal: gen tells it from the task's later
// waits, which a stale entry (the wait timed out) must not end.
type waiter struct {
	t   *task
	gen int
}

// Wait blocks until Notify or the virtual time reaches deadline (zero:
// none). It reports whether Notify woke it.
func (g *Signal) Wait(ctx context.Context, deadline time.Time) bool {
	t := taskOf(ctx)
	if t == nil {
		panic("qenv: Signal.Wait outside a simulation")
	}
	g.waiters = append(g.waiters, waiter{t, t.gen})
	t.wait(nil, deadline)
	return t.notified
}

// Notify wakes every task waiting on the signal.
func (g *Signal) Notify() {
	for _, w := range g.waiters {
		if t := w.t; t.waiting && t.gen == w.gen && !t.done && !t.killed {
			t.notified = true
			t.sim.enqueue(t)
		}
	}
	g.waiters = g.waiters[:0]
}

// Now is the virtual time in a simulation, else the real time.
func Now(ctx context.Context) time.Time {
	if t := taskOf(ctx); t != nil {
		return t.sim.Now()
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
	if t.sim.nat != nil {
		t.sleepNative(d)
		return
	}
	t.wait(nil, t.sim.now.Add(d))
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
	machine     Machine     // native, with a Platform: the process's machine
	queue       chan func() // native, with a Platform: tasks to start there
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
	if s.nat != nil && s.nat.plat != nil {
		p.queue = make(chan func(), spawnQueue)
		p.machine = s.nat.plat.Machine(name, s.machineMain(p))
	}
	s.spawn(s.base, name+".main", p, start)
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
// code of theirs runs while the simulation runs (as in a real crash; they
// unwind at Close, and steps they record then are only Interrupted). A
// task of the process calling Crash stops in it. Components holding state
// for the process (open transactions, unacknowledged deliveries) release
// it through their OnCrash hooks; a supervisor restarts it from one, or
// from another process's task.
func (p *Process) Crash() {
	if p.down {
		return
	}
	p.down = true
	for _, t := range p.sim.tasks {
		if t.proc == p && !t.done {
			t.killed = true
			if p.sim.nat == nil {
				p.sim.unqueue(t)
			}
		}
	}
	if p.sim.Log != nil {
		p.sim.Log(fmt.Sprintf("%v crash %s", p.sim.Elapsed(), p.ID()))
	}
	if p.sim.nat != nil {
		t := taskOf(nil)
		self := t != nil && t.proc == p && t.sim == p.sim
		if p.machine != nil {
			p.machine.Crash() // before the hooks, which may restart it
		}
		for _, h := range p.sim.onCrash {
			h(p)
		}
		if self {
			p.stopSelfNative(t)
		}
		return
	}
	for _, h := range p.sim.onCrash {
		h(p)
	}
	// A task of the process crashing it dies here too: hand control back
	// and stay frozen until Close, like the others, rather than run on to
	// its next scheduling point.
	if t := taskOf(nil); t != nil && t.proc == p && t.sim == p.sim {
		t.sim.back <- struct{}{}
		<-t.wake
		panic(killed{})
	}
}

// Restart boots a crashed process as a new incarnation.
func (p *Process) Restart() {
	if !p.down {
		return
	}
	p.down = false
	p.Incarnation++
	if p.sim.nat != nil {
		p.restartNative()
	}
	p.sim.spawn(p.sim.base, p.Name+".main", p, p.start)
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
// A panic in a task is re-raised here, with its own value (PanicStack has
// the task's stack).
func (s *Sim) Run(d time.Duration) error {
	if s.nat != nil {
		return s.runNative(d)
	}
	horizon := s.now.Add(d)
	for {
		if s.steps >= s.MaxSteps {
			return ErrMaxSteps
		}
		n, cands := s.runnable()
		if n == 0 {
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
		if s.Check {
			s.selfCheck()
		}
		if !s.faultsOff && s.nemesisDue() {
			s.maybeNemesis()
			if n, cands = s.runnable(); n == 0 {
				continue
			}
		}
		t := s.pick(s.Choose("sched.task", n), cands)
		s.dequeue(t)
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

// runnable reports how many tasks are runnable: the queued ones (yielded,
// new, woken by a Signal or their deadline) and the predicate waiters
// whose predicate holds now, which it returns in creation order. A
// predicate waiter is only a candidate: if another task runs first and
// falsifies its predicate, it waits on (a lock taken by another
// transaction stays taken).
func (s *Sim) runnable() (int, []*task) {
	for len(s.timers) > 0 {
		e := s.timers[0]
		if e.t.gen != e.gen || e.t.done || e.t.killed || !e.t.waiting {
			heap.Pop(&s.timers) // stale
			continue
		}
		if s.now.Before(e.at) {
			break
		}
		heap.Pop(&s.timers)
		s.enqueue(e.t)
	}
	var cands []*task
	keep := s.pred[:0]
	for _, t := range s.pred {
		if t.done || t.killed || !t.waiting || t.ready == nil {
			continue
		}
		keep = append(keep, t)
		if !t.queued && t.ready() {
			cands = append(cands, t)
		}
	}
	for i := len(keep); i < len(s.pred); i++ {
		s.pred[i] = nil
	}
	s.pred = keep
	sort.Slice(cands, func(i, j int) bool { return cands[i].id < cands[j].id })
	return s.runq.total() + len(cands), cands
}

// pick returns the k-th runnable task in creation order: queued tasks and
// predicate candidates merged by id.
func (s *Sim) pick(k int, cands []*task) *task {
	for i, c := range cands {
		pos := s.runq.count(c.id-1) + i
		if pos == k {
			return c
		}
		if pos > k {
			return s.tasks[s.runq.kth(k-i)-1]
		}
	}
	return s.tasks[s.runq.kth(k-len(cands))-1]
}

// dequeue takes the task picked to run off the run queue, ending its wait.
func (s *Sim) dequeue(t *task) {
	if t.waiting {
		t.waiting = false
		t.gen++
	}
	s.unqueue(t)
}

func (s *Sim) nextDeadline() (time.Time, bool) {
	for len(s.timers) > 0 {
		e := s.timers[0]
		if e.t.gen != e.gen || e.t.done || e.t.killed || !e.t.waiting {
			heap.Pop(&s.timers)
			continue
		}
		return e.at, true
	}
	return time.Time{}, false
}

// nemesisDue reports whether a nemesis is to be considered at this step:
// at every step, or with NemesisEvery once per period.
func (s *Sim) nemesisDue() bool {
	if s.NemesisEvery <= 0 {
		return true
	}
	if s.nextNem.IsZero() {
		s.nextNem = s.start.Add(s.NemesisEvery)
	}
	if s.now.Before(s.nextNem) {
		return false
	}
	// once per step, however many periods a jump of the clock skipped
	for !s.now.Before(s.nextNem) {
		s.nextNem = s.nextNem.Add(s.NemesisEvery)
	}
	return true
}

// selfCheck verifies the scheduler's invariants (Check).
func (s *Sim) selfCheck() {
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("qenv: scheduler invariant: "+format+"\n%s", append(args, s.Tasks())...))
	}
	queued := 0
	for _, t := range s.tasks {
		if t.queued {
			queued++
			if t.done || t.killed {
				fail("task %s is queued but finished", t.name)
			}
			if t.waiting {
				fail("task %s is both queued and waiting", t.name)
			}
		}
	}
	if total := s.runq.total(); total != queued {
		fail("run queue holds %d tasks, %d are flagged queued", total, queued)
	}
	timers := map[*task]bool{}
	for _, e := range s.timers {
		if e.t.gen == e.gen && e.t.waiting {
			timers[e.t] = true
		}
	}
	for _, t := range s.tasks {
		if t.waiting && !t.done && !t.killed && !t.deadline.IsZero() && !timers[t] {
			fail("task %s waits until %v with no live timer", t.name, t.deadline.Sub(s.start))
		}
		// a task killed by a crash keeps the deadline it was waiting for, and
		// rightly never runs again
		if t.waiting && !t.done && !t.killed && !t.deadline.IsZero() && t.deadline.Before(s.now) && !t.queued {
			fail("task %s is past its deadline (%v) and not runnable", t.name, t.deadline.Sub(s.start))
		}
	}
	if s.now.Before(s.lastNow) {
		fail("time went backwards: %v after %v", s.now.Sub(s.start), s.lastNow.Sub(s.start))
	}
	s.lastNow = s.now
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
	k := 0
	if by := nemesisOf(s.c); by != nil {
		if !s.settling && !s.faultsOff {
			names := make([]string, len(on))
			for i, n := range on {
				names[i] = n.name
			}
			k = by(names)
		}
	} else {
		k = s.Choose("fault.nemesis", len(on)+1)
	}
	if k > 0 {
		if s.Log != nil {
			s.Log(fmt.Sprintf("%v nemesis %s", s.Elapsed(), on[k-1].name))
		}
		on[k-1].do()
	}
}

// PanicStack is the stack of the last task that panicked.
func (s *Sim) PanicStack() string { return string(s.stack) }

// Close unwinds the tasks still parked (their goroutines exit). Call it
// when the run is over; the simulation cannot run afterwards.
func (s *Sim) Close() {
	if s.closed {
		return
	}
	s.closed = true
	defer disown(s.creator, s)
	defer live.Add(-1)
	if s.nat != nil {
		s.closeNative()
		return
	}
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
		if t.waiting {
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
