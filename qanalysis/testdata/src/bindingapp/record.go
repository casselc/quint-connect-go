package bindingapp

import (
	"context"

	"github.com/casselc/quint-connect-go/qobs"
)

// ledger has its own Record, which is not a step.
type ledger struct{}

func (ledger) Record(ctx context.Context, entry string, err error) {}

const settle = "settle"

func transfer(ctx context.Context, rec *qobs.Recorder, l ledger) {
	qobs.Record(ctx, "transfer", nil, "src", "alice")
	qobs.Record(ctx, "depositt", nil) // want `Record\(ctx, "depositt", ...\): depositt is not an action of bank \(did you mean deposit\?\); its actions: deposit, transfer, withdraw`
	rec.Record(ctx, "close", nil)     // want `Record\(ctx, "close", ...\): close is not an action of bank; its actions: deposit, transfer, withdraw`
	qobs.Record(ctx, settle, nil)     // a constant: not seen
	l.Record(ctx, "not a step", nil)  // not qobs's Record
}
