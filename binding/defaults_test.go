package binding

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qtrace"
)

// A parameter with no template is the step field of its name, converted by
// the parameter's type, aliases included (gcounter's Vec is str -> int).
func TestDefaultArgs(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	m, err := LoadModel("quint", "../examples/glomers/model/gcounter.qnt", "gcounter")
	if err != nil {
		t.Fatal(err)
	}
	shapes := map[string]string{}
	for _, p := range m.Actions["gossip"] {
		shapes[p.Name] = p.Shape
	}
	if shapes["vec"] != "map" || shapes["src"] != "" {
		t.Fatalf("gossip's parameter shapes: %v", shapes)
	}
	b := &Binding{Spec: "x.qnt", Module: "gcounter", Actions: map[string]Action{
		"gossip": {Args: map[string]string{"id": `"fixed"`}},
	}}
	if errs := b.Lint(m); len(errs) != 1 || !strings.Contains(errs[0], "constant NODES") {
		t.Fatalf("lint: %v", errs)
	}
	tr := &qtrace.Trace{Actor: "a", Processes: []string{"p"}, Steps: []qtrace.Step{{
		Action: "gossip", Process: "p", Outcome: "ok",
		Args: map[string]any{"id": "n0/1", "src": "n0", "dst": "n1", "vec": `{"n0":2}`},
	}, {
		Action: "add", Process: "p", Outcome: "ok", Args: map[string]any{"node": "n0", "delta": int64(3)},
	}}}
	p, err := b.Plan(m, tr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Calls[0].Quint, `gossip("fixed", "n0", "n1", Map("n0" -> 2))`; got != want {
		t.Errorf("gossip call: got %s, want %s", got, want)
	}
	if got, want := p.Calls[1].Quint, `add("n0", 3)`; got != want {
		t.Errorf("add (not in the binding) call: got %s, want %s", got, want)
	}
	// a field the step lacks names the template and the signature
	tr.Steps[1].Args = map[string]any{"node": "n0"}
	if _, err := b.Plan(m, tr); err == nil || !strings.Contains(err.Error(), "add(node: str, delta: int)") {
		t.Fatalf("want an error naming the signature, got %v", err)
	}
}
