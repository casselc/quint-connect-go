package binding

import (
	"os/exec"
	"testing"

	"github.com/casselc/quint-connect-go/qtrace"
)

// A step argument named like a step field (seq) is the parameter of its
// name: it once was the recorder's sequence number, so apply(seq=1)
// replayed as apply(4). The step's own number stays .step.seq.
func TestArgumentNamedLikeAStepField(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	m, err := LoadModel("quint", "testdata/seqarg.qnt", "seqarg")
	if err != nil {
		t.Fatal(err)
	}
	tr := &qtrace.Trace{Actor: "a", Processes: []string{"p"}, Steps: []qtrace.Step{
		{Action: "apply", Process: "p", Outcome: "ok", Seq: 4, Args: map[string]any{"seq": int64(1)}},
	}}
	b := &Binding{Spec: "testdata/seqarg.qnt", Module: "seqarg"}
	p, err := b.Plan(m, tr)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Calls[0].Quint; got != "apply(1)" {
		t.Errorf("default: got %s, want apply(1)", got)
	}
	b.Actions = map[string]Action{"apply": {Args: map[string]string{"seq": "{{.step.seq}}"}}}
	if p, err = b.Plan(m, tr); err != nil {
		t.Fatal(err)
	}
	if got := p.Calls[0].Quint; got != "apply(4)" {
		t.Errorf(".step.seq: got %s, want apply(4)", got)
	}
}

// A parameter named like the step's own .process defaults to the argument
// (.args.process), not the actor's incarnation, which it once silently was.
func TestParameterNamedProcess(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	m, err := LoadModel("quint", "testdata/procarg.qnt", "procarg")
	if err != nil {
		t.Fatal(err)
	}
	tr := &qtrace.Trace{Actor: "a", Processes: []string{"p"}, Steps: []qtrace.Step{
		{Action: "crash", Process: "p", Outcome: "ok", Seq: 1, Args: map[string]any{"process": "b"}},
	}}
	b := &Binding{Spec: "testdata/procarg.qnt", Module: "procarg"}
	p, err := b.Plan(m, tr)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Calls[0].Quint; got != `crash("b")` {
		t.Errorf("default: got %s, want crash(\"b\")", got)
	}
	if got := b.Actions["crash"].Fields(m.Actions["crash"]...); len(got) != 1 || got[0] != "process" {
		t.Errorf("fields: got %v, want [process]", got)
	}
}
