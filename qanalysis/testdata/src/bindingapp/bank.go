// Package bindingapp is checked against examples/bank's binding and model
// (testdata/bank.binding.yaml).
package bindingapp

import "context"

// Bank keeps balances.
type Bank struct{ balances map[string]int }

/* fine: every field the binding's expect uses is recorded */
//quint:action action:deposit account:account amount:amount obs.balance:b.balances[account]
func (b *Bank) Deposit(ctx context.Context, account string, amount int) error { return nil }

/* want `the binding for withdraw uses .obs.balance, which this annotation does not record` */ //quint:action action:withdraw account:account amount:amount
func (b *Bank) Withdraw(ctx context.Context, account string, amount int) error                 { return nil }

/* want `close is not an action of bank` */ //quint:action action:close account:account
func (b *Bank) Close(account string)        {}
