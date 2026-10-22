package qobs

import (
	"context"
	"sort"

	"github.com/casselc/quint-connect-go/qtrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// OTelSink emits each step as a short span "quint <action>" carrying one
// span event "quint.step" with the schema attributes (see qtrace). The span
// is a child of the span in ctx, so steps show up inside the application's
// own traces.
//
// Give it a TracerProvider whose sampler keeps every span: a sampled-out
// step is a hole in the trace, which the reconstructor reports (quint.seq is
// dense) but cannot fill.
type OTelSink struct{ Tracer trace.Tracer }

// NewOTelSink returns a sink on tp's "quintgo" tracer.
func NewOTelSink(tp trace.TracerProvider) *OTelSink {
	return &OTelSink{Tracer: tp.Tracer("github.com/casselc/quint-connect-go/qobs")}
}

func (o *OTelSink) Record(ctx context.Context, s *qtrace.Step) {
	_, span := o.Tracer.Start(ctx, qtrace.SpanName+s.Action, trace.WithTimestamp(s.Time))
	span.AddEvent(qtrace.EventName, trace.WithTimestamp(s.Time), trace.WithAttributes(Attributes(s)...))
	span.End(trace.WithTimestamp(s.Time))
}

// Attributes encodes a step in the schema: the native step log's
// attributes (qtrace.Step.Attributes) as OTel attributes, but its time,
// which is the event's timestamp. One encoding serves both formats.
func Attributes(s *qtrace.Step) []attribute.KeyValue {
	m := s.Attributes()
	delete(m, qtrace.KeyTime)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		kv = append(kv, attr(k, m[k]))
	}
	return kv
}

func attr(k string, v any) attribute.KeyValue {
	switch x := v.(type) {
	case int64:
		return attribute.Int64(k, x)
	case bool:
		return attribute.Bool(k, x)
	case float64:
		return attribute.Float64(k, x)
	case string:
		return attribute.String(k, x)
	}
	return attribute.String(k, "")
}
