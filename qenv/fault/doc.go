// Package fault holds the aspects that make //quint:fault functions fault
// points of a qenv simulation (orchestrion.yml); a module opts in by
// importing it in its tool file and weaving with qweave (the aspect uses
// Orchestrion's schema, but is checked with qweave only).
//
//	//quint:fault kind:db.write error:ErrUnavailable error:ErrConflict crash:true
//	func (s *Store) Write(ctx context.Context, k, v string) error
//
// At the function's entry the woven code calls qenv.FaultPoint: in a
// simulation a fault choice of kind "db.write" between going on (option 0),
// returning one of the named errors (expressions of the function's own
// package) with zero values for the other results, and, with crash:true,
// crashing the process there. A function with no error result can only
// crash. Outside a simulation the call returns nil. Each woven function
// also declares its point at init (qenv.DeclareFaultSite), so
// qenv.CoverageOf (qtest.Exploration.Coverage) names the points no run
// reached.
package fault
