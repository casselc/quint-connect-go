package qenv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// Two transfers lock the same rows in opposite orders. Without detection
// both waited forever and the run recorded nothing; now the transaction
// that closes the cycle aborts with ErrDeadlock (an ErrConflict), and Retry
// lets both commit.
func TestStoreDetectsDeadlock(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	st := qenv.NewStore(s, "db", qenv.Serializable)
	st.Load("a", 10)
	st.Load("b", 10)
	var deadlocks int
	committed := 0
	transfer := func(first, second string) func(context.Context) {
		return func(ctx context.Context) {
			err := qenv.Retry(ctx, st, 5, func(tx qenv.Txn) error {
				if _, _, err := tx.GetForUpdate(first); err != nil {
					return err
				}
				qenv.Sleep(ctx, 10*time.Millisecond) // both hold their first lock
				_, _, err := tx.GetForUpdate(second)
				if errors.Is(err, qenv.ErrDeadlock) {
					deadlocks++
				}
				return err
			})
			if err != nil {
				t.Errorf("%s then %s: %v", first, second, err)
				return
			}
			committed++
		}
	}
	s.Spawn("ab", transfer("a", "b"))
	s.Spawn("ba", transfer("b", "a"))
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if deadlocks != 1 || committed != 2 {
		t.Fatalf("%d deadlocks, %d committed; want 1 and 2", deadlocks, committed)
	}
}
