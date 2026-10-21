package shop

import (
	"context"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Provider is the payment provider: the external system, with the
// contract real providers offer. A capture with a key already used returns
// the first result. Void refunds the key's charge, if any, and rejects
// later captures with the key. It is not under test, but its charges and
// refunds are model steps, recorded where they happen.
type Provider struct {
	DB qenv.DB // its own, serializable
}

// PayReq is a request to the provider.
type PayReq struct {
	Op     string // capture, void
	Key    string
	Order  string
	Amount int
}

// payment is the provider's row for an idempotency key.
type payment struct {
	Order    string
	Charged  bool
	Refunded bool
	Voided   bool
}

// Handle serves one request.
func (p *Provider) Handle(ctx context.Context, r PayReq) (string, error) {
	var out string
	err := qenv.Retry(ctx, p.DB, 10, func(tx qenv.Txn) error {
		v, _ := tx.Get("pay/" + r.Key)
		pm, _ := v.(payment)
		pm.Order = r.Order
		switch r.Op {
		case "capture":
			switch {
			case pm.Voided:
				out = "voided"
				return nil
			case pm.Charged:
				out = "ok"
				return nil
			}
			pm.Charged = true
			tx.OnCommit(func() { qobs.Record(ctx, "charge", nil, "order", r.Order) })
		case "void":
			if pm.Charged && !pm.Refunded {
				pm.Refunded = true
				tx.OnCommit(func() { qobs.Record(ctx, "refund", nil, "order", r.Order) })
			}
			pm.Voided = true
		}
		out = "ok"
		tx.Put("pay/"+r.Key, pm)
		return nil
	})
	return out, err
}
