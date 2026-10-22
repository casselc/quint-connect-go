package qenv

import (
	"context"
)

// Queue is an at-least-once message queue: a published message is
// delivered until a consumer acknowledges it. Its contract allows, and its
// faults produce:
//   - redelivery: an acknowledgement is lost ("fault.queue.ack"), or the
//     consumer crashed holding the message;
//   - reordering, unless Ordered: which pending message is delivered next
//     is a schedule choice.
//
// Consumers must therefore be idempotent, and must not rely on order
// across messages.
type Queue struct {
	Name string
	// Ordered delivers the oldest pending message first (a single FIFO
	// partition). Redelivered messages go back to the front.
	Ordered bool

	sim     *Sim
	pending []*Delivery
	held    map[*Delivery]*Process
	next    int
	// Published counts messages published, Redelivered redeliveries.
	Published, Redelivered int
}

// Delivery is one delivery of a message.
type Delivery struct {
	ID          int // the message's id: equal across its redeliveries
	Body        any
	Redelivered bool
	q           *Queue
}

// NewQueue returns an empty queue in s.
func NewQueue(s *Sim, name string) *Queue {
	q := &Queue{Name: name, sim: s, held: map[*Delivery]*Process{}}
	s.OnCrash(func(p *Process) {
		// unacknowledged messages of a crashed consumer are redelivered,
		// in the order they were taken
		var back []*Delivery
		for _, d := range q.heldIn() {
			if q.held[d] == p {
				back = append(back, d)
				delete(q.held, d)
			}
		}
		for _, d := range back {
			q.requeue(d)
		}
	})
	return q
}

func (q *Queue) heldIn() []*Delivery {
	var ds []*Delivery
	for d := range q.held {
		ds = append(ds, d)
	}
	// deterministic: by message id
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j].ID < ds[j-1].ID; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
	return ds
}

func (q *Queue) requeue(d *Delivery) {
	q.Redelivered++
	r := &Delivery{ID: d.ID, Body: d.Body, Redelivered: true, q: q}
	q.pending = append([]*Delivery{r}, q.pending...)
}

// Publish enqueues a message.
func (q *Queue) Publish(ctx context.Context, body any) {
	Yield(ctx)
	q.next++
	q.Published++
	q.pending = append(q.pending, &Delivery{ID: q.next, Body: body, q: q})
}

// Receive waits for a message and takes it.
func (q *Queue) Receive(ctx context.Context) *Delivery {
	WaitIdle(ctx, func() bool { return len(q.pending) > 0 })
	i := 0
	if !q.Ordered {
		i = q.sim.Choose("sched.queue."+q.Name, len(q.pending))
	}
	d := q.pending[i]
	q.pending = append(q.pending[:i:i], q.pending[i+1:]...)
	q.held[d] = ProcessOf(ctx)
	return d
}

// queueAck is a lost acknowledgement: the message is delivered again.
var queueAck = DeclareFaults("queue.ack", "lost")

// Ack acknowledges a delivery. The acknowledgement may be lost
// ("fault.queue.ack"): the message is then delivered again.
func (d *Delivery) Ack(ctx context.Context) {
	Yield(ctx)
	q := d.q
	if _, ok := q.held[d]; !ok {
		return
	}
	delete(q.held, d)
	if q.sim.FaultOf(queueAck) == "lost" {
		q.requeue(d)
	}
}

// Nack returns a delivery to the queue for redelivery.
func (d *Delivery) Nack(ctx context.Context) {
	Yield(ctx)
	if _, ok := d.q.held[d]; ok {
		delete(d.q.held, d)
		d.q.requeue(d)
	}
}

// Pending is the number of messages waiting for delivery.
func (q *Queue) Pending() int { return len(q.pending) }

// InFlight is the number of messages taken and not yet acknowledged.
func (q *Queue) InFlight() int { return len(q.held) }
