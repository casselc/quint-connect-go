package mbt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/validate"
)

// QUINT_MAX_SAMPLES (default 5) traces of QUINT_MAX_STEPS (default 30)
// steps are generated from the model's currentDesign instance with seed
// QUINT_SEED (default 42) and QUINT_BACKEND (default typescript: the
// configuration these tests were verified with; see README "Known gaps"
// for a Rust-backend trace the driver cannot schedule yet).
func traces(t *testing.T) []string {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
	c := connect.Config{Spec: "../model/edgePublish.qnt", Main: "currentDesign", Dir: t.TempDir()}.FromEnv()
	if c.MaxSamples == 0 {
		c.MaxSamples = 5
	}
	if c.MaxSteps == 0 {
		c.MaxSteps = 30
	}
	if c.Seed == "" {
		c.Seed = "42"
	}
	if c.Backend == "" {
		c.Backend = "typescript"
	}
	files, _, err := connect.GenerateRun(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestModelTracesDriveThePublisher(t *testing.T) {
	for _, f := range traces(t) {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var acts []string
		for _, s := range tr.States[1:] {
			acts = append(acts, s.Action)
		}
		d := &Driver{}
		if err := d.Replay(tr); err != nil {
			t.Errorf("%s: %v\n  actions: %s", filepath.Base(f), err, strings.Join(acts, " "))
		} else {
			t.Logf("%s: %d steps replayed: %s", filepath.Base(f), len(tr.States)-1, strings.Join(acts, " "))
		}
	}
}

// The mutant must fail on some trace that starts a push and writes its table.
func TestModelTracesCatchManifestBeforeTable(t *testing.T) {
	caught := 0
	files := traces(t)
	for _, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d := &Driver{Mutation: publisher.ManifestBeforeTable}
		if err := d.Replay(tr); err != nil {
			caught++
			t.Logf("%s: rejected: %v", filepath.Base(f), err)
		}
	}
	if caught == 0 {
		t.Fatalf("no trace exposed the manifest-before-table mutant (%d traces)", len(files))
	}
}

// Round trip: drive the publisher from model traces with step recording on,
// then validate what it recorded against the model. Needs the annotations
// woven: `go run github.com/DataDog/orchestrion go test ./mbt/ -run RoundTrip`.
func TestRoundTrip(t *testing.T) {
	files := traces(t)
	checker, err := validate.NewChecker("../binding.yaml", validate.Options{Backend: os.Getenv("QUINT_BACKEND"), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sink := &qobs.MemorySink{}
		rec := qobs.NewRecorder(sink)
		qobs.SetGlobal(rec)
		d := &Driver{Recorder: rec}
		err = d.Replay(tr)
		qobs.SetGlobal(nil)
		if err != nil {
			t.Fatalf("%s: replay: %v", filepath.Base(f), err)
		}
		steps := sink.Steps()
		if len(steps) == 0 {
			t.Skip("no steps recorded: build with orchestrion to weave //quint:action")
		}
		rep, err := checker.Check(context.Background(), steps)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.OK() {
			t.Errorf("%s: recorded telemetry does not validate:\n%s", filepath.Base(f), rep.Summary())
		} else {
			t.Logf("%s: %d recorded steps validate against the model", filepath.Base(f), len(steps))
		}
	}
}
