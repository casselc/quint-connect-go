// Package qcover measures which code each model step ran: block coverage
// attributed to steps, so a run's coverage can be read against the model
// (DESIGN.md §17, docs/ROADMAP.md A1).
//
// Code is instrumented by qweave (`qweave -cover go test ./...`): each
// block of the root module's packages calls Hit on entry and the file
// registers its blocks at init. A run collects into its own Counts, put in
// its context (WithCounts; qtest's explorations do whenever blocks are registered),
// found from a block through the goroutine's run (qrun), so runs in
// parallel keep their coverage apart. The blocks
// a task runs between two of its steps are the later step's: steps are
// recorded where they take effect, at the end of the code that made them.
package qcover

import (
	"context"
	"fmt"
	"github.com/casselc/quint-connect-go/qrun"
	"sort"
	"strings"
	"sync"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// A Block is a straight-line run of statements.
type Block struct {
	File      string
	Line, Col int // start
	EndLine   int
	Func      string // the enclosing function, as pkg.Func or pkg.(T).Method
	Stmts     int
	ID        int
}

// Pos is the block's position, file:line.
func (b Block) Pos() string { return fmt.Sprintf("%s:%d", b.File, b.Line) }

var (
	regMu  sync.Mutex
	blocks []Block
)

// Register adds a file's blocks and returns the ID of the first: woven
// code numbers its blocks from it. It is called from package variable
// initialization, before any block runs.
func Register(bs []Block) int {
	regMu.Lock()
	defer regMu.Unlock()
	base := len(blocks)
	for i, b := range bs {
		b.ID = base + i
		blocks = append(blocks, b)
	}
	return base
}

// Blocks are all registered blocks, by ID.
func Blocks() []Block {
	regMu.Lock()
	defer regMu.Unlock()
	return append([]Block(nil), blocks...)
}

// Default collects blocks run outside any simulation, if set: a real
// binary's coverage, or a plain test's.
var Default *Counts

// Hit records that block id ran. Woven code calls it on entry to each
// block.
func Hit(id int) {
	rc, _ := qrun.Current()
	c, _ := qrun.InContext(rc, countsKey)
	task, _ := qrun.InContext(rc, qrun.Task)
	if c == nil {
		c = Default
	}
	if c != nil {
		c.hit(id, task)
	}
}

var countsKey = qrun.NewKey[*Counts]("coverage")

// WithCounts makes a run collect into c: its simulation's tasks (whose
// contexts derive from ctx) and the steps they record.
func WithCounts(ctx context.Context, c *Counts) context.Context {
	return qrun.With(ctx, countsKey, c)
}

// From returns the Counts of ctx or of the goroutine's run, or nil.
func From(ctx context.Context) *Counts {
	c, _ := qrun.Get(ctx, countsKey)
	return c
}

// A StepCover is the blocks one step ran.
type StepCover struct {
	Action, Outcome string
	Blocks          []int
}

// Counts is one run's coverage: blocks hit, and each step's.
type Counts struct {
	mu      sync.Mutex
	hits    map[int]int
	pending map[any]map[int]bool // per task, blocks since its last step
	steps   []StepCover
}

// NewCounts returns an empty collection.
func NewCounts() *Counts { return &Counts{hits: map[int]int{}, pending: map[any]map[int]bool{}} }

func (c *Counts) hit(id int, task any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits[id]++
	p := c.pending[task]
	if p == nil {
		p = map[int]bool{}
		c.pending[task] = p
	}
	p[id] = true
}

// Sink records steps into next and closes each step's blocks: the run's
// recorder writes through it (qobs.NewRecorder(c.Sink(mem))).
func (c *Counts) Sink(next qobs.Sink) qobs.Sink { return stepSink{c, next} }

type stepSink struct {
	c    *Counts
	next qobs.Sink
}

func (s stepSink) Record(ctx context.Context, st *qtrace.Step) {
	task, _ := qrun.Get(ctx, qrun.Task)
	s.c.mu.Lock()
	p := s.c.pending[task]
	delete(s.c.pending, task)
	ids := make([]int, 0, len(p))
	for id := range p {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	s.c.steps = append(s.c.steps, StepCover{Action: st.Action, Outcome: st.Outcome, Blocks: ids})
	s.c.mu.Unlock()
	if s.next != nil {
		s.next.Record(ctx, st)
	}
}

// Hits are the run's block counts.
func (c *Counts) Hits() map[int]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]int, len(c.hits))
	for k, v := range c.hits {
		out[k] = v
	}
	return out
}

// Steps are the run's steps and the blocks each ran, in recording order.
func (c *Counts) Steps() []StepCover {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]StepCover(nil), c.steps...)
}

// Unattributed are blocks run after their task's last step: code no
// recorded step accounts for (a loop's idle polling, cleanup, or work whose
// step is missing).
func (c *Counts) Unattributed() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := map[int]bool{}
	for _, p := range c.pending {
		for id := range p {
			set[id] = true
		}
	}
	return sortedKeys(set)
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// A Summary is the coverage of a set of runs, read against their steps.
type Summary struct {
	Runs    int
	Blocks  []Block                 // every registered block
	Hit     map[int]int             // runs that hit each block
	ByStep  map[string]map[int]bool // "action/outcome" -> blocks its steps ran
	OnlyOff map[int]bool            // blocks hit but never attributed to a step
}

// Summarize merges runs' coverage.
func Summarize(runs []*Counts) *Summary {
	s := &Summary{Runs: len(runs), Blocks: Blocks(), Hit: map[int]int{}, ByStep: map[string]map[int]bool{}, OnlyOff: map[int]bool{}}
	attributed := map[int]bool{}
	for _, c := range runs {
		if c == nil {
			continue
		}
		for id := range c.Hits() {
			s.Hit[id]++
		}
		for _, st := range c.Steps() {
			k := st.Action + "/" + st.Outcome
			if s.ByStep[k] == nil {
				s.ByStep[k] = map[int]bool{}
			}
			for _, id := range st.Blocks {
				s.ByStep[k][id] = true
				attributed[id] = true
			}
		}
	}
	for id := range s.Hit {
		if !attributed[id] {
			s.OnlyOff[id] = true
		}
	}
	return s
}

// Uncovered are the blocks no run hit.
func (s *Summary) Uncovered() []Block {
	var out []Block
	for _, b := range s.Blocks {
		if s.Hit[b.ID] == 0 {
			out = append(out, b)
		}
	}
	return out
}

// Ratio is the share of statements in blocks that some run hit.
func (s *Summary) Ratio() float64 {
	total, hit := 0, 0
	for _, b := range s.Blocks {
		total += b.Stmts
		if s.Hit[b.ID] > 0 {
			hit += b.Stmts
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

// Actions are the steps' "action/outcome" keys that ran a block.
func (s *Summary) Actions(id int) []string {
	var out []string
	for k, set := range s.ByStep {
		if set[id] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Indistinct are pairs of outcomes of one action whose steps ran the same
// blocks: the model tells them apart, the code (as far as these runs
// show) does not.
func (s *Summary) Indistinct() [][2]string {
	keys := make([]string, 0, len(s.ByStep))
	for k := range s.ByStep {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][2]string
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			if strings.SplitN(a, "/", 2)[0] != strings.SplitN(b, "/", 2)[0] {
				continue
			}
			if sameSet(s.ByStep[a], s.ByStep[b]) {
				out = append(out, [2]string{a, b})
			}
		}
	}
	return out
}

func sameSet(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// String reports the coverage: the ratio, the uncovered blocks by
// function, code no step accounts for, and indistinct outcomes.
func (s *Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "code coverage over %d runs: %.1f%% of statements\n", s.Runs, 100*s.Ratio())
	byFunc := map[string][]Block{}
	var funcs []string
	for _, bl := range s.Uncovered() {
		if byFunc[bl.Func] == nil {
			funcs = append(funcs, bl.Func)
		}
		byFunc[bl.Func] = append(byFunc[bl.Func], bl)
	}
	if len(funcs) > 0 {
		b.WriteString("  never run:\n")
		for _, f := range funcs {
			var pos []string
			for _, bl := range byFunc[f] {
				pos = append(pos, fmt.Sprintf("%d", bl.Line))
			}
			fmt.Fprintf(&b, "    %s (%s lines %s)\n", f, byFunc[f][0].File, strings.Join(pos, ", "))
		}
	}
	if len(s.OnlyOff) > 0 {
		// by function: tasks that record no step of their own (clients
		// whose steps the services they call record, relays, setup), or
		// code after a task's last step
		lines := map[string][]string{}
		var funcs []string
		for _, id := range sortedKeys(s.OnlyOff) {
			bl := s.Blocks[id]
			if lines[bl.Func] == nil {
				funcs = append(funcs, bl.Func)
			}
			lines[bl.Func] = append(lines[bl.Func], fmt.Sprint(bl.Line))
		}
		sort.Strings(funcs)
		b.WriteString("  run, but by no step of the task that ran it:\n")
		for _, f := range funcs {
			fmt.Fprintf(&b, "    %s (lines %s)\n", f, strings.Join(lines[f], ", "))
		}
	}
	for _, p := range s.Indistinct() {
		fmt.Fprintf(&b, "  %s and %s ran the same code\n", p[0], p[1])
	}
	return b.String()
}
