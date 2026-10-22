package actionmap_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/casselc/quint-connect-go/qanalysis/actionmap"
	"github.com/casselc/quint-connect-go/qcover"
)

const ck = "github.com/casselc/quint-connect-go/examples/checkout"

// Every action checkout records has a site: Record calls in OnCommit
// closures are found, by the declaration that holds them.
func TestCheckoutStatic(t *testing.T) {
	m, err := actionmap.Static("../..", "./examples/checkout/...")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"place": ck + "/shop.(Shop).PlaceOrder", "cancel": ck + "/shop.(Shop).Cancel",
		"markPaid": ck + "/shop.(Shop).pay", "ship": ck + "/shop.(Shop).RunShipping",
		"charge": ck + "/shop.(Provider).Handle", "refund": ck + "/shop.(Provider).Handle",
		"settle": ck + ".RunOn",
	}
	for a, site := range want {
		if !slices.Contains(m.Sites[a], site) {
			t.Errorf("%s: sites %v, want %s", a, m.Sites[a], site)
		}
	}
	// commit hooks are a library's callbacks: cancel does not reach
	// PlaceOrder through them
	if m.Reach["cancel"][ck+"/shop.(Shop).PlaceOrder"] {
		t.Error("cancel reaches PlaceOrder: reachability went through qenv's commit hooks")
	}
}

// Woven for coverage, checkout's steps read against the static map: the
// code that leads to a step lives in its site's callers (the payment
// consumer loop before markPaid), which reachability from the site
// cannot see, and setup (Deploy) is the first step's.
func TestCheckoutCompare(t *testing.T) {
	if testing.Short() {
		t.Skip("weaves checkout and explores it")
	}
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not on PATH")
	}
	keep := t.TempDir()
	cmd := exec.Command("go", "run", "-C", "qweave", "./cmd/qweave", "-C", "..", "-cover", "-coverpkg", ck,
		"go", "test", "-count=1", "-run", "^TestCodeCoverage$", "./examples/checkout/")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "QUINTGO_KEEP="+keep)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(keep, "checkout-coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s qcover.Summary
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	m, err := actionmap.Static("../..", "./examples/checkout/...")
	if err != nil {
		t.Fatal(err)
	}
	gaps := actionmap.Compare(m, &s)
	t.Log("\n" + actionmap.Report(gaps))
	by := map[string]actionmap.Gap{}
	for _, g := range gaps {
		by[g.Action] = g
		if len(g.Sites) == 0 {
			t.Errorf("%s: steps with no site", g.Action)
		}
	}
	if !slices.Contains(by["markPaid"].Unreachable, ck+"/shop.(Shop).RunPayments") {
		t.Errorf("markPaid: the consumer loop before pay should be run by its steps and unreachable from pay: %+v", by["markPaid"])
	}
	if !slices.Contains(by["place"].Unreachable, ck+".Deploy") {
		t.Errorf("place: setup should be run by the first step: %+v", by["place"])
	}
}
