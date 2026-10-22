package fleetrollout_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/kwok/pkg/apis/internalversion"
	"sigs.k8s.io/kwok/pkg/config"
	"sigs.k8s.io/kwok/pkg/utils/lifecycle"

	"github.com/casselc/quint-connect-go/examples/fleetrollout"
	"github.com/casselc/quint-connect-go/sktrace"
)

// The stages load with kwok's own config loader and match pods with its
// lifecycle: a new pod of a Deployment gets one of the trace's readiness
// times, a running Job's pod one of its run times, and a deleted pod goes
// at once. kwok-demo.sh (TRACE=) runs them on real clusters.
func TestKwokStagesFromTrace(t *testing.T) {
	tr, err := sktrace.ReadFile("../../sktrace/testdata/cronjob.sktrace")
	if err != nil {
		t.Fatal(err)
	}
	y, err := fleetrollout.KwokStagesFromTrace(tr, "", 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	lc := loadStages(t, y)

	// The trace's lifetime quantiles are 30s, 30s and 45s.
	want := []time.Duration{3 * time.Second, 3 * time.Second, 4500 * time.Millisecond}
	newPod := map[string]any{
		"metadata": map[string]any{"name": "web-1", "ownerReferences": []any{map[string]any{"kind": "ReplicaSet"}}},
		"spec":     map[string]any{"nodeName": "fake", "containers": []any{map[string]any{"name": "web", "image": "web:v2"}}},
		"status":   map[string]any{"phase": "Pending"},
	}
	if got := delays(t, lc, newPod); !slices.Equal(names(t, lc, newPod), []string{"pod-ready-0", "pod-ready-1", "pod-ready-2"}) || !slices.Equal(got, want) {
		t.Errorf("a new pod: stages %v with delays %v, want pod-ready-0..2 with %v", names(t, lc, newPod), got, want)
	}
	jobPod := map[string]any{
		"metadata": map[string]any{"name": "job-1", "ownerReferences": []any{map[string]any{"kind": "Job"}}},
		"spec":     map[string]any{"nodeName": "fake"},
		"status":   map[string]any{"phase": "Running", "podIP": "10.0.0.1"},
	}
	if got := delays(t, lc, jobPod); !slices.Equal(names(t, lc, jobPod), []string{"pod-complete-0", "pod-complete-1", "pod-complete-2"}) || !slices.Equal(got, want) {
		t.Errorf("a running Job's pod: stages %v with delays %v, want pod-complete-0..2 with %v", names(t, lc, jobPod), got, want)
	}
	deleted := map[string]any{
		"metadata": map[string]any{"name": "web-1", "deletionTimestamp": "2026-01-01T00:00:00Z"},
		"status":   map[string]any{"phase": "Running", "podIP": "10.0.0.1"},
	}
	if got := names(t, lc, deleted); !slices.Equal(got, []string{"pod-delete"}) {
		t.Errorf("a deleted pod: stages %v, want pod-delete", got)
	}
}

// Without completion times a Job's pod completes at once, as with kwok's
// default stages, and a trace with no ended pods is an error.
func TestKwokStagesDefaults(t *testing.T) {
	lc := loadStages(t, fleetrollout.KwokStages([]time.Duration{time.Second}, nil))
	jobPod := map[string]any{
		"metadata": map[string]any{"name": "job-1", "ownerReferences": []any{map[string]any{"kind": "Job"}}},
		"status":   map[string]any{"phase": "Running", "podIP": "10.0.0.1"},
	}
	if got, d := names(t, lc, jobPod), delays(t, lc, jobPod); !slices.Equal(got, []string{"pod-complete"}) || !slices.Equal(d, []time.Duration{0}) {
		t.Errorf("a running Job's pod: stages %v with delays %v, want pod-complete at once", got, d)
	}
	if _, err := fleetrollout.KwokStagesFromTrace(&sktrace.Trace{}, "", 3, 10); err == nil {
		t.Error("a trace with no ended pods made stages")
	}
}

func loadStages(t *testing.T, y string) lifecycle.Lifecycle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stages.yaml")
	if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	objs, err := config.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("kwok cannot load the stages: %v\n%s", err, y)
	}
	var stages []*internalversion.Stage
	for _, o := range objs {
		s, ok := o.(*internalversion.Stage)
		if !ok {
			t.Fatalf("not a Stage: %T", o)
		}
		stages = append(stages, s)
	}
	lc, err := lifecycle.NewLifecycle(stages)
	if err != nil {
		t.Fatal(err)
	}
	return lc
}

func matching(t *testing.T, lc lifecycle.Lifecycle, pod map[string]any) []*lifecycle.Stage {
	t.Helper()
	ss, err := lc.ListAllPossible(context.Background(), &lifecycle.Event{Data: pod})
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func names(t *testing.T, lc lifecycle.Lifecycle, pod map[string]any) []string {
	var out []string
	for _, s := range matching(t, lc, pod) {
		out = append(out, s.Name())
	}
	slices.Sort(out)
	return out
}

func delays(t *testing.T, lc lifecycle.Lifecycle, pod map[string]any) []time.Duration {
	var out []time.Duration
	ss := matching(t, lc, pod)
	slices.SortFunc(ss, func(a, b *lifecycle.Stage) int { return strings.Compare(a.Name(), b.Name()) })
	for _, s := range ss {
		d, _, err := s.Delay(context.Background(), &lifecycle.Event{Data: pod}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}
