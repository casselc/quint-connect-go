package bank

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

// tellers is the bank under three concurrent tellers in a simulation: the
// chooser picks each operation, its accounts and amount, and the
// interleaving. The bank's own qobs.Record calls are the steps.
func tellers(m Mutation) qtest.System {
	return qtest.System{Binding: "bank.binding.yaml", Run: func(c qenv.Chooser) error {
		s := qenv.New(c)
		defer s.Close()
		b := New(accounts, WithMutation(m))
		pick := func(kind string) string { return accounts[s.Choose("sched.teller."+kind, len(accounts))] }
		for i := 0; i < 3; i++ {
			s.Spawn(fmt.Sprintf("teller%d", i), func(ctx context.Context) {
				for k := 0; k < 6; k++ {
					amount := 1 + s.Choose("sched.teller.amount", 5)
					switch s.Choose("sched.teller.op", 3) {
					case 0:
						_ = b.Deposit(ctx, pick("account"), amount)
					case 1:
						_ = b.Withdraw(ctx, pick("account"), amount)
					default:
						_ = b.Transfer(ctx, pick("src"), pick("dst"), amount)
					}
					qenv.Yield(ctx) // another teller may go next
				}
			})
		}
		return s.Run(time.Minute)
	}}
}

// The bank as a System: every workflow (seeds here; Hegel, sweeps and the
// fuzzer alike) runs the same Run. Both mutants show in some seeds: a
// self-transfer is no transition of the model, a lost deposit leaves a
// balance the model does not have.
func TestTellersAsASystem(t *testing.T) {
	qtest.RequireQuint(t)
	x := qtest.ExploreConfig{Seeds: 20}
	tellers(Faithful).Explore(t, x).Expect(t, qtest.Conforms())
	tellers(SelfTransfer).Explore(t, x).Expect(t, qtest.Some(qtest.Rejects("transfer")))
	tellers(LostDeposit).Explore(t, x).Expect(t, qtest.Some(qtest.Mismatch("deposit")))
}
