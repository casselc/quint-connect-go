// Package qsweep generates covering arrays: few rows of a parameter space
// in which every combination of t factors' values appears (t-way coverage;
// pairwise for t = 2), and sequence covering arrays, in which every order
// of t events appears. Most failures need only a few factors to interact,
// so a sweep over such rows finds what the full product would, at a small
// fraction of its runs (qtest.System.Sweep).
//
// Rows are chosen greedily from the whole space, so a constraint (Allow)
// is any function of a full row, and combinations it rules out are not
// required. Generation is deterministic.
package qsweep

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
)

// A Factor is a parameter and its values.
type Factor struct {
	Name   string
	Values []string
	// Model, if set, is the model constant the factor's value is (a Quint
	// expression: an integer, a quoted string): a row's runs are checked
	// against the model with it (qtest.Sweep), so a size given to the
	// system and to the model is written once and cannot disagree.
	Model string
}

// Constant returns f naming the model constant its value is (Model).
func (f Factor) Constant(name string) Factor { f.Model = name; return f }

// Constants are the model constants of r's factors that name one (Model),
// with the row's values.
func (r Row) Constants() map[string]string {
	m := map[string]string{}
	for i, f := range r.factors {
		if f.Model != "" {
			m[f.Model] = f.Values[r.idx[i]]
		}
	}
	return m
}

// Bool is a factor with values "off" and "on" (Row.On).
func Bool(name string) Factor { return Factor{Name: name, Values: []string{"off", "on"}} }

// Ints is a factor with integer values (Row.Int).
func Ints(name string, values ...int) Factor {
	f := Factor{Name: name}
	for _, v := range values {
		f.Values = append(f.Values, strconv.Itoa(v))
	}
	return f
}

// A Row is one assignment of a value to every factor.
type Row struct {
	factors []Factor
	idx     []int
}

func (r Row) index(name string) int {
	for i, f := range r.factors {
		if f.Name == name {
			return i
		}
	}
	panic(fmt.Sprintf("qsweep: no factor %q (factors: %s)", name, r.names()))
}

func (r Row) names() string {
	var n []string
	for _, f := range r.factors {
		n = append(n, f.Name)
	}
	return strings.Join(n, ", ")
}

// Get is the row's value of a factor.
func (r Row) Get(name string) string {
	i := r.index(name)
	return r.factors[i].Values[r.idx[i]]
}

// Int is the row's value of an Ints factor.
func (r Row) Int(name string) int {
	v, err := strconv.Atoi(r.Get(name))
	if err != nil {
		panic(fmt.Sprintf("qsweep: factor %q: %v", name, err))
	}
	return v
}

// On is the row's value of a Bool factor.
func (r Row) On(name string) bool { return r.Get(name) == "on" }

// String is the row as name=value pairs, usable as a subtest name.
func (r Row) String() string {
	var b strings.Builder
	for i, f := range r.factors {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%s", f.Name, f.Values[r.idx[i]])
	}
	return b.String()
}

// MaxSpace bounds the full space Cover enumerates.
const MaxSpace = 1 << 21

// Cover returns rows covering every t-way combination of the factors'
// values that some allowed row contains (allow nil: every row is). t is
// clamped to [1, len(factors)].
func Cover(factors []Factor, t int, allow func(Row) bool) ([]Row, error) {
	k := len(factors)
	if k == 0 {
		return nil, nil
	}
	t = min(max(t, 1), k)
	size := 1
	for _, f := range factors {
		if len(f.Values) == 0 {
			return nil, fmt.Errorf("qsweep: factor %q has no values", f.Name)
		}
		size *= len(f.Values)
		if size > MaxSpace {
			return nil, fmt.Errorf("qsweep: the space has more than %d rows: use fewer factors or values, or split it into sweeps", MaxSpace)
		}
	}
	// every allowed row of the space
	var space [][]int
	idx := make([]int, k)
	for {
		row := append([]int(nil), idx...)
		if allow == nil || allow(Row{factors, row}) {
			space = append(space, row)
		}
		i := k - 1
		for ; i >= 0; i-- {
			idx[i]++
			if idx[i] < len(factors[i].Values) {
				break
			}
			idx[i] = 0
		}
		if i < 0 {
			break
		}
	}
	if len(space) == 0 {
		return nil, fmt.Errorf("qsweep: no row is allowed")
	}
	// t-subsets of factors, and each tuple's number: offset of its subset
	// plus its values in mixed radix
	subsets := combinations(k, t)
	offset := make([]int, len(subsets)+1)
	for i, s := range subsets {
		n := 1
		for _, f := range s {
			n *= len(factors[f].Values)
		}
		offset[i+1] = offset[i] + n
	}
	tuples := func(row []int, fn func(int)) {
		for i, s := range subsets {
			n := 0
			for _, f := range s {
				n = n*len(factors[f].Values) + row[f]
			}
			fn(offset[i] + n)
		}
	}
	// the tuples some allowed row has are the ones to cover
	need := make([]bool, offset[len(subsets)])
	left := 0
	for _, row := range space {
		tuples(row, func(n int) {
			if !need[n] {
				need[n] = true
				left++
			}
		})
	}
	var out []Row
	for left > 0 {
		best, bestN := -1, 0
		for i, row := range space {
			c := 0
			tuples(row, func(n int) {
				if need[n] {
					c++
				}
			})
			if c > bestN {
				best, bestN = i, c
			}
		}
		tuples(space[best], func(n int) {
			if need[n] {
				need[n] = false
				left--
			}
		})
		out = append(out, Row{factors, space[best]})
	}
	return out, nil
}

func combinations(n, t int) [][]int {
	var out [][]int
	var rec func(start int, cur []int)
	rec = func(start int, cur []int) {
		if len(cur) == t {
			out = append(out, append([]int(nil), cur...))
			return
		}
		for i := start; i < n; i++ {
			rec(i+1, append(cur, i))
		}
	}
	rec(0, nil)
	return out
}

// Sequences returns orders of events in which every ordered choice of t
// distinct events appears in that order (as a subsequence): a sequence
// covering array. For t = 2 an order and its reverse suffice; for larger t
// orders are chosen greedily from seeded permutations.
func Sequences(events []string, t int) [][]string {
	n := len(events)
	if n == 0 {
		return nil
	}
	t = min(max(t, 1), n)
	if t == 1 {
		return [][]string{append([]string(nil), events...)}
	}
	if t == 2 {
		rev := make([]string, n)
		for i, e := range events {
			rev[n-1-i] = e
		}
		return [][]string{append([]string(nil), events...), rev}
	}
	key := func(seq []int) int {
		k := 0
		for _, e := range seq {
			k = k*n + e
		}
		return k
	}
	covered := map[int]bool{}
	total := 1
	for i := 0; i < t; i++ {
		total *= n - i
	}
	// the ordered t-tuples a permutation covers: each t-subset of its
	// positions, in its order
	newIn := func(perm []int, mark bool) int {
		c := 0
		for _, pos := range combinations(n, t) {
			seq := make([]int, t)
			for i, p := range pos {
				seq[i] = perm[p]
			}
			if k := key(seq); !covered[k] {
				c++
				if mark {
					covered[k] = true
				}
			}
		}
		return c
	}
	rnd := rand.New(rand.NewPCG(uint64(n), uint64(t)))
	var out [][]string
	for len(covered) < total {
		var best []int
		bestN := -1
		for range 64 {
			perm := rnd.Perm(n)
			if c := newIn(perm, false); c > bestN {
				best, bestN = perm, c
			}
		}
		newIn(best, true)
		seq := make([]string, n)
		for i, e := range best {
			seq[i] = events[e]
		}
		out = append(out, seq)
	}
	return out
}

// Missing returns the t-way combinations of factor values that some
// allowed row has and no row of rows covers, as "name=value,..." strings,
// sorted: what a set of rows (Cover's, or rows chosen by hand) leaves out.
func Missing(factors []Factor, t int, allow func(Row) bool, rows []Row) ([]string, error) {
	all, err := Cover(factors, t, allow)
	if err != nil {
		return nil, err
	}
	t = min(max(t, 1), len(factors))
	have := map[string]bool{}
	desc := func(r Row, s []int) string {
		var parts []string
		for _, f := range s {
			parts = append(parts, fmt.Sprintf("%s=%s", factors[f].Name, factors[f].Values[r.idx[f]]))
		}
		return strings.Join(parts, ",")
	}
	subsets := combinations(len(factors), t)
	for _, r := range rows {
		for _, s := range subsets {
			have[desc(r, s)] = true
		}
	}
	miss := map[string]bool{}
	for _, r := range all {
		for _, s := range subsets {
			if d := desc(r, s); !have[d] {
				miss[d] = true
			}
		}
	}
	var out []string
	for d := range miss {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
