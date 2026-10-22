package sktrace

import (
	"fmt"
	"slices"
	"time"
)

// An Env is what a replay's target has before the trace's first event.
type Env struct {
	// Kinds are the kinds the target serves, as GVKs ("apps/v1.Deployment");
	// nil: any.
	Kinds []string
	// Namespaces are the namespaces that exist.
	Namespaces []string
}

// A Finding is a trace's dependency the target lacks when an event needs
// it.
type Finding struct {
	At     time.Duration // the event's offset in the trace
	Object string        // GVK namespace/name
	What   string
	Fix    string
}

func (f Finding) String() string {
	return fmt.Sprintf("%v %s: %s (%s)", f.At, f.Object, f.What, f.Fix)
}

// Lint checks, event by event, that what the trace applies can be applied
// to a target that starts as env: its namespace exists (in env, or created
// earlier in the trace), its kind is served, its owners were applied
// before it, and what it deletes was applied. A replay that skips these
// fails at the API server, or silently replays less than the trace
// recorded.
func (t *Trace) Lint(env Env) []Finding {
	var out []Finding
	namespaces := map[string]bool{"": true}
	for _, n := range env.Namespaces {
		namespaces[n] = true
	}
	applied := map[string]bool{}
	id := func(o Object) string { return o.GVK() + " " + o.Key() }
	for _, e := range t.Events {
		at := t.Offset(e)
		for _, o := range e.Applied {
			if o.Kind() == "Namespace" {
				namespaces[o.Name()] = true
			}
		}
		for _, o := range e.Applied {
			if !namespaces[o.Namespace()] {
				out = append(out, Finding{at, id(o), "namespace " + o.Namespace() + " does not exist",
					"create it before the replay (Env.Namespaces), or trace the Namespace too"})
				namespaces[o.Namespace()] = true // reported once
			}
			if env.Kinds != nil && !slices.Contains(env.Kinds, o.GVK()) {
				out = append(out, Finding{at, id(o), "kind " + o.GVK() + " is not served by the target",
					"install its CRD or controller, or drop the kind from the replay"})
			}
			for _, owner := range owners(o) {
				if !applied[owner] {
					out = append(out, Finding{at, id(o), "owner " + owner + " was not applied before it",
						"replay its owner first, or strip ownerReferences: a garbage collector deletes orphans"})
				}
			}
			applied[o.Kind()+" "+o.Key()] = true
		}
		for _, o := range e.Deleted {
			if !applied[o.Kind()+" "+o.Key()] {
				out = append(out, Finding{at, id(o), "deleted but never applied",
					"the trace starts after the object was created: create it before the replay, or skip the delete"})
			}
			delete(applied, o.Kind()+" "+o.Key())
		}
	}
	return out
}

// owners are an object's owner references, as kind namespace/name.
func owners(o Object) []string {
	meta, _ := o["metadata"].(map[string]any)
	refs, _ := meta["ownerReferences"].([]any)
	var out []string
	for _, r := range refs {
		m, _ := r.(map[string]any)
		kind, _ := m["kind"].(string)
		name, _ := m["name"].(string)
		if kind != "" && name != "" {
			out = append(out, kind+" "+o.Namespace()+"/"+name)
		}
	}
	return out
}
