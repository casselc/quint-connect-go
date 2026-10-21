// Package checkout deploys the shop (package shop) into a qenv
// simulation: its services as crashable processes, a database with a
// chosen isolation level, an at-least-once broker, a payment provider
// across an unreliable network, and clients that retry. Every choice of
// that environment (the interleaving of transactions, a crash, a lost
// response, a redelivery) is made by one qenv.Chooser: Hegel, a seed, or
// a replay.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Config of a deployment and its workload.
type Config struct {
	Isolation qenv.Isolation
	Mutation  shop.Mutation
	// Orders placed by clients (default 3), against Stock units of one SKU
	// (default 2: one order may sell out). Each order is perhaps cancelled.
	Orders, Stock int
	// NoCrashes keeps every process up.
	NoCrashes bool

	// For load experiments (load.go): the provider's capacity (nil:
	// unlimited, instant), and the shop's payment design.
	Provider        *qenv.Capacity
	PaymentWorkers  int
	PaymentRetry    shop.Retrier
	RedeliveryDelay time.Duration
}

// Deployment is a running shop in a simulation.
type Deployment struct {
	Sim      *qenv.Sim
	Shop     *shop.Shop
	DB       *qenv.Store
	Provider *qenv.Store
	PaySvc   *qenv.Service[shop.PayReq, string]
	PayQueue *qenv.Queue
	api      *qenv.Service[apiReq, string]
}

type apiReq struct{ Op, Order string }

// broker routes events to the consumers' queues.
type broker struct{ payments, shipping *qenv.Queue }

func (b broker) Publish(ctx context.Context, ev shop.Event) {
	if ev.Type == "paid" {
		b.shipping.Publish(ctx, ev)
	} else {
		b.payments.Publish(ctx, ev)
	}
}

type inbox struct{ q *qenv.Queue }

func (in inbox) Receive(ctx context.Context) (shop.Event, func(context.Context), func(context.Context)) {
	d := in.q.Receive(ctx)
	return d.Body.(shop.Event), d.Ack, d.Nack
}

// payments is the provider's client, across the network.
type payments struct {
	svc *qenv.Service[shop.PayReq, string]
}

func (p payments) call(ctx context.Context, r shop.PayReq) error {
	out, err := p.svc.Call(ctx, r, time.Second)
	if err == nil && out == "voided" {
		return shop.ErrVoided
	}
	return err
}

func (p payments) Capture(ctx context.Context, key, order string, amount int) error {
	return p.call(ctx, shop.PayReq{Op: "capture", Key: key, Order: order, Amount: amount})
}

func (p payments) Void(ctx context.Context, key, order string) error {
	return p.call(ctx, shop.PayReq{Op: "void", Key: key, Order: order})
}

// Deploy starts the shop's processes in s.
func Deploy(s *qenv.Sim, cfg Config) *Deployment {
	if cfg.Stock == 0 {
		cfg.Stock = 2
	}
	d := &Deployment{Sim: s, DB: qenv.NewStore(s, "shop", cfg.Isolation), Provider: qenv.NewStore(s, "provider", qenv.Serializable)}
	d.DB.Load("stock/widget", cfg.Stock)
	provider := &shop.Provider{DB: d.Provider}
	paySvc := qenv.NewService(s, "provider", nil, provider.Handle)
	paySvc.Capacity = cfg.Provider
	qPay, qShip := qenv.NewQueue(s, "payments"), qenv.NewQueue(s, "shipping")
	d.PaySvc, d.PayQueue = paySvc, qPay
	d.Shop = &shop.Shop{DB: d.DB, Broker: broker{qPay, qShip}, Payments: payments{paySvc}, Mutation: cfg.Mutation,
		PaymentWorkers: cfg.PaymentWorkers, PaymentRetry: cfg.PaymentRetry, RedeliveryDelay: cfg.RedeliveryDelay}
	crash := !cfg.NoCrashes
	api := s.Process("api", crash, func(context.Context) {})
	s.Process("relay", crash, d.Shop.RunRelay)
	s.Process("payments", crash, func(ctx context.Context) { d.Shop.RunPayments(ctx, inbox{qPay}) })
	s.Process("shipping", crash, func(ctx context.Context) { d.Shop.RunShipping(ctx, inbox{qShip}) })
	d.api = qenv.NewService(s, "api", api, func(ctx context.Context, r apiReq) (string, error) {
		var err error
		switch r.Op {
		case "place":
			err = d.Shop.PlaceOrder(ctx, r.Order, "widget", 1)
		case "cancel":
			err = d.Shop.Cancel(ctx, r.Order)
		}
		if err != nil {
			return err.Error(), nil
		}
		return "ok", nil
	})
	return d
}

// call is a client's request, retried on timeouts (the API is idempotent).
func (d *Deployment) call(ctx context.Context, r apiReq) string {
	for i := 0; i < 5; i++ {
		out, err := d.api.Call(ctx, r, 2*time.Second)
		if !errors.Is(err, qenv.ErrTimeout) {
			return out
		}
		qenv.Sleep(ctx, 500*time.Millisecond)
	}
	return "timeout"
}

// Workload starts the clients: each places an order and, by a schedule
// choice, keeps it or cancels it after 0 to 400 ms (in 50 ms steps): early,
// racing payment, or racing shipment.
func (d *Deployment) Workload(orders int) {
	if orders == 0 {
		orders = 3
	}
	for i := 1; i <= orders; i++ {
		id := fmt.Sprintf("o%d", i)
		d.Sim.Spawn("client."+id, func(ctx context.Context) {
			if d.call(ctx, apiReq{"place", id}) != "ok" {
				return
			}
			if k := d.Sim.Choose("sched.client.cancel", 10); k > 0 {
				qenv.Sleep(ctx, time.Duration(k-1)*50*time.Millisecond)
				d.call(ctx, apiReq{"cancel", id})
			}
		})
	}
}

// Run deploys the shop with c making the choices, runs the workload for
// 20 s of virtual time with faults, then settles for up to 60 s without,
// and records settle.
func Run(c qenv.Chooser, cfg Config) (*Deployment, error) {
	s := qenv.New(c)
	defer s.Close()
	d := Deploy(s, cfg)
	d.Workload(cfg.Orders)
	if err := s.Run(20 * time.Second); err != nil {
		return d, err
	}
	if err := s.Settle(60 * time.Second); err != nil {
		return d, err
	}
	qobs.Record(s.Context(), "settle", nil)
	return d, nil
}
