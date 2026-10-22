package actionmap_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/casselc/quint-connect-go/qanalysis/actionmap"
	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
)

const fix = "example.com/actionmapfixture/shop"

// Sites by annotation and by qobs.Record in a closure; reachability
// through calls and the closure.
func TestStatic(t *testing.T) {
	m, err := actionmap.Static("../testdata/actionmap", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Actions(); !reflect.DeepEqual(got, []string{"cancel", "place"}) {
		t.Fatalf("actions %v", got)
	}
	if got := m.Sites["cancel"]; !reflect.DeepEqual(got, []string{fix + ".(Shop).Cancel"}) {
		t.Errorf("cancel's sites %v; want Cancel, the closure's declaration", got)
	}
	for action, want := range map[string][]string{
		"place":  {".(Shop).Place", ".(Shop).reserve", ".(Shop).validate", ".(Shop).log"},
		"cancel": {".(Shop).Cancel", ".(Shop).release"},
	} {
		for _, f := range want {
			if !m.Reach[action][fix+f] {
				t.Errorf("%s does not reach %s: %v", action, f, m.Reach[action])
			}
		}
		if m.Reach[action][fix+".Helper"] {
			t.Errorf("%s reaches Helper", action)
		}
	}
}

// Read against steps: what the place steps never ran, what ran under
// cancel only, and what cancel's steps ran that no site reaches.
func TestCompare(t *testing.T) {
	m, err := actionmap.Static("../testdata/actionmap", "./...")
	if err != nil {
		t.Fatal(err)
	}
	blocks := []qcover.Block{}
	for i, f := range []string{".(Shop).Place", ".(Shop).reserve", ".(Shop).validate", ".(Shop).log", ".(Shop).Cancel", ".(Shop).release", ".Helper"} {
		blocks = append(blocks, qcover.Block{Func: fix + f, ID: i})
	}
	s := &qcover.Summary{Runs: 1, Blocks: blocks,
		Hit: map[int]int{0: 1, 1: 1, 3: 1, 4: 1, 5: 1, 6: 1},
		ByStep: map[string]map[int]bool{
			"place/ok":  {0: true, 1: true},
			"cancel/ok": {3: true, 4: true, 5: true, 6: true},
		}}
	gaps := actionmap.Compare(m, s)
	t.Log("\n" + actionmap.Report(gaps))
	want := []actionmap.Gap{
		{Action: "cancel", Sites: []string{fix + ".(Shop).Cancel"},
			Unreachable: []string{fix + ".(Shop).log", fix + ".Helper"}},
		{Action: "place", Sites: []string{fix + ".(Shop).Place"},
			NotRun: []string{fix + ".(Shop).validate"}, ByOthers: []string{fix + ".(Shop).log"}},
	}
	if !reflect.DeepEqual(gaps, want) {
		t.Errorf("gaps\n%#v\nwant\n%#v", gaps, want)
	}
}

// Dead code and untried error paths. unused is called by nothing (Helper
// is exported, a root of a library). The runs reached the charge fault
// point and never faulted it, so the refund branch of its caller, which no
// run ran, is an error path the exploration never tried.
func TestDeadAndUntriedErrorPaths(t *testing.T) {
	m, err := actionmap.Static("../testdata/actionmap", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Dead, fix+".unused") || slices.Contains(m.Dead, fix+".Helper") {
		t.Errorf("dead %v; want unused and not Helper", m.Dead)
	}
	if !m.Callers[fix+".(Shop).charge"][fix+".(Shop).Place"] {
		t.Errorf("callers of charge: %v", m.Callers[fix+".(Shop).charge"])
	}
	qenv.DeclareFault("pay", "charge")
	cov := qenv.CoverageOf([][]qenv.Choice{{{Kind: "fault.pay", N: 2, Value: 0}}})
	s := &qcover.Summary{Runs: 1, Hit: map[int]int{0: 1}, Blocks: []qcover.Block{
		{Func: fix + ".(Shop).Place", Line: 22, ID: 0},
		{Func: fix + ".(Shop).Place", Line: 25, ID: 1},
	}}
	paths := actionmap.UntriedErrorPaths(m, s, cov)
	if len(paths) != 1 || paths[0].Kind != "pay" || paths[0].Caller != fix+".(Shop).Place" || len(paths[0].Blocks) != 1 || paths[0].Blocks[0].ID != 1 {
		t.Fatalf("untried error paths %+v", paths)
	}
}
