package qanalysis_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/casselc/quint-connect-go/qanalysis"
)

// The analyzers report what quintgo lint reports, at the same places
// (qlint's own test has the same fixtures).
func TestSim(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), qanalysis.Sim, "simapp")
}

func TestWeave(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), qanalysis.Weave, "weaveapp")
}

// Without a binding: the action is named and the expressions see only
// what the advice can (parameters, receiver, named results).
func TestAction(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), qanalysis.Action, "actionapp")
}

// With a binding: the model has the action, and the annotation records
// the fields the binding uses. Loading the model runs quint.
func TestActionWithBinding(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not on PATH")
	}
	b, err := filepath.Abs(filepath.Join(analysistest.TestData(), "bank.binding.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := qanalysis.Action.Flags.Set("binding", b); err != nil {
		t.Fatal(err)
	}
	defer qanalysis.Action.Flags.Set("binding", "")
	analysistest.Run(t, analysistest.TestData(), qanalysis.Action, "bindingapp")
}
