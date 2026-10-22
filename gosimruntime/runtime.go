package gosimruntime

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/jellevandenhooff/gosim/internal/coro"
	"github.com/jellevandenhooff/gosim/internal/race"
)

type constErr struct {
	error
}

func makeConstErr(err error) error {
	return constErr{error: err}
}

var (
	ErrPaniced      = makeConstErr(errors.New("paniced"))
	ErrAborted      = makeConstErr(errors.New("aborted"))
	ErrMainReturned = makeConstErr(errors.New("main returned"))
	ErrTimeout      = makeConstErr(errors.New("timeout"))
	ErrTestFailed   = makeConstErr(errors.New("test failed"))
)

func getEntryFunc(fun func()) string {
	return runtime.FuncForPC(reflect.ValueOf(fun).Pointer()).Name()
}

var aslrBaseAddr = uintptr(reflect.ValueOf(reflect.ValueOf).UnsafePointer())

// relativeFuncAddr returns the address of a function ignoring address space
// layout randomization. This makes function pointers comparable across
// processes to check determinism.
func relativeFuncAddr(fun func()) uintptr {
	return uintptr(reflect.ValueOf(fun).UnsafePointer()) - aslrBaseAddr
}

var (
	insideGosimRun atomic.Bool
	gs             atomicValue[*scheduler]
)

func setgs(newgs *scheduler) func() {
	if !insideGosimRun.CompareAndSwap(false, true) {
		panic("gosimruntime already running; only support one per process")
	}
	gs.set(newgs)
	if newgs.current.get() != nil {
		panic("when setting gs expecting nil current")
	}
	globalPtr.set(nil)

	return func() {
		gs.set(nil)
		globalPtr.set(nil)
		if !insideGosimRun.CompareAndSwap(true, false) {
			panic("gosimruntime already stopped!!!???")
		}
	}
}

func getg() *goroutine {
	return gs.get().current.get()
}

type Machine struct {
	label   string
	globals unsafe.Pointer
}

func newMachine(label string) *Machine {
	return &Machine{
		label:   label,
		globals: allocGlobals(),
	}
}

type scheduler struct {
	seed int64

	goroutines      intrusiveList[*goroutine]
	runnable        intrusiveList[*goroutine]
	nextGoroutineID int

	goroutinesById map[int]*goroutine

	envs []string

	current atomicValue[*goroutine]

	checksummer *checksummer

	fastrander fastrander

	// chooser, if set, makes every bounded choice (choose.go).
	chooser Chooser

	clock *clock

	nextEventID int

	logger logger

	abortErr error

	// race token given to background goroutines so they can read gs fields
	globaltoken RaceToken

	inupcall bool
}

func newScheduler(seed int64, logger logger, checksummer *checksummer, extraenv []string) *scheduler {
	clock := newClock()

	rand := mathrand.New(mathrand.NewSource(seed))

	s := &scheduler{
		seed:            seed,
		goroutines:      make([]*goroutine, 0, 1024),
		runnable:        make([]*goroutine, 0, 1024),
		nextGoroutineID: 1,
		fastrander:      fastrander{state: rand.Uint64()},
		goroutinesById:  make(map[int]*goroutine),
		clock:           clock,
		nextEventID:     1,
		logger:          logger,
		checksummer:     checksummer,
		globaltoken:     MakeRaceToken(),
		envs: append(extraenv,
			"GOSIM_LOG_LEVEL="+logger.level.String(),
		),
	}
	s.globaltoken.Release()

	return s
}

type intrusiveList[A any] []A

type intrusiveIndex struct{ idx int }

func (l *intrusiveList[A]) add(elem A, idxFunc func(elem A) *intrusiveIndex) {
	oldIdx := idxFunc(elem).idx
	if oldIdx != -1 {
		panic(elem)
	}
	idxFunc(elem).idx = len(*l)
	*l = append(*l, elem)
}

func (l *intrusiveList[A]) remove(elem A, idxFunc func(elem A) *intrusiveIndex) {
	oldIdx := idxFunc(elem).idx
	if oldIdx == -1 {
		panic(elem)
	}
	n := len(*l)
	replacement := (*l)[n-1]
	(*l)[oldIdx] = replacement
	idxFunc(replacement).idx = oldIdx
	*l = (*l)[:n-1]
	idxFunc(elem).idx = -1
}

func (s *scheduler) addGoroutine(g *goroutine) {
	s.goroutines.add(g, (*goroutine).allIdxPtr)
	s.goroutinesById[g.ID] = g
	s.addRunnableGoroutine(g)
}

func (s *scheduler) removeGoroutine(g *goroutine) {
	s.goroutines.remove(g, (*goroutine).allIdxPtr)
	delete(s.goroutinesById, g.ID)
	if g.runnableIdx.idx != -1 {
		s.removeRunnableGoroutine(g)
	}
}

func (s *scheduler) addRunnableGoroutine(g *goroutine) {
	s.runnable.add(g, (*goroutine).runnableIdxPtr)
}

func (s *scheduler) removeRunnableGoroutine(g *goroutine) {
	s.runnable.remove(g, (*goroutine).runnableIdxPtr)
}

func (s *scheduler) pickRandomOtherGoroutine(g *goroutine) *goroutine {
	if len(s.goroutines) == 1 {
		return nil
	}
	idx := s.choose("sched.other", uint32(len(s.goroutines)-1))
	if s.goroutines[idx] == g {
		idx++
	}
	return s.goroutines[idx]
}

func (s *scheduler) getNextGoroutineID() int {
	id := s.nextGoroutineID
	s.nextGoroutineID++
	return id
}

func (s *scheduler) Go(fun func(), machine *Machine, token RaceToken) *goroutine {
	id := s.getNextGoroutineID()

	eventID := NextEventID()

	if !token.Empty() {
		// for getEntryFunc
		token.Acquire()
	}
	if s.logger.slog.Enabled(context.TODO(), slog.LevelDebug) {
		s.logger.slog.Log(context.TODO(), slog.LevelDebug, "spawning goroutine", "eventid", eventID, "childgoroutine", id, "entry", getEntryFunc(fun))
	}

	// XXX: log/trace happens-before spawn, happens-after start?
	newg := newGoroutine(id, getg(), eventID, fun, machine, token)
	s.addGoroutine(newg)

	return newg
}

func (s *scheduler) Run() error {
	// TODO: add a wall-clock timeout for running any given step. If the step
	// hangs for too long, it probably is doing something weird (using
	// uninstrumented sync or some spin.) Call out the stuck goroutine as the
	// problem.

	// TODO: move scheduler deterministic seed/state initialization here?

	for len(s.goroutines) > 0 && s.abortErr == nil {
		// XXX: can we make the scheduler itself run in the goroutines? so we
		// don't have to ping-pong another goroutine switch

		for {
			handler, ok := s.clock.maybefire()
			if !ok {
				break
			}
			if handler != nil {
				handler.handler(handler)
				// s.firetimer(handler)
			}
		}

		if len(s.runnable) == 0 && s.clock.anyWaiting() {
			s.clock.doadvance()
			continue
		}

		if len(s.runnable) == 0 {
			buf := make([]byte, 256*1024)
			buf = buf[:runtime.Stack(buf, true)]
			return fmt.Errorf("all goroutines blocked\n\n%s", string(buf))
		}

		pick := s.runnable[s.choose("sched", uint32(len(s.runnable)))]

		if s.checksummer != nil {
			s.checksummer.recordIntInt(checksumKeyRunPick, uint64(pick.ID), uint64(s.fastrander.state))
		}
		s.current.set(pick)
		pick.step()
		s.current.set(nil)
		if s.checksummer != nil {
			var flags uint64
			if pick.finished {
				flags |= 1
			}
			if pick.waiting {
				flags |= 1 << 1
			}
			s.checksummer.recordIntInt(checksumKeyRunResult, flags, s.fastrander.state)
		}
		if pick.finished {
			s.removeGoroutine(pick)
			freeGoroutine(pick)
		}
	}

	// TODO: log exit reason? log that we're killing a bunch of goroutines?

	for _, pick := range s.goroutines {
		pick.abort()
		freeGoroutine(pick)
	}
	s.goroutines = nil

	// TODO: Test abortErr?
	return s.abortErr
}

var (
	globalRuntime      func(func())
	runtimeInitialized bool
)

var benchmarkGlobals = flag.Bool("benchmark-globals", false, "benchmark global initialization cost")

func initializeRuntime(rt func(func())) {
	if runtimeInitialized {
		panic("already initialized")
	}
	runtimeInitialized = true

	globalRuntime = rt
	prepareGlobals()
	if err := initSharedGlobals(); err != nil {
		panic(err)
	}

	if *benchmarkGlobals {
		doBenchmarkGlobals()
	}
}

type internalRunResult struct {
	Seed      int64
	Checksum  []byte
	Failed    bool
	LogOutput []byte
	Err       error
}

func Seed() int64 {
	return gs.get().seed
}

func run(f func(), seed int64, enableChecksum bool, captureLog bool, logLevelOverride string, simLogger io.Writer, extraenv []string) internalRunResult {
	return runWith(f, seed, nil, enableChecksum, captureLog, logLevelOverride, simLogger, extraenv)
}

// runWith is run with a chooser making the choices (choose.go); nil keeps
// the seeded generator.
func runWith(f func(), seed int64, chooser Chooser, enableChecksum bool, captureLog bool, logLevelOverride string, simLogger io.Writer, extraenv []string) internalRunResult {
	if !runtimeInitialized {
		panic("not yet initialized")
	}

	enableChecksum = enableChecksum || *forceChecksum

	logLevelStr := *logLevel
	if logLevelOverride != "" {
		logLevelStr = logLevelOverride
	}
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(logLevelStr)); err != nil {
		panic("bad log-level " + logLevelStr)
	}

	var logOut io.Writer = simLogger
	var logBuffer *bytes.Buffer
	if captureLog {
		logBuffer = &bytes.Buffer{}
		logOut = io.MultiWriter(logBuffer, logOut)
	}

	logger := makeLogger(logOut, logLevel)

	var checksummer *checksummer
	if enableChecksum {
		checksummer = newChecksummer(logger.slog)
		logOut = io.MultiWriter(&checksumWriter{checksummer: checksummer}, logger.out)
		logger = makeLogger(logOut, logLevel)
	}

	scheduler := newScheduler(seed, logger, checksummer, extraenv)
	defer setgs(scheduler)()
	if chooser != nil {
		scheduler.chooser = chooser
		scheduler.fastrander.state = uint64(chooser.Choose("seed", 1<<16))
	}

	InitSteps()

	defaultMachine := newMachine("")

	wrapper := func() {
		InitGlobals(false, false)
		globalRuntime(f)
	}

	scheduler.Go(wrapper, defaultMachine, scheduler.globaltoken)
	if enableChecksum {
		// XXX: add a top-level force-trace flag
		// XXX: this flag is kinda sad... reorg?
		checksummer.recordIntInt(checksumKeyRunStarted, 0, 0)
	}
	runErr := scheduler.Run()
	if runErr == ErrMainReturned {
		runErr = nil
	}
	var checksum []byte
	if enableChecksum {
		checksummer.recordIntInt(checksumKeyRunFinished, 0, 0)
		checksum = checksummer.finalize()
	}
	var capturedLog []byte
	if captureLog {
		capturedLog = logBuffer.Bytes()
	}
	return internalRunResult{
		Seed:      seed,
		Checksum:  checksum,
		Failed:    runErr != nil, // TODO: get rid of Failed or Err?
		LogOutput: capturedLog,
		Err:       runErr,
	}
}

var sharedGlobalsInitialized bool

func initSharedGlobals() error {
	if sharedGlobalsInitialized {
		panic("help already initialized")
	}
	sharedGlobalsInitialized = true

	logger := makeLogger(makeConsoleLogger(os.Stderr), slog.LevelInfo)
	scheduler := newScheduler(0, logger, nil, nil)

	defer setgs(scheduler)()

	defaultMachine := newMachine("")

	scheduler.Go(func() {
		InitGlobals(false, true)
		SetAbortError(ErrMainReturned)
	}, defaultMachine, scheduler.globaltoken)
	if err := scheduler.Run(); err != nil && err != ErrMainReturned {
		return err
	}

	return nil
}

func doBenchmarkGlobals() error {
	logger := makeLogger(makeConsoleLogger(os.Stderr), slog.LevelInfo)
	scheduler := newScheduler(0, logger, nil, nil)

	defer setgs(scheduler)()

	defaultMachine := newMachine("")

	scheduler.Go(func() {
		InitGlobals(true, false)
		SetAbortError(ErrMainReturned)
	}, defaultMachine, scheduler.globaltoken)
	if err := scheduler.Run(); err != nil && err != ErrMainReturned {
		return err
	}

	return nil
}

type corotask int

const (
	corotaskNone corotask = iota
	corotaskAbort
	corotaskStep
	corotaskBacktrace
)

type goroutine struct {
	ID          int
	allIdx      intrusiveIndex // idx in scheduler.goroutines, or -1
	runnableIdx intrusiveIndex // idx in scheduler.runnable, or -1

	spawnEventID int
	machine      *Machine
	parent       *goroutine

	finished bool

	coro coro.Upcallcoro

	corotask corotask

	backtracePc []uintptr
	backtraceN  int

	parksynctoken RaceToken
	shouldacquire bool
	shouldrelease *racesynchelper

	selected *sudog
	waiters  *sudog // linked-list

	// true if called yield(true) and not yet continued
	waiting bool
	// true if explicitly paused
	paused bool

	parkWait           bool
	parkPaniced        bool
	parkPanicTraceback []byte
	parkPanicValue     string

	entryFun   func()
	entryToken RaceToken

	// per-goroutine syscall struct so we don't do allocs
	syscallCache unsafe.Pointer // *Syscall
}

func newGoroutine(id int, parent *goroutine, spawnEventID int, fun func(), machine *Machine, token RaceToken) *goroutine {
	gs := gs.get()

	g := allocGoroutine()
	g.syscallCache = syscallAllocator(id) //  &Syscall{} // XXX: doing this rn because of reused sema
	g.ID = id
	g.parent = parent
	g.spawnEventID = spawnEventID
	g.allIdx.idx = -1
	g.runnableIdx.idx = -1
	g.machine = machine
	g.parksynctoken = MakeRaceToken()

	if gs.checksummer != nil {
		gs.checksummer.recordIntInt(checksumKeyCreating, uint64(g.ID), uint64(relativeFuncAddr(fun))) // XXX: test this pointer
	}

	prev := getg()
	gs.current.set(g)
	g.entryFun = fun
	g.entryToken = token
	race.Disable() // make sure no happens-before to the scheduler
	g.coro.Start(goroutineEntrypoint)
	race.Enable()
	gs.current.set(prev)

	return g
}

var goroutinePool []*goroutine

func allocGoroutine() *goroutine {
	if n := len(goroutinePool); n > 0 {
		g := goroutinePool[n-1]
		goroutinePool = goroutinePool[:n-1]
		return g
	}
	return &goroutine{}
}

// XXX: if ANYONE still refers to this goroutine we will be in a world of
// pain... eg. select or timer of an aborted goroutine???????????
func freeGoroutine(g *goroutine) {
	g.ID = 0
	g.allIdx.idx = -1
	g.runnableIdx.idx = -1
	g.spawnEventID = 0
	g.machine = nil
	g.parent = nil
	g.finished = false
	g.coro = coro.Upcallcoro{}
	g.corotask = corotaskNone
	g.parksynctoken = RaceToken{}
	g.shouldacquire = false
	g.shouldrelease = nil
	g.selected = nil
	g.waiters = nil
	g.waiting = false
	g.paused = false
	g.parkWait = false
	g.parkPaniced = false
	g.parkPanicTraceback = nil
	g.parkPanicValue = ""
	g.entryFun = nil
	g.entryToken = RaceToken{}
	g.syscallCache = nil
	goroutinePool = append(goroutinePool, g)
}

func (g *goroutine) allIdxPtr() *intrusiveIndex {
	return &g.allIdx
}

func (g *goroutine) runnableIdxPtr() *intrusiveIndex {
	return &g.runnableIdx
}

func (g *goroutine) setWaiting(waiting bool) {
	gs := gs.get()
	g.waiting = waiting
	if g.runnableIdx.idx == -1 && !(g.waiting || g.paused) {
		gs.addRunnableGoroutine(g)
	} else if g.runnableIdx.idx != -1 && (g.waiting || g.paused) {
		gs.removeRunnableGoroutine(g)
	}
}

func (g *goroutine) setPaused(paused bool) {
	gs := gs.get()
	g.paused = paused
	if g.runnableIdx.idx == -1 && !(g.waiting || g.paused) {
		gs.addRunnableGoroutine(g)
	} else if g.runnableIdx.idx != -1 && (g.waiting || g.paused) {
		gs.removeRunnableGoroutine(g)
	}
}

// TODO: should this be norace? think about it
//
//go:norace
func (g *goroutine) allocWaiter() *sudog {
	sg := allocSudog()
	sg.goroutine = g
	sg.waitersNext = g.waiters
	g.waiters = sg
	return sg
}

// TODO: should this be norace? think about it
//
//go:norace
func (g *goroutine) releaseWaiters() {
	g.selected = nil
	next := g.waiters
	g.waiters = nil
	for next != nil {
		cur := next
		cur.goroutine = nil
		cur.item = nil
		next = cur.waitersNext
		cur.waitersNext = nil
		freeSudog(cur)
	}
}

const debugUpcall = true

//go:norace
func (g *goroutine) upcall(f func()) {
	race.Disable()
	if debugUpcall {
		if gs.get().inupcall {
			panic("already in upcall")
		}
		gs.get().inupcall = true
	}
	g.coro.Upcall(f)
	if debugUpcall {
		gs.get().inupcall = false
	}
	race.Enable()
}

func (g *goroutine) step() {
	globalPtr.set(g.machine.globals)

	if inControlledNondeterminism() {
		panic("help")
	}

	g.corotask = corotaskStep
	g.coro.Next()
	if g.parkWait {
		g.setWaiting(true)
	}
	if g.parkPaniced {
		if gs.get().abortErr == nil {
			gs.get().abortErr = ErrPaniced
		}
		gs.get().logger.slog.Error("uncaught panic",
			"traceback", strings.Split(strings.ReplaceAll(string(g.parkPanicTraceback), "\t", "  "), "\n"),
			"panic", g.parkPanicValue)
	}
}

// abort stops the goroutine.
// this is a dangerous call, make sure that noone will refer to the goroutine later.
func (g *goroutine) abort() {
	gs := gs.get()

	// XXX: trace?
	if getg() == g {
		panic("aborting self")
	}

	// if currently waiting, unhook from queues. for crash support. queue code
	// better be resilient...
	if g.waiters != nil && g.selected == nil {
		for sg := g.waiters; sg != nil; sg = sg.waitersNext {
			sg.queue.remove(sg)
		}
	}
	g.releaseWaiters()

	// XXX: if g is in any kind of select operation, carefully unwind that??? so
	// far, not necessary because we abort entire machines at a time. (though
	// what about cross-machine selects??? help.)

	prev := getg()
	gs.current.set(g)
	g.corotask = corotaskAbort
	g.coro.Next()
	gs.current.set(prev)

	if !g.finished {
		panic("expected finished")
	}
}

func (g *goroutine) backtrace(pc []uintptr) int {
	if getg() == g {
		panic("backtrace self")
	}

	g.backtracePc = pc
	g.corotask = corotaskBacktrace
	g.coro.Next()
	g.backtracePc = nil

	n := g.backtraceN
	g.backtraceN = 0

	return n
}

// Marked norace to read entryToken and entryFun.
//
//go:norace
func goroutineEntrypoint() {
	g := getg()
	// no need to allocate this entrypoint
	if !g.entryToken.Empty() {
		g.entryToken.Acquire()
	}

	g.park(false)
	defer g.exitpoint()
	g.entryFun()
}

// exitpoint is a helper for entrypoint that stores any exit panic value
// and marks the coroutine as finished.
//
// It cannot be inlined because go:norace does not apply internally.
//
// The g.finished = true must be in a recover call because the goroutine
// might exit by calling runtime.Goexit()
//
//go:norace
func (g *goroutine) exitpoint() {
	if recovered := recover(); recovered != nil {
		g.parkPaniced = true
		traceback := make([]byte, 32*1024)
		traceback = traceback[:runtime.Stack(traceback, false)]
		// XXX: necessary?
		g.parkPanicTraceback = NoraceCopy(traceback)
		g.parkPanicValue = fmt.Sprint(recovered)
	}
	g.finished = true
	g.coro.Finish()
}

//go:norace
func (g *goroutine) park(wait bool) {
	if inControlledNondeterminism() {
		if wait {
			panic("help")
		}
		return
	}

	g.parkWait = wait

	// inform scheduler we're paused
	// wait for us to resume
	for {
		if race.Enabled {
			if wait {
				g.parksynctoken.Release()
			}
			race.Disable()
		}

		g.coro.Yield()

		if race.Enabled {
			race.Enable()
			if wait {
				if g.shouldacquire {
					race.Acquire(unsafe.Pointer(&g.shouldacquire))
					g.shouldacquire = false
				}
				if g.shouldrelease != nil {
					addr := g.shouldrelease
					race.Release(unsafe.Pointer(addr))
					addr.pending = nil
					g.shouldrelease = nil
				}
			}
		}

		switch g.corotask {
		case corotaskAbort:
			g.finished = true
			g.coro.Finish()
		case corotaskStep:
			return
		case corotaskBacktrace:
			g.backtraceN = runtime.Callers(1, g.backtracePc)
		default:
			panic(g.corotask)
		}
		g.corotask = corotaskNone
	}
}

func (g *goroutine) yield() {
	// TODO: optimize by fast-pathing here to stay on the same goroutine before
	// hitting the expensive coroutine switching in park?
	g.park(false)
}

func (g *goroutine) wait() {
	g.park(true)
}

var syscallAllocator func(goroutineId int) unsafe.Pointer

func SetSyscallAllocator(f func(goroutineId int) unsafe.Pointer) {
	syscallAllocator = f
}

//go:norace
func GetGoroutineLocalSyscall() unsafe.Pointer {
	g := getg()
	return g.syscallCache
}

// Go starts a new goroutine.
//
// Marked go:norace to let upcall access g.
//
//go:norace
func Go(fun func()) {
	g := getg()
	g.yield()

	token := MakeRaceToken()
	token.Release()
	g.upcall(func() {
		gs.get().Go(fun, g.machine, token)
	})
}

//go:norace
func GoWithMachine(fun func(), machine *Machine) {
	g := getg()
	g.yield()

	token := MakeRaceToken()
	token.Release()
	g.upcall(func() {
		gs.get().Go(fun, machine, token)
	})
}

//go:norace
func GoFromTimerHandler(fun func(), machine *Machine) {
	if getg() != nil {
		panic("must be called without g")
	}

	gs.get().Go(fun, machine, gs.get().globaltoken)
}

// Marked go:norace to return result.
//
//go:norace
func PickRandomOtherGoroutine() (goroutineID int) {
	var result int
	g := getg()
	g.upcall(func() {
		result = gs.get().pickRandomOtherGoroutine(g).ID
	})
	return result
}

func PauseGoroutine(goroutineID int) {
	getg().upcall(func() {
		gs.get().goroutinesById[goroutineID].setPaused(true)
	})
}

func ResumeGoroutine(goroutineID int) {
	getg().upcall(func() {
		gs.get().goroutinesById[goroutineID].setPaused(false)
	})
}

func StopMachine(machine *Machine) {
	// TODO: janky method, expose different API?
	getg().upcall(func() {
		gs := gs.get()

		// XXX: why are we doing this list copying game? trying to not lose goroutines
		// if they get shuffled...?
		var toAbort []*goroutine
		// XXX: inefficient?
		for _, g := range gs.goroutines {
			if g.machine == machine {
				toAbort = append(toAbort, g)
			}
		}
		for _, g := range toAbort {
			g.abort()
			gs.removeGoroutine(g)
			freeGoroutine(g)
		}
		// unhook timer events
		// XXX: inefficient:
		gs.clock.removemachine(machine)

		// XXX: poison machine for future use? set globals to nil?
		// XXX: could later also reuse globals struct (after zeroing...) (but maybe not in super paranoid mode)
		machine.globals = nil

		// XXX: mark machine as bad for higher-level errors?
	})
}

func IsSim() bool {
	// TODO: test this doesn't race?
	return gs.get() != nil
}

func Yield() {
	getg().yield()
}

func SetFinalizer(obj any, finalizer any) {
	// never do anything, hopefully no problems...

	// TODO: maybe could have deterministic GC if we disable automatic GC and
	// invoke it ourselves?
}

func SetAbortError(err error) {
	if _, ok := err.(constErr); !ok {
		panic("SetAbortErr must be called with one of the predefined errors in this package")
	}

	getg().upcall(func() {
		gs := gs.get()
		if gs.abortErr == nil {
			gs.abortErr = err
		}
	})

	// call park so this goroutine can check abortErr and exit
	getg().park(false)
}

const GOOS = "linux"

// Marked go:norace to read ID
//
//go:norace
func GetGoroutine() int {
	return getg().ID
}

// Marked go:norace to read machine
//
//go:norace
func CurrentMachine() *Machine {
	return getg().machine
}

// Marked go:norace to read and write nextEventID
//
//go:norace
func NextEventID() int {
	gs := gs.get()
	id := gs.nextEventID
	gs.nextEventID++
	return id
}

var logBuf []byte

//go:norace
func WriteLog(b []byte) {
	logBuf = NoraceAppend(logBuf[:0], b...)
	getg().upcall(func() {
		gs.get().logger.out.Write(logBuf)
	})
}

//go:norace
func Envs() []string {
	return NoraceCopy(gs.get().envs)
}

//go:norace
func NewMachine(label string) *Machine {
	var result *Machine
	getg().upcall(func() {
		result = newMachine(label)
	})
	return result
}

var controlledNondeterminismDepth int

func inControlledNondeterminism() bool {
	return controlledNondeterminismDepth > 0
}

// BeginControlledNondeterminism starts a section of code that is expected to
// behave non-deterministically inside the section but that will not show that
// outside the section.
//
// Calls to random in the section always return 0. Any attempt to pause the
// current thread (eg. mutex.Lock) will panic.
//
// Controlled nondeterminism is useful to cache work that can be shared between
// different machines that is expensive to recompute and complicated to
// precompute once before machines start. One use case is the JSON package's
// type cache, see the translate code that inserts calls.
//
//go:norace
func BeginControlledNondeterminism() {
	controlledNondeterminismDepth++
}

//go:norace
func EndControlledNondeterminism() {
	controlledNondeterminismDepth--
}

var globalCounter int

//go:norace
func Nondeterministic() int {
	globalCounter++
	return globalCounter
}

// TODO: expose something like func AssertAllDone() which checks there are no
// other goroutines, no registered timers, ...?

var (
	stepCounter    int
	stepBreakpoint int
)

var stepBreakpointFlag = flag.Int("step-breakpoint", 0, "")

//go:norace
func InitSteps() {
	stepCounter = 0
	stepBreakpoint = *stepBreakpointFlag
}

//go:norace
func Step() int {
	stepCounter++
	if stepCounter == stepBreakpoint {
		runtime.Breakpoint()
	}
	return stepCounter
}

func GetStep() int {
	return stepCounter
}

// GetStacktraceFor captures a stacktrace for any goroutine. It switchers, if
// necessary, to the requested goroutine and captures its stack with
// runtime.Callers.
//
// TODO: is this necessary? maybe we make goroutines capture their stacks
// when doing invoke?
//
//go:norace
func GetStacktraceFor(goroutine int, stack []uintptr) int {
	g := getg()
	if g.ID == goroutine {
		return runtime.Callers(1, stack)
	}

	var n int
	g.upcall(func() {
		gs := gs.get()
		g := gs.goroutinesById[goroutine]
		if g == nil {
			// TODO: return 0 or -1 instead?
			panic("no such goroutine")
		}
		n = g.backtrace(stack)
	})
	return n
}
