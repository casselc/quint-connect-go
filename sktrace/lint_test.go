package sktrace_test

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/sktrace"
)

func obj(kind, ns, name string, owner ...string) sktrace.Object {
	meta := map[string]any{"namespace": ns, "name": name}
	if len(owner) == 2 {
		meta["ownerReferences"] = []any{map[string]any{"kind": owner[0], "name": owner[1]}}
	}
	api := "v1"
	if kind == "Deployment" || kind == "ReplicaSet" {
		api = "apps/v1"
	}
	return sktrace.Object{"apiVersion": api, "kind": kind, "metadata": meta}
}

// A scenario's dependencies: the production trace needs its namespace,
// and with it and its kind served it lints clean. A trace that starts
// mid-run deletes what it never applied, and a pod whose owner comes later
// is an orphan to the garbage collector.
func TestLint(t *testing.T) {
	tr, err := sktrace.ReadFile("testdata/dsb-scaling.sktrace")
	if err != nil {
		t.Fatal(err)
	}
	if fs := tr.Lint(sktrace.Env{}); len(fs) != 1 || !strings.Contains(fs[0].What, "namespace dsb does not exist") {
		t.Errorf("no namespace: %v", fs)
	}
	if fs := tr.Lint(sktrace.Env{Namespaces: []string{"dsb"}, Kinds: []string{"apps/v1.Deployment"}}); len(fs) != 0 {
		t.Errorf("namespace and kind present: %v", fs)
	}
	if fs := tr.Lint(sktrace.Env{Namespaces: []string{"dsb"}, Kinds: []string{"v1.Pod"}}); len(fs) == 0 || !strings.Contains(fs[0].What, "not served") {
		t.Errorf("kind not served: %v", fs)
	}
	mid := &sktrace.Trace{Events: []sktrace.Event{
		{TS: 100, Applied: []sktrace.Object{obj("Namespace", "", "app"), obj("Pod", "app", "web-1", "ReplicaSet", "web")}},
		{TS: 110, Applied: []sktrace.Object{obj("ReplicaSet", "app", "web")}, Deleted: []sktrace.Object{obj("Pod", "app", "old-1")}},
	}}
	fs := mid.Lint(sktrace.Env{})
	if len(fs) != 2 || !strings.Contains(fs[0].What, "owner ReplicaSet app/web was not applied") || !strings.Contains(fs[1].What, "never applied") {
		t.Errorf("mid-run trace: %v", fs)
	}
	for _, f := range fs {
		t.Log(f)
	}
}

// The CronJob trace's pods ran 30 s (the first version) or 45 s: its
// lifetimes, as delay options from shortest (nominal) to longest.
func TestLifetimeQuantiles(t *testing.T) {
	tr, err := sktrace.ReadFile("testdata/cronjob.sktrace")
	if err != nil {
		t.Fatal(err)
	}
	ds := tr.Lifetimes("")
	if len(ds) == 0 {
		t.Fatal("no ended pods")
	}
	q := sktrace.Quantiles(ds, 3)
	if len(q) != 3 || q[0] > q[1] || q[1] > q[2] || q[0] < 25e9 || q[2] > 50e9 {
		t.Fatalf("quantiles %v of %d lifetimes", q, len(ds))
	}
	t.Logf("%d lifetimes; options %v", len(ds), q)
}
