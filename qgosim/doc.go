// Package qgosim runs qenv deployments, and whole programs, in gosim
// (https://github.com/jellevandenhooff/gosim), on a patched gosim
// (./fetch-gosim.sh), with every choice made by a qenv Chooser in the test
// process (DESIGN.md §14, §15.1).
//
// A simulated test is an ordinary gosim test in a package of its own,
// built with the sim tag. It makes a simulation with New (or uses Chooser,
// Platform and Sink directly) and records steps with qobs as usual. The
// steps reach the host, where a normal test drives the simulated one
// with Run or, for qtest.Explore, Scenario, and checks the steps against
// the model:
//
//	// checkout/sim_test.go, //go:build sim
//	func TestCheckout(t *testing.T) {
//		s := qgosim.New()
//		checkout.RunOn(s, config())
//	}
//
//	// checkout/host_test.go
//	qtest.Explore(t, binding, x, qgosim.Scenario(t, pkg, "TestCheckout"))
//
// The package lives in a module of its own because it depends on gosim.
package qgosim
