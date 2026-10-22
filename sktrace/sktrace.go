// Package sktrace reads SimKube traces (https://github.com/acrlabs/simkube,
// trace format v3): what happened to a real cluster's objects over time,
// captured by sk-tracer. They feed simulations measured workloads: the
// objects applied and deleted, as timed tasks against a fake API server
// (examples/fleetrollout), and the rates of events as a load shape
// (qload.Piecewise) for the load simulation and the CTMC's calibration.
//
// A trace records the desired state of tracked objects, in whole seconds,
// with no actor and no causal order: a workload, not a run to check
// against a model.
package sktrace

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Trace is a decoded trace file.
type Trace struct {
	Version int
	// Config is sk-tracer's configuration: the tracked kinds.
	Config map[string]any
	Events []Event
	// Lifecycles are the pods' lifetimes, from the trace's index.
	Lifecycles []Lifecycle
}

// An Event is what changed at one second.
type Event struct {
	TS               int64 // Unix seconds
	Applied, Deleted []Object
}

// An Object is a Kubernetes object, as decoded (map[string]any).
type Object map[string]any

func (o Object) str(path ...string) string {
	var v any = map[string]any(o)
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

// Kind is the object's kind.
func (o Object) Kind() string { return o.str("kind") }

// APIVersion is the object's apiVersion.
func (o Object) APIVersion() string { return o.str("apiVersion") }

// GVK is the object's group, version and kind as sk-tracer's config names
// them ("apps/v1.Deployment").
func (o Object) GVK() string { return o.APIVersion() + "." + o.Kind() }

// Namespace is the object's namespace.
func (o Object) Namespace() string { return o.str("metadata", "namespace") }

// Name is the object's name.
func (o Object) Name() string { return o.str("metadata", "name") }

// Key is namespace/name.
func (o Object) Key() string { return o.Namespace() + "/" + o.Name() }

// A Lifecycle is one pod's lifetime, owned by an object of the trace.
type Lifecycle struct {
	GVK, Owner string // the owning object: kind, namespace/name
	Mtime      int64  // the owner's modification time it belongs to
	Start, End int64  // Unix seconds; End 0 while running
}

// Duration is the pod's lifetime (0 while running).
func (l Lifecycle) Duration() time.Duration {
	if l.End == 0 {
		return 0
	}
	return time.Duration(l.End-l.Start) * time.Second
}

// ReadFile reads a trace file.
func ReadFile(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Read decodes a trace.
func Read(r io.Reader) (*Trace, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	v, _, err := decode(b)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sktrace: not a trace (a %T at the top)", v)
	}
	t := &Trace{}
	ver, _ := top["version"].(int64)
	t.Version = int(ver)
	if t.Version != 3 {
		return nil, fmt.Errorf("sktrace: trace format version %d; this reader knows version 3", t.Version)
	}
	t.Config, _ = top["config"].(map[string]any)
	evs, _ := top["events"].([]any)
	for i, e := range evs {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sktrace: event %d is a %T", i, e)
		}
		ts, _ := m["ts"].(int64)
		t.Events = append(t.Events, Event{TS: ts, Applied: objects(m["applied_objs"]), Deleted: objects(m["deleted_objs"])})
	}
	t.Lifecycles = lifecycles(top["index"])
	return t, nil
}

func objects(v any) []Object {
	a, _ := v.([]any)
	var out []Object
	for _, o := range a {
		if m, ok := o.(map[string]any); ok {
			out = append(out, Object(m))
		}
	}
	return out
}

// lifecycles flattens the index: GVK -> owner -> mtime -> pod data.
func lifecycles(v any) []Lifecycle {
	idx, _ := v.(map[string]any)
	var out []Lifecycle
	for _, gvk := range sortedKeys(idx) {
		owners, _ := idx[gvk].(map[string]any)
		for _, owner := range sortedKeys(owners) {
			mtimes, _ := owners[owner].(map[string]any)
			for _, mt := range sortedKeys(mtimes) {
				mtime, _ := strconv.ParseInt(mt, 10, 64)
				pods, _ := mtimes[mt].([]any)
				for _, p := range pods {
					l := Lifecycle{GVK: gvk, Owner: owner, Mtime: mtime}
					pm, _ := p.(map[string]any)
					lc, _ := pm["lifecycle"].(map[string]any)
					if f, ok := lc["Finished"].([]any); ok && len(f) == 2 {
						l.Start, _ = f[0].(int64)
						l.End, _ = f[1].(int64)
					} else if r, ok := lc["Running"].(int64); ok {
						l.Start = r
					}
					out = append(out, l)
				}
			}
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Start and End are the times of the first and last events.
func (t *Trace) Start() time.Time { return t.at(0) }

// End is the time of the last event.
func (t *Trace) End() time.Time { return t.at(len(t.Events) - 1) }

func (t *Trace) at(i int) time.Time {
	if i < 0 || i >= len(t.Events) {
		return time.Time{}
	}
	return time.Unix(t.Events[i].TS, 0).UTC()
}

// Offset is when an event happened, from the trace's start.
func (t *Trace) Offset(e Event) time.Duration {
	if len(t.Events) == 0 {
		return 0
	}
	return time.Duration(e.TS-t.Events[0].TS) * time.Second
}

// Rates are counts per second of tick, tick by tick from the trace's
// start: count says what an event contributes (its applied objects of a
// kind, its deletions).
func (t *Trace) Rates(tick time.Duration, count func(Event) int) []float64 {
	if len(t.Events) == 0 || tick <= 0 {
		return nil
	}
	n := int(t.Offset(t.Events[len(t.Events)-1])/tick) + 1
	out := make([]float64, n)
	for _, e := range t.Events {
		out[int(t.Offset(e)/tick)] += float64(count(e))
	}
	for i := range out {
		out[i] /= tick.Seconds()
	}
	return out
}

// Applied counts an event's applied objects of the given kinds (none:
// all): a count for Rates.
func Applied(kinds ...string) func(Event) int {
	return func(e Event) int {
		n := 0
		for _, o := range e.Applied {
			if len(kinds) == 0 || containsFold(kinds, o.Kind()) {
				n++
			}
		}
		return n
	}
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// Gauge is a value of the cluster's tracked objects over time, tick by
// tick from the trace's start: value summed over the objects that exist
// at the end of each tick (the latest version of each applied object,
// until deleted). Desired replicas over time is
// Gauge(tick, sktrace.Replicas).
func (t *Trace) Gauge(tick time.Duration, value func(Object) float64) []float64 {
	if len(t.Events) == 0 || tick <= 0 {
		return nil
	}
	n := int(t.Offset(t.Events[len(t.Events)-1])/tick) + 1
	out := make([]float64, n)
	live := map[string]Object{}
	e := 0
	for i := range out {
		end := time.Duration(i+1) * tick
		for ; e < len(t.Events) && t.Offset(t.Events[e]) < end; e++ {
			for _, o := range t.Events[e].Applied {
				live[o.GVK()+" "+o.Key()] = o
			}
			for _, o := range t.Events[e].Deleted {
				delete(live, o.GVK()+" "+o.Key())
			}
		}
		for _, o := range live {
			out[i] += value(o)
		}
	}
	return out
}

// Replicas is an object's spec.replicas (0 if it has none): a value for
// Gauge.
func Replicas(o Object) float64 {
	spec, _ := o["spec"].(map[string]any)
	n, _ := spec["replicas"].(int64)
	return float64(n)
}

// Starts are pod starts per second of tick, from the lifecycles: the
// arrival process of the cluster's work.
func (t *Trace) Starts(tick time.Duration) []float64 {
	if len(t.Events) == 0 || tick <= 0 {
		return nil
	}
	t0 := t.Events[0].TS
	var out []float64
	for _, l := range t.Lifecycles {
		if l.Start < t0 {
			continue
		}
		i := int(time.Duration(l.Start-t0) * time.Second / tick)
		for len(out) <= i {
			out = append(out, 0)
		}
		out[i] += 1 / tick.Seconds()
	}
	return out
}

// Lifetimes are the durations of the pods that ended, owned by objects of
// kind gvk ("": any), in the order the trace lists them.
func (t *Trace) Lifetimes(gvk string) []time.Duration {
	var out []time.Duration
	for _, l := range t.Lifecycles {
		if (gvk == "" || l.GVK == gvk) && l.End != 0 {
			out = append(out, l.Duration())
		}
	}
	return out
}

// Quantiles are n durations spread over ds's distribution, ascending: the
// options of a simulated delay drawn from a measured one (the first, the
// shortest, is the nominal choice). Nil if ds is empty.
func Quantiles(ds []time.Duration, n int) []time.Duration {
	if len(ds) == 0 || n < 1 {
		return nil
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	out := make([]time.Duration, n)
	for i := range out {
		q := 0.0
		if n > 1 {
			q = float64(i) / float64(n-1)
		}
		out[i] = s[int(q*float64(len(s)-1)+0.5)]
	}
	return out
}
