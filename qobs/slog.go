package qobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ActionKey is the slog attribute that makes a log record a model step
// (SlogHandler).
const ActionKey = "quint.action"

// A SlogHandler records model steps from an application's structured
// logs: a record with the attribute quint.action is a step of that action,
// recorded into the run of the record's context (Record), and every record
// goes on to Next. The record's other attributes are the step's fields as
// Record takes them ("obs.balance", "order.lamport", arguments by name;
// groups join with "."), and an attribute named "error" (an error, or a
// string) is the step's error. So a service that already logs what it did
// becomes checkable without a line of instrumentation besides the key:
//
//	slog.InfoContext(ctx, "deposited", "quint.action", "deposit", "account", acct, "amount", n)
//
// A step is recorded whatever Next's level: the model needs every step,
// the log only what it keeps.
type SlogHandler struct {
	Next   slog.Handler // nil: steps only
	attrs  []slog.Attr  // from WithAttrs, with their groups' prefixes
	prefix string       // from WithGroup
}

// NewSlogHandler returns a handler recording steps and passing records to
// next (nil: none).
func NewSlogHandler(next slog.Handler) *SlogHandler { return &SlogHandler{Next: next} }

// Enabled implements slog.Handler: every level, since a step at a level
// the log drops is still a step.
func (h *SlogHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle implements slog.Handler.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	var action string
	var err error
	var kv []any
	add := func(k string, v slog.Value) {
		switch {
		case k == ActionKey:
			action = v.String()
		case k == "error":
			if e, ok := v.Any().(error); ok {
				err = e
			} else {
				err = errors.New(v.String())
			}
		default:
			kv = append(kv, k, v.Any())
		}
	}
	var walk func(prefix string, a slog.Attr)
	walk = func(prefix string, a slog.Attr) {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			p := prefix + a.Key + "."
			if a.Key == "" {
				p = prefix
			}
			for _, g := range v.Group() {
				walk(p, g)
			}
			return
		}
		if v.Kind() == slog.KindTime || v.Kind() == slog.KindDuration || v.Kind() == slog.KindAny {
			if _, isErr := v.Any().(error); !isErr {
				v = slog.StringValue(fmt.Sprint(v.Any()))
			}
		}
		add(prefix+a.Key, v)
	}
	for _, a := range h.attrs {
		walk("", a)
	}
	r.Attrs(func(a slog.Attr) bool { walk(h.prefix, a); return true })
	if action != "" {
		Record(ctx, action, err, kv...)
	}
	if h.Next != nil && h.Next.Enabled(ctx, r.Level) {
		return h.Next.Handle(ctx, r)
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *SlogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range as {
		if h.prefix != "" {
			a.Key = h.prefix + a.Key
		}
		c.attrs = append(c.attrs, a)
	}
	if h.Next != nil {
		c.Next = h.Next.WithAttrs(as)
	}
	return &c
}

// WithGroup implements slog.Handler.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	if h.Next != nil {
		c.Next = h.Next.WithGroup(name)
	}
	return &c
}
