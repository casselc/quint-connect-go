package qtest

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// overdraw deposits past the default LIMIT (100).
func overdraw(c qenv.Chooser) error {
	qobs.Record(qenv.ContextOf(c), "deposit", nil, "amount", 150, "obs.balance", 150)
	return nil
}

// A verdict is of steps against one instance: the same steps are a
// violation with LIMIT 100 and conform with LIMIT 200, and a cache shared
// by the two explorations (as a sweep's rows share one) keeps them apart.
func TestVerdictCacheKeysTheInstance(t *testing.T) {
	RequireQuint(t)
	cache := &VerdictCache{}
	cfg := ExploreConfig{Seeds: 1, Verdicts: cache}
	System{Binding: "../validate/testdata/account.yaml", Run: overdraw}.Explore(t, cfg).Expect(t, Violates("underLimit"))
	System{Binding: "../validate/testdata/account.yaml", Run: overdraw, Constants: map[string]string{"LIMIT": "200"}}.
		Explore(t, cfg).Expect(t, Conforms())
}

// A claim naming an invariant or action the model does not have fails at
// once: None(Violates("underLimt")) would otherwise hold for nothing.
func TestClaimNamesAreChecked(t *testing.T) {
	RequireQuint(t)
	x := System{Binding: "../validate/testdata/account.yaml", Run: overdraw}.Explore(t, ExploreConfig{Seeds: 1})
	for _, c := range []Claim{None(Violates("underLimt")), Rejects("depositt")} {
		f := &failTB{TB: t}
		done := make(chan struct{})
		go func() { defer close(done); x.Expect(f, c) }() //qenv:ok failTB's Fatalf ends the goroutine (runtime.Goexit)
		<-done
		if len(f.errs) != 1 || !strings.Contains(f.errs[0], "misspelled") {
			t.Errorf("%v: %v", c, f.errs)
		}
	}
}
