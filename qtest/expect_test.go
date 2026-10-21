package qtest

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
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
