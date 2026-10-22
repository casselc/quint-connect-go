// Package profile is a qenv simulation's profile of virtual time: where
// tasks wait, by stack, in pprof's format (qenv.Sim.Profile).
package profile

import (
	"bytes"
	"compress/gzip"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Profile is where a simulation's virtual time goes: each wait at a seam
// (Sleep, Wait, a Signal, a call's response, a lock) is a sample with its
// stack, weighted by the virtual time the task stayed there. A CPU profile
// of a simulation shows the scheduler; this shows the system's latency:
// which call a storm's requests wait on, and from where. Set Sim.Profile
// before Run; write it with WriteTo and read it with `go tool pprof`. It
// is kept by qenv's own scheduler (qenv.New), not the native runtime's.
type Profile struct {
	samples map[string]*sample
}

type sample struct {
	pcs   []uintptr
	task  string
	nanos int64
	count int64
}

// New returns an empty profile.
func New() *Profile { return &Profile{samples: map[string]*sample{}} }

// Waited records a wait of task that starts now, at its caller's caller
// (the seam that waits); the returned function, called when it ends, adds
// the virtual time it took. The simulation calls it.
func (p *Profile) Waited(task string, start time.Time, now func() time.Time) func() {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(3, pcs) // from the seam that waits
	pcs = pcs[:n]
	return func() {
		// a task name's last part is an instance (client.o3, c.send.p2):
		// the profile groups by its kind
		kind := task
		if i := strings.LastIndex(kind, "."); i > 0 {
			kind = kind[:i]
		}
		var key strings.Builder
		key.WriteString(kind)
		for _, pc := range pcs {
			key.WriteByte(' ')
			key.WriteString(strconv.FormatUint(uint64(pc), 16))
		}
		k := key.String()
		smp := p.samples[k]
		if smp == nil {
			smp = &sample{pcs: pcs, task: kind}
			p.samples[k] = smp
		}
		smp.nanos += int64(now().Sub(start))
		smp.count++
	}
}

// Total is the virtual time in all waits recorded.
func (p *Profile) Total() time.Duration {
	var n int64
	for _, s := range p.samples {
		n += s.nanos
	}
	return time.Duration(n)
}

// WriteTo writes the profile in pprof's format (gzipped protobuf), with
// two sample types: the number of waits, and the virtual time waited (the
// default). Each sample is labelled with the kind of task that waited
// (`go tool pprof -tagfocus task=client`).
func (p *Profile) WriteTo(w io.Writer) (int64, error) {
	var e pbuf
	strs := map[string]int64{}
	str := func(s string) int64 {
		if i, ok := strs[s]; ok {
			return i
		}
		strs[s] = int64(len(strs))
		return strs[s]
	}
	str("")
	valueType := func(typ, unit string) []byte {
		var b pbuf
		b.int(1, str(typ))
		b.int(2, str(unit))
		return b.b
	}
	// pprof shows the last sample type by default: the virtual time
	e.bytes(1, valueType("waits", "count"))
	e.bytes(1, valueType("virtual", "nanoseconds"))
	locs := map[uintptr]uint64{}
	funcs := map[string]uint64{}
	var locBufs, funcBufs [][]byte
	loc := func(pc uintptr) uint64 {
		if id, ok := locs[pc]; ok {
			return id
		}
		id := uint64(len(locs) + 1)
		locs[pc] = id
		var lb pbuf
		lb.uint(1, id)
		// a return address: the call is the instruction before it, which
		// CallersFrames finds itself (inlined calls included)
		lb.uint(3, uint64(pc-1))
		frames := runtime.CallersFrames([]uintptr{pc})
		for {
			f, more := frames.Next()
			fid, ok := funcs[f.Function]
			if !ok {
				fid = uint64(len(funcs) + 1)
				funcs[f.Function] = fid
				var fb pbuf
				fb.uint(1, fid)
				fb.int(2, str(f.Function))
				fb.int(3, str(f.Function))
				fb.int(4, str(f.File))
				funcBufs = append(funcBufs, fb.b)
			}
			var line pbuf
			line.uint(1, fid)
			line.int(2, int64(f.Line))
			lb.bytes(4, line.b)
			if !more {
				break
			}
		}
		locBufs = append(locBufs, lb.b)
		return id
	}
	keys := make([]string, 0, len(p.samples))
	for k := range p.samples {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := p.samples[k]
		var sb pbuf
		for _, pc := range s.pcs {
			sb.uint(1, loc(pc))
		}
		sb.int(2, s.count)
		sb.int(2, s.nanos)
		var lab pbuf
		lab.int(1, str("task"))
		lab.int(2, str(s.task))
		sb.bytes(3, lab.b)
		e.bytes(2, sb.b)
	}
	for _, b := range locBufs {
		e.bytes(4, b)
	}
	for _, b := range funcBufs {
		e.bytes(5, b)
	}
	table := make([]string, len(strs))
	for s, i := range strs {
		table[i] = s
	}
	for _, s := range table {
		e.bytes(6, []byte(s))
	}
	e.bytes(11, valueType("virtual", "nanoseconds"))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(e.b); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	n, err := w.Write(gz.Bytes())
	return int64(n), err
}

// pbuf encodes the few protobuf fields a profile needs.
type pbuf struct{ b []byte }

func (p *pbuf) varint(x uint64) {
	for x >= 0x80 {
		p.b = append(p.b, byte(x)|0x80)
		x >>= 7
	}
	p.b = append(p.b, byte(x))
}

func (p *pbuf) uint(field int, x uint64) { p.varint(uint64(field)<<3 | 0); p.varint(x) }
func (p *pbuf) int(field int, x int64)   { p.uint(field, uint64(x)) }
func (p *pbuf) bytes(field int, b []byte) {
	p.varint(uint64(field)<<3 | 2)
	p.varint(uint64(len(b)))
	p.b = append(p.b, b...)
}
