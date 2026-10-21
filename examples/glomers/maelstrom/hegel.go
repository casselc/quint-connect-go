package maelstrom

import (
	"fmt"

	"hegel.dev/go/hegel"
)

// Network is a Hegel state machine fragment that schedules a Sim: embed it
// in a challenge's machine, next to the rules that send client requests,
// and Hegel explores deliveries in any order, lost messages, partitions,
// healing and timers, then shrinks a failure to the shortest schedule.
//
//	type machine struct {
//		maelstrom.Network
//	}
//	func (m *machine) RuleBroadcast(tc hegel.TestCase) { m.Sim.Request("c1", ..., ...) }
//	hegel.RunStateful(tc, &machine{Network: maelstrom.Network{Sim: sim, Faults: true}})
type Network struct {
	Sim *Sim
	// Faults enables RuleDrop and RulePartition; otherwise the network is
	// reliable (but still reorders).
	Faults bool
	// NoTimers disables RuleTick, for challenges without timers.
	NoTimers bool
}

// RuleDeliver delivers any message in flight.
func (n *Network) RuleDeliver(tc hegel.TestCase) {
	p := len(n.Sim.pending)
	tc.Assume(p > 0)
	i := hegel.Draw(tc, hegel.Integers(0, p-1))
	tc.Note(fmt.Sprintf("deliver %s", n.Sim.pending[i]))
	n.Sim.Deliver(i)
}

// RuleDrop loses any message in flight.
func (n *Network) RuleDrop(tc hegel.TestCase) {
	tc.Assume(n.Faults && len(n.Sim.pending) > 0)
	i := hegel.Draw(tc, hegel.Integers(0, len(n.Sim.pending)-1))
	tc.Note(fmt.Sprintf("drop %s", n.Sim.pending[i]))
	n.Sim.Drop(i)
}

// RulePartition splits the nodes in two.
func (n *Network) RulePartition(tc hegel.TestCase) {
	tc.Assume(n.Faults && !n.Sim.Partitioned())
	ids := n.Sim.IDs()
	var a, b []string
	for _, id := range ids {
		if hegel.Draw(tc, hegel.Booleans()) {
			a = append(a, id)
		} else {
			b = append(b, id)
		}
	}
	tc.Assume(len(a) > 0 && len(b) > 0)
	tc.Note(fmt.Sprintf("partition %v | %v", a, b))
	n.Sim.Partition(a, b)
}

// RuleHeal removes the partition.
func (n *Network) RuleHeal(tc hegel.TestCase) {
	tc.Assume(n.Sim.Partitioned())
	tc.Note("heal")
	n.Sim.Heal()
}

// RuleTick fires one node's timers.
func (n *Network) RuleTick(tc hegel.TestCase) {
	tc.Assume(!n.NoTimers)
	id := hegel.Draw(tc, hegel.SampledFrom(n.Sim.IDs()))
	tc.Note("tick " + id)
	n.Sim.Tick(id)
}
