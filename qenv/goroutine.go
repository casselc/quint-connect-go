package qenv

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"

	"github.com/casselc/quint-connect-go/qobs"
)

// Which simulation each goroutine belongs to: its tasks' goroutines (qenv
// creates them), and the goroutine that created it (which runs it, and
// records around it). qobs asks for a step whose context carries no
// recorder, so that context.Background() inside a simulation records into
// the simulation's run (qobs.SetFallback). Go has no goroutine-local
// storage; the goroutine id comes from the runtime's stack header, read
// only when a task starts and on that fallback path.
var owners struct {
	sync.Mutex
	m map[int64][]*Sim // a stack: a goroutine may create a simulation inside another's lifetime
}

func init() {
	owners.m = map[int64][]*Sim{}
	qobs.SetFallback(func() (*qobs.Recorder, bool) {
		s := ownerOf(goid())
		if s == nil {
			return nil, false
		}
		r := qobs.InContext(s.base)
		return r, r != nil
	})
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

func own(id int64, s *Sim) {
	owners.Lock()
	owners.m[id] = append(owners.m[id], s)
	owners.Unlock()
}

func disown(id int64, s *Sim) {
	owners.Lock()
	defer owners.Unlock()
	st := owners.m[id]
	for i := len(st) - 1; i >= 0; i-- {
		if st[i] == s {
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

func ownerOf(id int64) *Sim {
	owners.Lock()
	defer owners.Unlock()
	if st := owners.m[id]; len(st) > 0 {
		return st[len(st)-1]
	}
	return nil
}
