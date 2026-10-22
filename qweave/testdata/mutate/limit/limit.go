// Package limit has no mutation code of its own: qweave -mutate adds it.
package limit

// Limiter admits up to Max requests.
type Limiter struct {
	Max, n int
	Log    []string
}

// Admit admits a request if fewer than Max are in.
func (l *Limiter) Admit() bool {
	if l.n < l.Max {
		l.n++
		l.note("admit")
		return true
	}
	return false
}

// Full is whether Max are in: a standalone comparison.
func (l *Limiter) Full() bool { return l.n >= l.Max }

func (l *Limiter) note(s string) { l.Log = append(l.Log, s) }
