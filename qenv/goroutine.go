package qenv

import (
	"bytes"
	"context"
	"github.com/casselc/quint-connect-go/qrun"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/casselc/quint-connect-go/qobs"
)

// Which simulation each goroutine belongs to: its tasks' goroutines (qenv
// creates them), and the goroutine that created it (which runs it, and
// records around it). qobs asks for a step whose context carries no
// recorder, so that context.Background() inside a simulation records into
// the simulation's run (qrun.SetResolver). Go has no goroutine-local
// storage; the goroutine id comes from the runtime's stack header, read
// only when a task starts and on that fallback path.
var owners struct {
	sync.Mutex
	m map[int64][]owner // a stack: a goroutine may create a simulation inside another's lifetime
}

// owner is a goroutine's simulation, and its task if it runs one (nil for
// the goroutine that created the simulation).
type owner struct {
	s *Sim
	t *task
}

// live counts the simulations not yet closed: with none, the per-step
// discard check costs an atomic load.
var live atomic.Int64

func init() {
	owners.m = map[int64][]owner{}
	qobs.SetDiscard(func() bool {
		if live.Load() == 0 {
			return false
		}
		o, ok := ownerOf(goid())
		return ok && o.t != nil && o.t.killed
	})
	qrun.SetResolver(func() (context.Context, bool) {
		if live.Load() == 0 {
			return nil, false
		}
		o, ok := ownerOf(goid())
		if !ok {
			return nil, false
		}
		if o.t != nil {
			return o.t.ctx, true
		}
		return o.s.base, true // the goroutine that created the simulation
	})
}

// Current returns the context of the simulation task running on the
// calling goroutine, whatever context the caller has (none: woven code).
// It is how code rewritten at build time (qenv/weave) finds the
// simulation it runs in; false outside any task.
func Current() (context.Context, bool) {
	o, ok := ownerOf(goid())
	if !ok || o.t == nil {
		return nil, false
	}
	return o.t.ctx, true
}

// goid is the calling goroutine's id ("goroutine 123 [running]:").
func goid() int64 {
	var buf [32]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseInt(string(b), 10, 64)
	return id
}

func own(id int64, s *Sim, t *task) {
	owners.Lock()
	owners.m[id] = append(owners.m[id], owner{s, t})
	owners.Unlock()
}

func disown(id int64, s *Sim) {
	owners.Lock()
	defer owners.Unlock()
	st := owners.m[id]
	for i := len(st) - 1; i >= 0; i-- {
		if st[i].s == s {
			st = append(st[:i], st[i+1:]...)
			break
		}
	}
	if len(st) == 0 {
		delete(owners.m, id)
	} else {
		owners.m[id] = st
	}
}

func ownerOf(id int64) (owner, bool) {
	owners.Lock()
	defer owners.Unlock()
	if st := owners.m[id]; len(st) > 0 {
		return st[len(st)-1], true
	}
	return owner{}, false
}
