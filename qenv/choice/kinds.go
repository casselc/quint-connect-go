package choice

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// The fault kinds of a process: each one's options' names (Declare) and
// the sites that declared it (DeclareSite), in one registry, so a choice,
// a coverage report and a script name an option the same way.
var registry struct {
	sync.Mutex
	kinds map[string]*kind
}

type kind struct {
	options []string // nil: not declared by name
	named   bool
	sites   []string
}

func kindOf(name string) *kind {
	name = strings.TrimPrefix(name, "fault.")
	if registry.kinds == nil {
		registry.kinds = map[string]*kind{}
	}
	k := registry.kinds[name]
	if k == nil {
		k = &kind{}
		registry.kinds[name] = k
	}
	return k
}

// Declare names a fault kind's options in order (option 0, nominal,
// unnamed). Declaring it again with other options panics: option numbers
// would change meaning.
func Declare(name string, options ...string) {
	registry.Lock()
	defer registry.Unlock()
	k := kindOf(name)
	if k.named && !slices.Equal(k.options, options) {
		panic(fmt.Sprintf("qenv: fault kind %s declared with options %v, then %v", strings.TrimPrefix(name, "fault."), k.options, options))
	}
	k.options, k.named = append([]string(nil), options...), true
}

// Options are a declared kind's option names (with or without the
// "fault." prefix), or nil.
func Options(name string) []string {
	registry.Lock()
	defer registry.Unlock()
	if k := registry.kinds[strings.TrimPrefix(name, "fault.")]; k != nil && k.named {
		return append([]string(nil), k.options...)
	}
	return nil
}

// OptionName is the name of a kind's option v (1-based), or "".
func OptionName(name string, v int) string {
	if o := Options(name); v >= 1 && v <= len(o) {
		return o[v-1]
	}
	return ""
}

// DeclareSite registers a site (a function, a position) of a fault kind:
// what woven code does at init, so a coverage report can name the points
// no run reached.
func DeclareSite(name, site string) {
	registry.Lock()
	defer registry.Unlock()
	k := kindOf(name)
	if !slices.Contains(k.sites, site) {
		k.sites = append(k.sites, site)
	}
}

// Sites are the declared sites: kind -> sites.
func Sites() map[string][]string {
	registry.Lock()
	defer registry.Unlock()
	out := map[string][]string{}
	for n, k := range registry.kinds {
		if len(k.sites) > 0 {
			out[n] = append([]string(nil), k.sites...)
		}
	}
	return out
}
