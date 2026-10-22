// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found at
// https://go.googlesource.com/go/+/refs/heads/master/LICENSE.

package gosimruntime

import (
	"iter"
	"unsafe"

	"github.com/jellevandenhooff/gosim/internal/race"
)

type waitq struct {
	first, last *sudog
}

//go:norace
func (sq *waitq) empty() bool {
	return sq.first == nil
}

//go:norace
func (sq *waitq) enqueue(elem *sudog) {
	elem.queue = sq
	elem.prev = sq.last
	elem.next = nil
	if sq.last != nil {
		sq.last.next = elem
	}
	sq.last = elem
	if sq.first == nil {
		sq.first = elem
	}
}

//go:norace
func (sq *waitq) enqueuelifo(elem *sudog) {
	elem.queue = sq
	elem.prev = nil
	elem.next = sq.first
	if sq.first != nil {
		sq.first.prev = elem
	}
	sq.first = elem
	if sq.last == nil {
		sq.last = elem
	}
}

//go:norace
func (sq *waitq) remove(elem *sudog) {
	if elem.prev != nil {
		elem.prev.next = elem.next
	}
	if elem.next != nil {
		elem.next.prev = elem.prev
	}
	if sq.first == elem {
		sq.first = elem.next
	}
	if sq.last == elem {
		sq.last = elem.prev
	}
	elem.queue = nil
}

//go:norace
func (sq *waitq) dequeue() *sudog {
	if sq.first != nil {
		elem := sq.first

		g := elem.goroutine
		// remove all waiters of this goroutine from their respective queues (so we only trigger
		// one branch in a select)
		for sg := g.waiters; sg != nil; sg = sg.waitersNext {
			sg.queue.remove(sg)
			// TODO: optimize? remove does more work than necessary for the
			// current waiter, since we know sq.first == elem and elem.prev ==
			// nil.
		}

		if g.selected != nil {
			panic("help")
		}
		g.selected = elem
		getg().upcall(func() {
			elem.goroutine.setWaiting(false)
		})
		return elem
	}
	return nil
}

type sudog struct {
	queue *waitq

	prev, next *sudog // linked list in queue

	waitersNext *sudog // linked list in g.waiters

	goroutine *goroutine
	selectIdx int

	success bool

	// TODO: make this a pointer to a selector instead? and then we can maybe skip a copy?
	// TODO: set to nil when done
	item interface{}

	// used instead of item in NotifyList because storing ints in interfaces allocates
	index int
}

var sudogPool []*sudog

//go:norace
func allocSudog() *sudog {
	if n := len(sudogPool); n > 0 {
		sg := sudogPool[n-1]
		sudogPool = sudogPool[:n-1]
		return sg
	}
	return &sudog{}
}

//go:norace
func freeSudog(sg *sudog) {
	sudogPool = append(sudogPool, sg)
}

type chanImpl[V any] struct {
	buf             []V
	race            []racesynchelper
	start, end, len int
	closed          bool
	sendQ           waitq
	recvQ           waitq
}

type Chan[V any] struct {
	impl *chanImpl[V]
}

//go:norace
func NewChan[V any](capacity int) Chan[V] {
	var buf []V
	if capacity != 0 {
		buf = make([]V, capacity)
	}
	var r []racesynchelper
	if race.Enabled {
		r = make([]racesynchelper, max(1, capacity))
	}
	return Chan[V]{
		impl: &chanImpl[V]{
			buf:   buf,
			race:  r,
			start: 0,
			end:   0,
			len:   0,
		},
	}
}

func NilChan[V any]() Chan[V] {
	return Chan[V]{}
}

func ExtractChan[M ~struct{ impl *chanImpl[V] }, V any](m M) Chan[V] {
	return Chan[V](m)
}

func (c Chan[V]) canSend() bool {
	return c.impl.len < len(c.impl.buf) || !c.impl.recvQ.empty() || c.impl.closed
}

//go:norace
func (c Chan[V]) send(v V) {
	if c.impl.closed {
		panic("closed")
	}

	if other := c.impl.recvQ.dequeue(); other != nil {
		if c.impl.buf != nil {
			if race.Enabled {
				racesyncdouble(other.goroutine, &c.impl.race[c.impl.end])
			}
			c.impl.end = (c.impl.end + 1) % len(c.impl.buf)
			c.impl.start = c.impl.end
		} else {
			if race.Enabled {
				racesynccross(other.goroutine, &c.impl.race[0])
			}
		}
		other.item = v
		other.success = true
	} else {
		if race.Enabled {
			racesyncsingle(&c.impl.race[c.impl.end])
		}
		c.impl.buf[c.impl.end] = v
		c.impl.len++
		c.impl.end = (c.impl.end + 1) % len(c.impl.buf)
	}
}

//go:norace
func (c Chan[V]) Send(v V) {
	g := getg()
	if c.impl == nil {
		g.wait()
	}
	g.yield()

	if race.Enabled {
		race.Read(unsafe.Pointer(c.impl))
	}

	if c.canSend() {
		c.send(v)
		return
	}
	sudog := g.allocWaiter()
	sudog.item = v
	c.impl.sendQ.enqueue(sudog)
	g.wait()
	if !sudog.success {
		race.Acquire(unsafe.Pointer(c.impl))
		g.releaseWaiters()
		panic("closed")
	}
	g.releaseWaiters()
}

func (c Chan[V]) completeSend(ok bool) {
	if !ok {
		race.Acquire(unsafe.Pointer(c.impl))
		panic("closed")
	}
}

func ChanCast[V any](v interface{}) V {
	return chanCast[V](v)
}

//go:norace
func (c Chan[V]) close() {
	if c.impl.closed {
		panic("closed")
	}
	c.impl.closed = true

	for !c.impl.sendQ.empty() {
		next := c.impl.sendQ.dequeue()
		next.success = false
	}
	for !c.impl.recvQ.empty() {
		next := c.impl.recvQ.dequeue()
		var v V
		next.item = v
		next.success = false
	}
}

//go:norace
func (c Chan[V]) Close() {
	getg().yield()

	// XXX: race.Write
	race.Write(unsafe.Pointer(c.impl))
	race.Release(unsafe.Pointer(c.impl))

	c.close()
}

//go:norace
func (c Chan[V]) canRecv() bool {
	return c.impl.len > 0 || !c.impl.sendQ.empty() || c.impl.closed
}

func chanCast[V any](v any) V {
	// if we send nil to a iface channel (eg chan error), then casting v.(error) will fail.
	// handle that gracefully
	res, _ := v.(V)
	return res
}

//go:norace
func (c Chan[V]) recvOk() (V, bool) {
	if c.impl.closed && c.impl.len == 0 {
		race.Acquire(unsafe.Pointer(c.impl))
		var v V
		return v, false
	}
	if other := c.impl.sendQ.dequeue(); other != nil {
		if c.impl.buf != nil {
			if race.Enabled {
				racesyncdouble(other.goroutine, &c.impl.race[c.impl.start])
			}
			other.success = true
			v := c.impl.buf[c.impl.start]
			c.impl.buf[c.impl.start] = chanCast[V](other.item)
			c.impl.start = (c.impl.start + 1) % len(c.impl.buf)
			c.impl.end = c.impl.start
			return v, true
		} else {
			if race.Enabled {
				racesynccross(other.goroutine, &c.impl.race[0])
			}
			v := chanCast[V](other.item)
			other.success = true
			return v, true
		}
	} else {
		if race.Enabled {
			racesyncsingle(&c.impl.race[c.impl.start])
		}
		v := c.impl.buf[c.impl.start]
		c.impl.len--
		c.impl.start = (c.impl.start + 1) % len(c.impl.buf)
		return v, true
	}
}

//go:norace
func (c Chan[V]) RecvOk() (V, bool) {
	g := getg()
	if c.impl == nil {
		g.wait()
	}
	g.yield()
	if !c.canRecv() {
		sudog := g.allocWaiter()
		c.impl.recvQ.enqueue(sudog)
		g.wait()
		v := chanCast[V](sudog.item)
		ok := sudog.success
		g.releaseWaiters()
		if !sudog.success {
			// closed, acquire
			race.Acquire(unsafe.Pointer(c.impl))
		}
		return v, ok
	}
	return c.recvOk()
}

func (c Chan[V]) Range() iter.Seq[V] {
	return func(yield func(V) bool) {
		for {
			v, ok := c.RecvOk()
			if !ok {
				break
			}
			if !yield(v) {
				break
			}
		}
	}
}

func (c Chan[V]) completeRecv(ok bool) {
	if !ok {
		// closed, acquire
		race.Acquire(unsafe.Pointer(c.impl))
	}
}

func (c Chan[V]) Recv() V {
	v, _ := c.RecvOk()
	return v
}

//go:norace
func (c Chan[V]) Len() int {
	getg().yield()

	return c.impl.len
}

//go:norace
func (c Chan[V]) Cap() int {
	return len(c.impl.buf)
}

type sendSelector[V any] struct {
	c Chan[V]
	v V // XXX: box already here?
}

//go:norace
func (c sendSelector[V]) enqueue(g *sudog) {
	g.item = c.v
	c.c.impl.sendQ.enqueue(g)
}

func (c sendSelector[V]) exec() (any, bool) {
	c.c.send(c.v)
	return nil, true
}

func (c Chan[V]) SelectSendOrDefault(v V) (int, any, bool) {
	if c.impl == nil {
		return 1, nil, false
	}
	getg().yield()
	if !c.canSend() {
		return 1, nil, false
	}
	c.send(v)
	return 0, nil, true
}

func (c sendSelector[V]) ready() bool {
	return c.c.canSend()
}

func (c sendSelector[V]) complete(ok bool) {
	c.c.completeSend(ok)
}

type recvSelector[V any] struct {
	c Chan[V]
}

func (c recvSelector[V]) enqueue(g *sudog) {
	c.c.impl.recvQ.enqueue(g)
}

func (c recvSelector[V]) exec() (any, bool) {
	v, ok := c.c.recvOk()
	return v, ok
}

func (c Chan[V]) SelectRecvOrDefault() (int, any, bool) {
	if c.impl == nil {
		return 1, nil, false
	}
	getg().yield()
	if !c.canRecv() {
		return 1, nil, false
	}
	v, ok := c.recvOk()
	return 0, v, ok
}

func (c recvSelector[V]) ready() bool {
	return c.c.canRecv()
}

func (c recvSelector[V]) complete(ok bool) {
	c.c.completeRecv(ok)
}

func (c Chan[V]) SendSelector(item V) ChanSelector {
	if c.impl == nil {
		return nilChanSelector{}
	}
	return sendSelector[V]{c: c, v: item}
}

func (c Chan[V]) RecvSelector() ChanSelector {
	if c.impl == nil {
		return nilChanSelector{}
	}
	return recvSelector[V]{c: c}
}

type nilChanSelector struct{}

func (nilChanSelector) enqueue(g *sudog)          {}
func (nilChanSelector) ready() bool               { return false }
func (nilChanSelector) exec() (interface{}, bool) { panic("help") }
func (nilChanSelector) complete(bool)             { panic("help") }

type ChanSelector interface {
	enqueue(g *sudog)
	ready() bool
	exec() (interface{}, bool)
	complete(bool)
}

func DefaultSelector() ChanSelector {
	return nil
}

//go:norace
func Select(selectors ...ChanSelector) (int, any, bool) {
	// TODO: add test for not taking default
	g := getg()
	g.yield()

	if len(selectors) >= 128 {
		panic("bad")
	}
	var readybuf [8]byte
	ready := readybuf[:0]

	defaultIdx := -1
	count := 0
	for i, selector := range selectors {
		if selector == nil {
			defaultIdx = i
		} else if selector != (nilChanSelector{}) {
			count++
			if selector.ready() {
				ready = append(ready, byte(i))
			}
		}
	}
	if len(ready) == 0 && defaultIdx != -1 {
		return defaultIdx, nil, false
	}
	if len(ready) > 0 {
		var idx byte
		if len(ready) == 1 {
			idx = ready[0]
		} else {
			idx = ready[gs.get().choose("select", uint32(len(ready)))]
		}
		v, ok := selectors[idx].exec()
		return int(idx), v, ok
	}

	// add self to queue for every selector
	for i, selector := range selectors {
		if selector == (nilChanSelector{}) {
			continue
		}
		sudog := g.allocWaiter()
		sudog.selectIdx = i
		selector.enqueue(sudog)
	}

	// wait to be selected
	g.wait()
	idx, v, ok := g.selected.selectIdx, g.selected.item, g.selected.success
	g.releaseWaiters()
	selectors[idx].complete(ok)

	// return result
	return idx, v, ok
}

// racesynchelper tracks goroutines that still have to call race.Release and
// race.Acquire on a given channel element.
//
// When a blocked goroutine's channel operation is completed by another
// goroutine, the completing goroutine will call racesync[single/double/cross]
// to race.Acquire and race.Release a channel element for itself, and then
// (ideally) for the blocked goroutine. In the standard go runtime there are
// functions to do that but those are not exposed.  We simulate the end result
// by tracking that the blocked goroutine should have called race.Release and
// race.Acquire.  This requires some care because another goroutine might come
// along want to call race.Acquire and race.Release on the same variable:
//
// 1. A blocked goroutine releases blockedg.waitsyncvar before pausing.
// 2. The unblocking goroutine releases to blockedg.shouldacquire.
// 3. The unblocking goroutine stores the address of the channel element to
// blockedg.shouldrelease
// Scenario A:
// 4. The blocked goroutine acquires blockedg.shouldacquire
// 5. The blocked goroutine releases blockedg.shouldrelease
// Scenario B:
// 4. Another goroutine comes wants to race.Acquire and race.Release. It notices the
// pending blockedg, acquires blockedg.waitsyncvar, and sets
// blockedg.shouldrelease to false.
// 5. The blocked goroutine acquires blockedg.shouldacquire.
//
// In both scenario A and scenario B the same happens-before relationships are
// established.
type racesynchelper struct {
	// pending.shouldrelease = &thisracesynchelper
	pending *goroutine
}

//go:norace
func racesyncsingle(addr *racesynchelper) {
	if g := addr.pending; g != nil {
		g.parksynctoken.Acquire()
		addr.pending = nil
		g.shouldrelease = nil
	}

	race.Acquire(unsafe.Pointer(addr))
	race.Release(unsafe.Pointer(addr))
}

//go:norace
func racesyncdouble(g *goroutine, addr *racesynchelper) {
	if g := addr.pending; g != nil {
		g.parksynctoken.Acquire()
		addr.pending = nil
		g.shouldrelease = nil
	}

	race.Acquire(unsafe.Pointer(addr))
	race.Release(unsafe.Pointer(addr))

	g.shouldacquire = true
	race.Release(unsafe.Pointer(&g.shouldacquire))
	addr.pending = g
	g.shouldrelease = addr
}

//go:norace
func racesynccross(g *goroutine, addr *racesynchelper) {
	g.parksynctoken.Acquire()

	race.Acquire(unsafe.Pointer(addr))
	race.Release(unsafe.Pointer(addr))

	g.shouldacquire = true
	race.Release(unsafe.Pointer(&g.shouldacquire))
}
