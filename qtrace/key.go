package qtrace

import "fmt"

// A Keyer renders steps as a verdict cache key, one step at a time: two
// runs whose keys are equal get the same verdict from the same instance.
// Recorders, processes and threads are numbered by first appearance, so
// runs that differ only in their generated ids (an incarnation's process
// name, a span id) share a key; the sequence numbers are left out, the
// steps' order is the key's.
type Keyer struct{ ids map[string]int }

// Step is the key of the next step.
func (k *Keyer) Step(s *Step) string {
	return fmt.Sprintf("%s|r%d|p%d|t%d|%s|%s|%v|%v;", s.Actor, k.id("r", s.Recorder), k.id("p", s.Process), k.id("t", s.Thread),
		s.Outcome, s.String(), s.Obs, s.Order)
}

func (k *Keyer) id(kind, name string) int {
	if k.ids == nil {
		k.ids = map[string]int{}
	}
	key := kind + ":" + name
	n, ok := k.ids[key]
	if !ok {
		n = len(k.ids)
		k.ids[key] = n
	}
	return n
}

// Key is the verdict cache key of a run (Keyer).
func Key(steps []Step) string {
	var k Keyer
	b := make([]byte, 0, 64*len(steps))
	for i := range steps {
		b = append(b, k.Step(&steps[i])...)
	}
	return string(b)
}
