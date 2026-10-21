package quinthegel_test

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

// TestModeChild runs one configuration; TestCompareModes re-executes the
// test binary for each, so failing configurations can be timed too.
func TestModeChild(t *testing.T) {
	spec := os.Getenv("QUINTHEGEL_CHILD")
	if spec == "" {
		t.Skip("run by TestCompareModes")
	}
	var mode quinthegel.Mode
	var buggy bool
	var cases int
	var name string
	fmt.Sscanf(spec, "%s %t %d", &name, &buggy, &cases)
	mode = map[string]quinthegel.Mode{"Each": quinthegel.Each, "After": quinthegel.After, "Batched": quinthegel.Batched}[name]
	quinthegel.Test(t, quinthegel.Config{Binding: "testdata/bank.yaml", Mode: mode, Seed: 7}, run(buggy),
		hegel.WithTestCases(cases), hegel.WithDatabase(""), hegel.WithSeed(7))
}

// TestCompareModes times Each, After and Batched on the same machine, on a
// faithful bank (every run passes) and a buggy one (a run fails, and is
// shrunk in Each and Batched). QUINTHEGEL_COMPARE=1 to run (a few minutes).
func TestCompareModes(t *testing.T) {
	if os.Getenv("QUINTHEGEL_COMPARE") == "" {
		t.Skip("set QUINTHEGEL_COMPARE=1")
	}
	needQuint(t)
	reQuint := regexp.MustCompile(`(\d+) runs checked with quint|~(\d+) quint processes|checked (\d+) distinct runs`)
	reShrunk := regexp.MustCompile(`(?s)model step \d+: transfer\(amount=\d+, dst=(\w+), src=(\w+)\)`)
	for _, c := range []struct {
		mode  string
		buggy bool
		cases int
	}{
		{"Each", false, 40}, {"After", false, 40}, {"Batched", false, 40},
		{"Each", true, 40}, {"After", true, 40}, {"Batched", true, 40},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestModeChild$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), fmt.Sprintf("QUINTHEGEL_CHILD=%s %t %d", c.mode, c.buggy, c.cases))
		t0 := time.Now()
		out, err := cmd.CombinedOutput()
		d := time.Since(t0)
		failed := err != nil
		q := reQuint.FindStringSubmatch(string(out))
		shrunk := ""
		if m := reShrunk.FindAllStringSubmatch(string(out), -1); len(m) > 0 {
			last := m[len(m)-1]
			shrunk = fmt.Sprintf("transfer %s->%s", last[2], last[1])
		}
		t.Logf("%-8s buggy=%-5v %3d cases: %6.1fs  failed=%-5v quint=%v  shrunk to %q", c.mode, c.buggy, c.cases, d.Seconds(), failed,
			strings.Join(q, " "), shrunk)
		if failed != c.buggy {
			t.Errorf("%s buggy=%v: failed=%v\n%s", c.mode, c.buggy, failed, out)
		}
	}
}
