// Package bank has one annotated step.
package bank

import "context"

// Bank holds balances.
type Bank struct{ bal map[string]int }

// Deposit adds n to acct.
//
//quint:action action:deposit acct:acct amount:n
func (b *Bank) Deposit(ctx context.Context, acct string, n int) error {
	b.bal[acct] += n
	return nil
}
