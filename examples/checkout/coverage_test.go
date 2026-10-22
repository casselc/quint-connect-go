package checkout_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

// Which code each model step ran, and what conforming runs never ran. Run
// woven: go run -C qweave ./cmd/qweave -C .. -cover -coverpkg
// github.com/casselc/quint-connect-go/examples/checkout go test -run
// TestCodeCoverage ./examples/checkout/
func TestCodeCoverage(t *testing.T) {
	qtest.RequireQuint(t)
	if len(qcover.Blocks()) == 0 {
		t.Skip("not woven for coverage: run with qweave -cover")
	}
	x := explore(t, checkout.Config{Isolation: qenv.SnapshotIsolation}, qtest.ExploreConfig{Seeds: 30, Rates: crashy, Coverage: true, Parallel: 4})
	x.Expect(t, qtest.Conforms())
	t.Log(x.Coverage())
	t.Log(x.CodeCoverage())
	// kept for qanalysis/actionmap, which reads it against the code each
	// action reaches statically
	if k := os.Getenv("QUINTGO_KEEP"); k != "" {
		b, err := json.Marshal(x.CodeCoverage())
		if err == nil {
			err = os.WriteFile(filepath.Join(k, "checkout-coverage.json"), b, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
