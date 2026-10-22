package qtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/validate"
)

func TestExpectations(t *testing.T) {
	RequireQuint(t)
	rec := func(ops ...any) func(*qobs.Recorder) {
		return func(r *qobs.Recorder) {
			for i := 0; i+2 < len(ops); i += 3 {
				r.Record(context.Background(), ops[i].(string), nil, "amount", ops[i+1], "obs.balance", ops[i+2])
			}
		}
	}
	b := "../validate/testdata/account.yaml"
	Record(t, b, rec("deposit", 5, 5)).Expect(t, Conforms())
	Record(t, b, rec("deposit", 5, 5, "withdraw", 9, -4)).Expect(t, Rejects("withdraw"), Rejects(""))
	Record(t, b, rec("deposit", 5, 6)).Expect(t, Mismatch("deposit"))
	Record(t, b, rec("deposit", 60, 60, "deposit", 50, 110)).Expect(t, Violates("underLimit"))
	for _, e := range []Expectation{Conforms(), Rejects("deposit"), Violates("underLimit")} {
		if e.check(Record(t, b, rec("deposit", 5, 6)).Report) {
			t.Errorf("%s matched a state mismatch", e)
		}
	}
}

// Explore: two concurrent withdrawals, each checking the balance first. With
// a row lock the account never goes negative; without one, under read
// committed, some interleaving overdraws it.
func TestExplore(t *testing.T) {
	RequireQuint(t)
	b := "../validate/testdata/account.yaml"
	scenario := func(lock bool) func(qenv.Chooser) {
		return func(c qenv.Chooser) {
			s := qenv.New(c)
			defer s.Close()
			st := qenv.NewStore(s, "db", qenv.ReadCommitted)
			st.Load("balance", 10)
			ctx := s.Context() // carries the run's recorder
			qobs.Record(ctx, "deposit", nil, "amount", 10, "obs.balance", 10)
			for i := 0; i < 2; i++ {
				s.Spawn("withdraw", func(ctx context.Context) {
					_ = qenv.Retry(ctx, st, 3, func(tx qenv.Txn) error {
						var v any
						if lock {
							v, _, _ = tx.GetForUpdate("balance")
						} else {
							v, _ = tx.Get("balance")
						}
						if v.(int) < 7 {
							return nil
						}
						tx.Put("balance", v.(int)-7)
						tx.OnCommit(func() { qobs.Record(ctx, "withdraw", nil, "amount", 7, "obs.balance", v.(int)-7) })
						return nil
					})
				})
			}
			if err := s.Run(time.Second); err != nil {
				t.Fatal(err)
			}
		}
	}
	Explore(t, b, ExploreConfig{Seeds: 20}, scenario(true)).Expect(t, Conforms())
	x := Explore(t, b, ExploreConfig{Seeds: 20}, scenario(false))
	x.Expect(t, AnyOf(Mismatch("withdraw"), Rejects("withdraw")))
	if len(x.Failing()) == 20 {
		t.Fatal("every interleaving overdrew: the schedule is not being explored")
	}
}

// One binding, two instances: Constants overrides the binding's LIMIT.
func TestConstants(t *testing.T) {
	RequireQuint(t)
	b := "../validate/testdata/account.yaml"
	steps := Record(t, b, func(rec *qobs.Recorder) {
		for i := range 2 {
			rec.Record(context.Background(), "deposit", nil, "amount", 60, "obs.balance", 60*(i+1))
		}
	}).Steps
	Validate(t, b, steps).Expect(t, Violates("underLimit"))
	Validate(t, b, steps, Constants(map[string]string{"LIMIT": "200"})).Expect(t, Conforms())
	Validate(t, b, steps, Invariants("nonNegative")).Expect(t, Conforms()) // underLimit not checked
	if _, err := checker(t, b, validate.Options{}).WithConstants(map[string]string{"LIMT": "1"}); err == nil || !strings.Contains(err.Error(), "LIMIT") {
		t.Errorf("a misspelt constant: %v, want an error naming the model's constants", err)
	}
}

// Runs with the same steps share one verdict: ten seeds of a run that
// makes no choices are checked once, though each has its own recorder ids.
func TestVerdictCache(t *testing.T) {
	RequireQuint(t)
	cache := &VerdictCache{}
	x := Explore(t, "../validate/testdata/account.yaml", ExploreConfig{Seeds: 10, Verdicts: cache}, func(c qenv.Chooser) {
		s := qenv.New(c)
		defer s.Close()
		qobs.Record(s.Context(), "deposit", nil, "amount", 10, "obs.balance", 10)
	})
	x.Expect(t, Conforms())
	if cache.Hits() != 9 {
		t.Errorf("%d of 10 runs judged from the cache, want 9", cache.Hits())
	}
}
