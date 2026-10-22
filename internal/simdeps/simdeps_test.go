package simdeps_test

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// The packages a simulation runs: qenv's runtime and doubles, recording,
// the load simulation, and the examples that run on gosim; with them,
// every package that imports qenv (simulatedPackages), so a new double or
// example is checked without being listed.
var simulated = []string{
	"github.com/casselc/quint-connect-go/qenv",
	"github.com/casselc/quint-connect-go/qobs",
	"github.com/casselc/quint-connect-go/qtrace",
	"github.com/casselc/quint-connect-go/qload",
	"github.com/casselc/quint-connect-go/qmeta",
	"github.com/casselc/quint-connect-go/qrole",
	"github.com/casselc/quint-connect-go/qrun",
	"github.com/casselc/quint-connect-go/qcover",
	"github.com/casselc/quint-connect-go/qenv/fault",
	"github.com/casselc/quint-connect-go/qmut",
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom",
	"github.com/casselc/quint-connect-go/examples/glomers/broadcast",
	"github.com/casselc/quint-connect-go/examples/checkout",
	"github.com/casselc/quint-connect-go/examples/retrystorm",
}

// hostSide import qenv but run outside any simulation: the Hegel adapters.
var hostSide = map[string]bool{
	"github.com/casselc/quint-connect-go/quinthegel": true,
}

const module = "github.com/casselc/quint-connect-go"

// simulatedPackages is simulated and every package of the module that
// imports qenv, but the host-side ones.
func simulatedPackages(t *testing.T) []string {
	out, err := exec.Command("go", "list", "-json", module+"/...").Output()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	all := append([]string(nil), simulated...)
	for _, p := range all {
		seen[p] = true
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if seen[p.ImportPath] || hostSide[p.ImportPath] {
			continue
		}
		for _, imp := range p.Imports {
			if imp == module+"/qenv" {
				all = append(all, p.ImportPath)
				seen[p.ImportPath] = true
				break
			}
		}
	}
	return all
}

type pkg struct {
	ImportPath string
	Standard   bool
	CgoFiles   []string
	Imports    []string
}

// native reports why a package cannot be translated into a simulation.
func native(p pkg) string {
	switch {
	case strings.HasPrefix(p.ImportPath, "hegel.dev/go/hegel"):
		return "Hegel"
	case strings.HasPrefix(p.ImportPath, "github.com/ebitengine/purego"):
		return "purego (native calls)"
	case !p.Standard && len(p.CgoFiles) > 0:
		return "cgo"
	}
	return ""
}

func TestSimulatedPackagesHaveNoNativeDeps(t *testing.T) {
	roots := simulatedPackages(t)
	out, err := exec.Command("go", append([]string{"list", "-deps", "-json"}, roots...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]pkg{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pkgs[p.ImportPath] = p
	}
	if len(roots) <= len(simulated) {
		t.Fatalf("found no importers of qenv beyond the listed packages (%d): the derivation is broken", len(roots))
	}
	for _, root := range roots {
		if chain := find(pkgs, root, map[string]bool{}); chain != nil {
			why := native(pkgs[chain[len(chain)-1]])
			t.Errorf("%s depends on %s, which a simulation cannot run: %s.\n"+
				"Move the code that needs it out of the simulated packages (Hegel adapters go in quinthegel).",
				root, why, strings.Join(chain, " -> "))
		}
	}
}

// find returns an import chain from p to a native package, or nil.
func find(pkgs map[string]pkg, p string, seen map[string]bool) []string {
	if seen[p] {
		return nil
	}
	seen[p] = true
	if native(pkgs[p]) != "" {
		return []string{p}
	}
	for _, imp := range pkgs[p].Imports {
		if chain := find(pkgs, imp, seen); chain != nil {
			return append([]string{p}, chain...)
		}
	}
	return nil
}
