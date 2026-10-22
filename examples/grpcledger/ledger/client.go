package ledger

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/casselc/quint-connect-go/examples/grpcledger/ledgerpb"
	"github.com/casselc/quint-connect-go/qenv"
)

// Client calls a Ledger, retrying what may not have happened: a refused
// connection, an overloaded server and a call with no response (whose
// transfer may have been applied, which the idempotency key makes safe to
// retry).
type Client struct {
	Ledger   ledgerpb.LedgerClient
	Attempts int           // default 4
	Backoff  time.Duration // before the first retry, doubling (default 100 ms)
}

// retryable are the codes a call may be retried on with the same key.
func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	}
	return false
}

func (c *Client) retry(ctx context.Context, call func() error) error {
	attempts, wait := c.Attempts, c.Backoff
	if attempts == 0 {
		attempts = 4
	}
	if wait == 0 {
		wait = 100 * time.Millisecond
	}
	for i := 1; ; i++ {
		err := call()
		if !retryable(err) || i == attempts {
			return err
		}
		qenv.Sleep(ctx, wait) // virtual in a simulation, real outside
		wait *= 2
	}
}

// Transfer moves amount from src to dest under key, retried.
func (c *Client) Transfer(ctx context.Context, key, src, dest string, amount int64) (*ledgerpb.TransferResponse, error) {
	var resp *ledgerpb.TransferResponse
	err := c.retry(ctx, func() (err error) {
		resp, err = c.Ledger.Transfer(ctx, &ledgerpb.TransferRequest{IdempotencyKey: key, From: src, To: dest, Amount: amount})
		return err
	})
	return resp, err
}

// Balance reads an account, retried.
func (c *Client) Balance(ctx context.Context, acct string) (int64, error) {
	var resp *ledgerpb.BalanceResponse
	err := c.retry(ctx, func() (err error) {
		resp, err = c.Ledger.Balance(ctx, &ledgerpb.BalanceRequest{Account: acct})
		return err
	})
	return resp.GetAmount(), err
}
