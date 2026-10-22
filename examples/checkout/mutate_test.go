package checkout_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

// Every woven mutant of the shop, explored: run woven, go run -C qweave
// ./cmd/qweave -C .. -mutate -coverpkg
// github.com/casselc/quint-connect-go/examples/checkout/shop go test -run
// TestMutationOfTheShop ./examples/checkout/
func TestMutationOfTheShop(t *testing.T) {
	qtest.RequireQuint(t)
	rep := system(checkout.Config{Isolation: qenv.SnapshotIsolation}).Mutate(t, qtest.ExploreConfig{Seeds: 20, Rates: crashy}, nil)
	t.Log(rep)
}
