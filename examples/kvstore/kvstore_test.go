package kvstore

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/convert"
	"github.com/casselc/quint-connect-go/examples/kvstore/kv"
	"github.com/casselc/quint-connect-go/otelio"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/quinthegel"
	"github.com/casselc/quint-connect-go/validate"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"hegel.dev/go/hegel"
)

// service starts the HTTP service on a fresh store, its spans going to sr.
func service(t testing.TB, m kv.Mutation, sr *tracetest.SpanRecorder) *kv.Client {
	var opts []sdktrace.TracerProviderOption
	if sr != nil {
		opts = append(opts, sdktrace.WithSpanProcessor(sr))
	}
	srv := httptest.NewServer(kv.NewServer(kv.NewStore(m), sdktrace.NewTracerProvider(opts...)).Handler())
	t.Cleanup(srv.Close)
	return &kv.Client{Base: srv.URL}
}

// ---- 1. the running service reports, the model checks ---------------------

// Concurrent load against the service. The store's model steps are
// recorded (qtest) and must form a behaviour of the model: every CAS
// outcome, revision and read consistent with some serial history, namely
// the one the store's lock produced.
func TestServiceUnderLoadConforms(t *testing.T) {
	qtest.RequireQuint(t)
	qtest.RecordAndCheck(t, "kv.binding.yaml", func(*qobs.Recorder) {
		cl := service(t, kv.Faithful, nil)
		if err := kv.Load(context.Background(), cl.Base, 8, 40, []string{"a", "b"}, 1); err != nil {
			t.Fatal(err)
		}
	})
}

// The server's plain OTel spans, from the same kind of load, converted by
// kv.convert.yaml. Spans of concurrent requests end in any order; kv.op
// (the store's linearization index) puts them back in the store's order.
func TestServiceSpansConform(t *testing.T) {
	qtest.RequireQuint(t)
	steps := loadSpans(t, kv.Faithful, 1)
	rep := check(t, steps)
	if !rep.OK() {
		t.Fatalf("spans of the faithful service do not conform:\n%s", rep.Summary())
	}
}

// The stale-CAS race only shows under concurrency; its signature is a CAS
// that succeeded with an expected version the model's store no longer
// had. Both the recorded steps and the converted spans show it.
func TestStaleCASCaughtUnderLoad(t *testing.T) {
	qtest.RequireQuint(t)
	for attempt := uint64(1); attempt <= 5; attempt++ {
		rep := check(t, loadSpans(t, kv.StaleCAS, attempt))
		if rep.OK() {
			continue // the race did not happen this time
		}
		f := rep.Actors[0].Failure
		if f == nil || f.Kind != "disabled" || f.Step.Action != "cas" || f.Step.Outcome != "ok" {
			t.Fatalf("want a successful CAS the model cannot take:\n%s", rep.Summary())
		}
		t.Logf("caught on load %d:\n%s", attempt, rep.Summary())
		return
	}
	t.Fatal("five concurrent loads never exposed the stale CAS")
}

func loadSpans(t *testing.T, m kv.Mutation, seed uint64) []qtrace.Step {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	cl := service(t, m, sr)
	if err := kv.Load(context.Background(), cl.Base, 8, 40, []string{"a", "b"}, seed); err != nil {
		t.Fatal(err)
	}
	rules, err := convert.Load("kv.convert.yaml")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := rules.Steps([]otelio.TracesData{otelio.SpansToTracesData(sr.Ended())})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func check(t *testing.T, steps []qtrace.Step) *validate.Report {
	t.Helper()
	c, err := quinthegel.Checker("kv.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Check(context.Background(), steps)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// ---- 2. the model drives the service ---------------------------------------

type driver struct {
	cl    *kv.Client
	store *kv.Store
	m     kv.Mutation
	t     testing.TB
}

func (d *driver) Step(s *connect.Step) error {
	ctx := context.Background()
	key := func() string { return connect.MustPick[string](s, "key") }
	val := func() string { return connect.MustPick[string](s, "value") }
	ignore := func(err error) error {
		if kv.IsConflict(err) || kv.IsNotFound(err) {
			return nil // the model's outcome is checked through the state
		}
		return err
	}
	return connect.Switch{
		"init": func(*connect.Step) error {
			d.store = kv.NewStore(d.m)
			srv := httptest.NewServer(kv.NewServer(d.store, nil).Handler())
			d.t.Cleanup(srv.Close)
			d.cl = &kv.Client{Base: srv.URL}
			return nil
		},
		"write": func(*connect.Step) error { _, err := d.cl.Put(ctx, key(), val()); return err },
		"cas": func(s *connect.Step) error {
			_, err := d.cl.CAS(ctx, key(), connect.MustPick[int64](s, "expected"), val())
			return ignore(err)
		},
		"del":  func(*connect.Step) error { _, err := d.cl.Delete(ctx, key()); return ignore(err) },
		"read": func(*connect.Step) error { _, err := d.cl.Get(ctx, key()); return ignore(err) },
	}.Step(s)
}

// modelState is the model's state, decoded with itf.Decode: store's
// entries into kv.Entry (by its json field names), and the revision.
type modelState struct {
	Store    map[string]kv.Entry
	Revision int64
}

func (d *driver) State() (any, error) {
	data, rev := d.store.Snapshot()
	return modelState{Store: data, Revision: rev}, nil
}

// Model traces drive the service through its HTTP API; the store must
// match the model after every request.
func TestModelDrivesService(t *testing.T) {
	qtest.RequireQuint(t)
	connect.Run(t, connect.Config{Spec: "model/kv.qnt", Main: "kvDesign", MaxSamples: 30, MaxSteps: 25},
		func() connect.Driver { return &driver{t: t} })
}

// ---- 3. Hegel drives the service, the model judges --------------------------

type machine struct {
	cl       *kv.Client
	versions *hegel.Pool[int64] // versions the machine has seen: plausible CAS expectations
}

var (
	keys   = hegel.SampledFrom([]string{"a", "b"})
	values = hegel.SampledFrom([]string{"x", "y", "z"})
)

func (m *machine) RulePut(tc hegel.TestCase) {
	if r, err := m.cl.Put(context.Background(), hegel.Draw(tc, keys), hegel.Draw(tc, values)); err == nil {
		m.versions.Add(tc, r.Revision)
	}
}

func (m *machine) RuleCAS(tc hegel.TestCase) {
	exp := int64(0)
	if !m.versions.IsEmpty() && hegel.Draw(tc, hegel.Booleans()) {
		exp = hegel.Draw(tc, m.versions.ValuesReusable())
	}
	if r, err := m.cl.CAS(context.Background(), hegel.Draw(tc, keys), exp, hegel.Draw(tc, values)); err == nil {
		m.versions.Add(tc, r.Revision)
	}
}

func (m *machine) RuleDelete(tc hegel.TestCase) {
	_, _ = m.cl.Delete(context.Background(), hegel.Draw(tc, keys))
}
func (m *machine) RuleGet(tc hegel.TestCase) {
	_, _ = m.cl.Get(context.Background(), hegel.Draw(tc, keys))
}

// Hegel's rules call the service over HTTP; the store's model steps go to
// each case's recorder (Global) and the runs are checked after the test.
func TestHegelDrivesService(t *testing.T) {
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "kv.binding.yaml", Mode: quinthegel.After, Global: true, AllowEmpty: true},
		func(ht *hegel.T, _ *quinthegel.Recorder) {
			hegel.RunStateful(ht, &machine{cl: service(ht, kv.Faithful, nil), versions: hegel.NewPool[int64](ht)},
				hegel.WithStatefulStepCount(15))
		}, hegel.WithTestCases(30), hegel.WithDatabase(""))
}

// The RevisionOnConflict mutant, with every case checked: Hegel shrinks to
// a failed CAS (the revision wrongly advances) caught by the next check of
// the revision.
func TestHegelShrinksRevisionOnConflict(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint dozens of times")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: "kv.binding.yaml", Global: true}, func(tc hegel.TestCase, _ *quinthegel.Recorder) {
		cl := service(t, kv.RevisionOnConflict, nil)
		hegel.RunStateful(tc, &machine{cl: cl, versions: hegel.NewPool[int64](tc)}, hegel.WithStatefulStepCount(10))
	}, hegel.WithTestCases(30), hegel.WithSeed(11))
	if last := v.Last(); len(v.Steps) != 1 || last.Action != "cas" || last.Outcome != "conflict" {
		t.Fatalf("want a single failed CAS:\n%s", quinthegel.Render(v.Steps))
	}
}
