package qtest

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qmut"
)

// NoSurvivors names each unmarked survivor with its mark, and each mark
// that matched no survivor.
func TestNoSurvivors(t *testing.T) {
	dropAck := qmut.Mutant{ID: 1, File: "/src/shop/pay.go", Line: 42, Op: "drop call", Code: "ack()"}
	boundary := qmut.Mutant{ID: 2, File: "/src/shop/retry.go", Line: 7, Op: "<= for <"}
	rep := &MutationReport{Survived: []qmut.Mutant{dropAck, boundary}}
	f := &failTB{TB: t}
	rep.Expect(f, NoSurvivors("pay.go:42 drop call", "retry.go:7"))
	if len(f.errs) != 0 {
		t.Errorf("both marked: %v", f.errs)
	}
	f = &failTB{TB: t}
	rep.Expect(f, NoSurvivors("pay.go:42", "stock.go:9"))
	if len(f.errs) != 2 || !strings.Contains(f.errs[0], `NoSurvivors("retry.go:7")`) || !strings.Contains(f.errs[1], "stock.go:9") {
		t.Errorf("an unmarked survivor and a stale mark: %v", f.errs)
	}
}
