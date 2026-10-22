// Package shop is a small checkout saga: orders reserve stock, a payment
// provider captures the money, a warehouse ships, and customers may cancel
// until shipping.
//
// Services and what they own:
//   - api: PlaceOrder and Cancel, in transactions on the shop's DB. Each
//     writes its events to an outbox table in the same transaction.
//     Shipping and cancelling both update the order row: under snapshot
//     isolation that write conflict, not the row lock, is what stops a
//     shipment and a cancellation from both committing.
//   - relay: publishes the outbox to the broker, then deletes it.
//   - payments: on "placed", captures with the idempotency key cap:<order>
//     and marks the order paid. On "cancelled", voids cap:<order> at the
//     provider, which refunds a charge and rejects any later capture.
//   - shipping: on "paid", ships unless cancelled.
//
// Everything outside the process (the DB, the broker, the provider) is
// reached through the interfaces below. internal/sim implements them with
// qenv components, whose contracts allow what production's do (lost
// responses, redelivery, transactions at a chosen isolation level).
//
// Model steps (../model/checkout.qnt) are recorded with Txn.OnCommit: at
// the commit where they take effect.
package shop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// NoLocks reads rows with plain SELECTs instead of SELECT ... FOR
	// UPDATE. Correct under Serializable and SnapshotIsolation (their
	// write-conflict checks abort one of two racing updates); under
	// ReadCommitted the last writer wins: stock is oversold, and a
	// shipment and a cancellation both commit (lost updates).
	NoLocks
	// LockWithoutWrite locks the order row but records shipments and
	// cancellations only in their own tables. Correct under Serializable
	// and ReadCommitted (after the lock, reads see the latest commit);
	// under SnapshotIsolation the second transaction still reads the
	// other table from its snapshot, and both commit (write skew).
	LockWithoutWrite
	// NoIdempotencyKey captures with a fresh key on every attempt: a retry
	// after a lost response charges twice.
	NoIdempotencyKey
	// DualWrite publishes "placed" straight to the broker, in the
	// background after the commit, instead of through the outbox: a crash
	// before it goes out loses the event, and the order is never paid or
	// shipped.
	DualWrite
)

// Event is a message between services.
type Event struct {
	Type  string // placed, cancelled, paid
	Order string
}

// Broker publishes events. Delivery is at least once.
type Broker interface {
	Publish(ctx context.Context, ev Event)
}

// Inbox delivers a service's events, at least once and in any order.
type Inbox interface {
	Receive(ctx context.Context) (ev Event, ack, nack func(context.Context))
}

// ErrVoided is returned by Capture for a key whose payment was voided.
var ErrVoided = errors.New("payment voided")

// Payments is the payment provider's API. Calls may time out after taking
// effect; keys make them safe to retry.
type Payments interface {
	Capture(ctx context.Context, key, order string, amount int) error
	// Void refunds the capture with this key if there was one, and rejects
	// any later capture with it.
	Void(ctx context.Context, key, order string) error
}

// Order is a row of the orders table.
type Order struct {
	ID        string
	SKU       string
	Qty       int
	Paid      bool
	Shipped   bool
	Cancelled bool
}

// Shop is the application. Its fields are its dependencies.
type Shop struct {
	DB       qenv.DB
	Broker   Broker
	Payments Payments
	Mutation Mutation
	// PollInterval of the outbox relay (default 100ms).
	PollInterval time.Duration
	// PaymentRetry decides the retries of provider calls (nil: 4 attempts,
	// 200 ms apart). examples/retrystorm's retry.Policy is one.
	PaymentRetry Retrier
	// PaymentWorkers is the number of concurrent payment consumers
	// (default 1).
	PaymentWorkers int
	// RedeliveryDelay is how long a consumer waits before returning a
	// message it failed to process (0: at once, and the broker redelivers
	// it immediately).
	RedeliveryDelay time.Duration
}

// Retrier is a retry policy (the same interface as qload.Retrier).
type Retrier interface {
	Request(now time.Time)
	Retry(n int, now time.Time) (wait time.Duration, ok bool)
}

var (
	ErrSoldOut   = errors.New("sold out")
	ErrNoOrder   = errors.New("no such order")
	ErrShipped   = errors.New("already shipped")
	errTransient = errors.New("transient failure; retry later")
)

func (s *Shop) read(tx qenv.Txn, key string) (any, bool, error) {
	if s.Mutation == NoLocks {
		v, ok := tx.Get(key)
		return v, ok, nil
	}
	return tx.GetForUpdate(key)
}

func (s *Shop) emit(tx qenv.Txn, ev Event) {
	if s.Mutation == DualWrite && ev.Type == "placed" {
		return // published after the commit, by PlaceOrder
	}
	tx.Put(fmt.Sprintf("outbox/%s/%s", ev.Order, ev.Type), ev)
}

// PlaceOrder reserves qty units of sku for a new order. It is idempotent
// in id: a client retrying after a timeout gets the same order.
func (s *Shop) PlaceOrder(ctx context.Context, id, sku string, qty int) error {
	created, soldOut := false, false
	err := qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
		created, soldOut = false, false
		// lock the stock row first, then check for the order: under read
		// committed a concurrent retry of this call is then seen
		v, _, err := s.read(tx, "stock/"+sku)
		if err != nil {
			return err
		}
		if _, ok := tx.Get("order/" + id); ok {
			return nil
		}
		stock, _ := v.(int)
		if stock < qty {
			// A refusal is a step (the model's refuse), recorded when the
			// read-only transaction commits: an error here would roll back
			// and drop the hook, and the stock check would go unchecked.
			tx.OnCommit(func() { qobs.Record(ctx, "refuse", nil, "order", id, "qty", qty, "obs.stock", stock) })
			soldOut = true
			return nil
		}
		tx.Put("stock/"+sku, stock-qty)
		tx.Put("order/"+id, Order{ID: id, SKU: sku, Qty: qty})
		s.emit(tx, Event{Type: "placed", Order: id})
		tx.OnCommit(func() { qobs.Record(ctx, "place", nil, "order", id, "qty", qty, "obs.stock", stock-qty) })
		created = true
		return nil
	})
	if err == nil && created && s.Mutation == DualWrite {
		qenv.Go(ctx, "publish", func(ctx context.Context) { s.Broker.Publish(ctx, Event{Type: "placed", Order: id}) })
	}
	if err == nil && soldOut {
		return ErrSoldOut
	}
	return err
}

// Cancel cancels an order that has not shipped, releasing its stock.
func (s *Shop) Cancel(ctx context.Context, id string) error {
	return qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
		v, ok, err := s.read(tx, "order/"+id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoOrder
		}
		o := v.(Order)
		shipped, cancelled := o.Shipped, o.Cancelled
		if s.Mutation == LockWithoutWrite {
			_, shipped = tx.Get("ship/" + id)
			_, cancelled = tx.Get("cancel/" + id)
		}
		if shipped {
			return ErrShipped
		}
		if cancelled {
			return nil
		}
		sv, _, err := s.read(tx, "stock/"+o.SKU)
		if err != nil {
			return err
		}
		restocked := sv.(int) + o.Qty
		tx.Put("stock/"+o.SKU, restocked)
		if s.Mutation == LockWithoutWrite {
			tx.Put("cancel/"+id, true)
		} else {
			o.Cancelled = true
			tx.Put("order/"+id, o)
		}
		s.emit(tx, Event{Type: "cancelled", Order: id})
		tx.OnCommit(func() { qobs.Record(ctx, "cancel", nil, "order", id, "obs.stock", restocked) })
		return nil
	})
}

// RunRelay publishes the outbox forever: read, publish, delete. A crash
// between publishing and deleting publishes again (at least once).
func (s *Shop) RunRelay(ctx context.Context) {
	every := s.PollInterval
	if every == 0 {
		every = 100 * time.Millisecond
	}
	for {
		tx := s.DB.Begin(ctx)
		rows := tx.Scan("outbox/")
		tx.Rollback()
		for _, r := range rows {
			s.Broker.Publish(ctx, r.Val.(Event))
			_ = qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error { tx.Delete(r.Key); return nil })
		}
		qenv.Sleep(ctx, every)
	}
}

// retry calls fn until it succeeds or returns a permanent error, as
// PaymentRetry decides (by default at most 4 times, 200 ms apart).
func (s *Shop) retry(ctx context.Context, fn func() error) error {
	if s.PaymentRetry != nil {
		s.PaymentRetry.Request(qenv.Now(ctx))
	}
	for n := 1; ; n++ {
		err := fn()
		if err == nil || errors.Is(err, ErrVoided) {
			return err
		}
		wait, ok := 200*time.Millisecond, n < 4
		if s.PaymentRetry != nil {
			wait, ok = s.PaymentRetry.Retry(n, qenv.Now(ctx))
		}
		if !ok {
			return errTransient
		}
		qenv.Sleep(ctx, wait)
	}
}

// RunPayments consumes "placed" and "cancelled" forever, with
// PaymentWorkers consumers.
func (s *Shop) RunPayments(ctx context.Context, in Inbox) {
	for i := 1; i < s.PaymentWorkers; i++ {
		qenv.Go(ctx, "payments.consumer", func(ctx context.Context) { s.consumePayments(ctx, in) })
	}
	s.consumePayments(ctx, in)
}

func (s *Shop) consumePayments(ctx context.Context, in Inbox) {
	for {
		ev, ack, nack := in.Receive(ctx)
		var err error
		switch ev.Type {
		case "placed":
			err = s.pay(ctx, ev.Order)
		case "cancelled":
			err = s.retry(ctx, func() error { return s.Payments.Void(ctx, "cap:"+ev.Order, ev.Order) })
		}
		if err != nil {
			if s.RedeliveryDelay > 0 {
				qenv.Sleep(ctx, s.RedeliveryDelay)
			}
			nack(ctx)
		} else {
			ack(ctx)
		}
	}
}

func (s *Shop) pay(ctx context.Context, id string) error {
	tx := s.DB.Begin(ctx)
	v, ok := tx.Get("order/" + id)
	_, cancelledRow := tx.Get("cancel/" + id)
	tx.Rollback()
	if !ok {
		return nil
	}
	o := v.(Order)
	if o.Paid || o.Cancelled || cancelledRow {
		return nil
	}
	attempt := 0
	err := s.retry(ctx, func() error {
		attempt++
		key := "cap:" + id
		if s.Mutation == NoIdempotencyKey {
			key = fmt.Sprintf("cap:%s:%d", id, attempt)
		}
		return s.Payments.Capture(ctx, key, id, o.Qty*10)
	})
	if errors.Is(err, ErrVoided) {
		return nil // cancelled meanwhile; Void refunded nothing or the charge
	}
	if err != nil {
		return err
	}
	return qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
		v, _, err := s.read(tx, "order/"+id)
		if err != nil {
			return err
		}
		o := v.(Order)
		if o.Paid {
			return nil
		}
		o.Paid = true
		tx.Put("order/"+id, o)
		s.emit(tx, Event{Type: "paid", Order: id})
		tx.OnCommit(func() { qobs.Record(ctx, "markPaid", nil, "order", id) })
		return nil
	})
}

// RunShipping consumes "paid" forever: ships an order unless cancelled.
func (s *Shop) RunShipping(ctx context.Context, in Inbox) {
	for {
		ev, ack, nack := in.Receive(ctx)
		err := qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
			v, ok, err := s.read(tx, "order/"+ev.Order)
			if err != nil || !ok || !v.(Order).Paid {
				return err
			}
			o := v.(Order)
			shipped, cancelled := o.Shipped, o.Cancelled
			if s.Mutation == LockWithoutWrite {
				_, shipped = tx.Get("ship/" + ev.Order)
				_, cancelled = tx.Get("cancel/" + ev.Order)
			}
			if shipped || cancelled {
				return nil
			}
			if s.Mutation == LockWithoutWrite {
				tx.Put("ship/"+ev.Order, true)
			} else {
				o.Shipped = true
				tx.Put("order/"+ev.Order, o)
			}
			tx.OnCommit(func() { qobs.Record(ctx, "ship", nil, "order", ev.Order) })
			return nil
		})
		if err != nil {
			nack(ctx)
		} else {
			ack(ctx)
		}
	}
}
