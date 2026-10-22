// Package actionapp has //quint:action annotations for the Action
// analyzer, some wrong. Expectations sit in block comments before the
// directive, which runs to the end of its line.
package actionapp

import (
	"context"
	"errors"
)

// Bank keeps balances.
type Bank struct{ balances map[string]int }

/* fine: parameters and the receiver */
//quint:action action:deposit account:account amount:amount obs.balance:b.balances[account]
func (b *Bank) Deposit(ctx context.Context, account string, amount int) error {
	b.balances[account] += amount
	return nil
}

/* want `withdraw: obs.balance uses left, which is not a parameter, the receiver or a named result of Withdraw` */ //quint:action action:withdraw account:account amount:amount obs.balance:left
func (b *Bank) Withdraw(ctx context.Context, account string, amount int) error {
	left := b.balances[account] - amount
	if left < 0 {
		return errors.New("overdraft")
	}
	b.balances[account] = left
	return nil
}

/* fine: named results, a function literal's own parameter, a struct literal's keys */
//quint:action action:transfer src:src dst:dst amount:amount obs.src_balance:s obs.dst_balance:"func(x int) int { return x }(d)"
func (b *Bank) Transfer(ctx context.Context, src, dst string, amount int) (s, d int, err error) {
	b.balances[src] -= amount
	b.balances[dst] += amount
	return b.balances[src], b.balances[dst], nil
}

/* want `//quint:action on Audit without action:NAME` */ //quint:action account:account
func (b *Bank) Audit(account string)                     {}

/* want `close: amount:\)\( is not a Go expression` */ //quint:action action:close amount:")("
func (b *Bank) Close(account string)                   {}
