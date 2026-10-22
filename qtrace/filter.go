package qtrace

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"time"
)

// A Filter selects and sanitizes recorded steps before they are kept or
// validated: production logs carry customers' identifiers and more steps
// than a check needs. Selection keeps whole actors' steps in a window;
// sanitizing replaces identifiers with pseudonyms that keep equality (so
// a binding's interning and the model's comparisons mean what they did)
// and drops what the binding does not read.
type Filter struct {
	// Actors and Actions, if set, keep the steps whose actor or action
	// matches one of the patterns (path.Match: "checkout-*").
	Actors, Actions []string
	// Since and Until, if set, keep the steps recorded in [Since, Until).
	Since, Until time.Time
	// Pseudonymize names the fields (arguments, "obs.<name>", "actor")
	// whose string values become pseudonyms: a salted hash, the same for
	// the same value. Salt keeps them from being reversed by guessing.
	Pseudonymize []string
	Salt         string
	// Keep, if set, lists by action the fields to keep (Binding.UsedFields:
	// the ones the binding reads); every other argument and observation,
	// and the error text, is dropped. Steps of actions it does not list
	// keep only their identity.
	Keep map[string][]string
}

func match(patterns []string, s string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

// Pseudonym is the filter's pseudonym for v.
func (f Filter) Pseudonym(v string) string {
	h := sha256.Sum256([]byte(f.Salt + "\x00" + v))
	return "p" + hex.EncodeToString(h[:6])
}

// Apply returns the selected steps, sanitized; steps are not modified.
func (f Filter) Apply(steps []Step) []Step {
	pseud := map[string]bool{}
	for _, k := range f.Pseudonymize {
		pseud[k] = true
	}
	var out []Step
	for _, s := range steps {
		if !match(f.Actors, s.Actor) || !match(f.Actions, s.Action) ||
			(!f.Since.IsZero() && s.Time.Before(f.Since)) || (!f.Until.IsZero() && !s.Time.Before(f.Until)) {
			continue
		}
		var keep map[string]bool
		if f.Keep != nil {
			keep = map[string]bool{}
			for _, k := range f.Keep[s.Action] {
				keep[k] = true
			}
			s.Error, s.Source = "", ""
		}
		fields := func(m map[string]any, prefix string) map[string]any {
			if m == nil {
				return nil
			}
			c := map[string]any{}
			for k, v := range m {
				name := prefix + k
				if keep != nil && !keep[name] {
					continue
				}
				if str, ok := v.(string); ok && pseud[name] {
					v = f.Pseudonym(str)
				}
				c[k] = v
			}
			return c
		}
		s.Args, s.Obs = fields(s.Args, ""), fields(s.Obs, "obs.")
		if pseud["actor"] && s.Actor != "" {
			s.Actor = f.Pseudonym(s.Actor)
		}
		out = append(out, s)
	}
	return out
}

// Fields parses a comma-separated list, for flags.
func Fields(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
