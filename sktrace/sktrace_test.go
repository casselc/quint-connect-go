package sktrace_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/sktrace"
)

func read(t *testing.T, name string) *sktrace.Trace {
	t.Helper()
	tr, err := sktrace.ReadFile("testdata/" + name + ".sktrace")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// SimKube's CronJob trace: the CronJob applied twice and deleted. Its
// second version runs longer jobs: the index keys pods by the owner's
// version, so the first version's pods take 30 s and the second's 45 s.
func TestCronJob(t *testing.T) {
	tr := read(t, "cronjob")
	if len(tr.Events) != 3 || tr.End().Sub(tr.Start()) != 5*time.Minute+32*time.Second {
		t.Fatalf("%d events over %v", len(tr.Events), tr.End().Sub(tr.Start()))
	}
	o := tr.Events[0].Applied[0]
	if o.GVK() != "batch/v1.CronJob" || o.Key() != "testing/hello-simkube" {
		t.Errorf("first object %s %s", o.GVK(), o.Key())
	}
	if d := tr.Events[2].Deleted; len(d) != 1 || d[0].Name() != "hello-simkube" {
		t.Errorf("deleted %v", d)
	}
	if len(tr.Lifecycles) != 5 {
		t.Fatalf("%d lifecycles", len(tr.Lifecycles))
	}
	v1 := tr.Lifecycles[0].Mtime
	for _, l := range tr.Lifecycles {
		want := 45 * time.Second
		if l.Mtime == v1 {
			want = 30 * time.Second
		}
		if l.Owner != "testing/hello-simkube" || l.Duration() != want {
			t.Errorf("lifecycle %+v: %v, want %v", l, l.Duration(), want)
		}
	}
	rates := tr.Rates(10*time.Second, sktrace.Applied("CronJob"))
	sum := 0.0
	for _, r := range rates {
		sum += r * 10
	}
	if sum != 2 {
		t.Errorf("rates count %g CronJob applies, want 2", sum)
	}
	if tr.Config["trackedObjects"] == nil {
		t.Error("no tracer config")
	}
}

// SimKube's scaling trace: desired replicas over time, as a gauge.
func TestScalingGauge(t *testing.T) {
	g := read(t, "dsb-scaling").Gauge(time.Minute, sktrace.Replicas)
	if len(g) != 19 || g[0] != 27 || slices.Max(g) != 52 || g[len(g)-1] != 42 {
		t.Errorf("replicas per minute %v", g)
	}
}

// Bare pods: one start per minute or so.
func TestStarts(t *testing.T) {
	s := read(t, "bare-pods").Starts(time.Minute)
	sum := 0.0
	for _, r := range s {
		sum += r * 60
	}
	if sum != 4 {
		t.Errorf("starts %v", s)
	}
}

// A file that is not a v3 trace says so.
func TestNotATrace(t *testing.T) {
	for _, b := range [][]byte{{0x81, 0xa7, 'v', 'e', 'r', 's', 'i', 'o', 'n', 0x02}, {0x91}, {0xc1}} {
		if _, err := sktrace.Read(bytes.NewReader(b)); err == nil || !strings.HasPrefix(err.Error(), "sktrace: ") {
			t.Errorf("% x: %v", b, err)
		}
	}
}
