package connect

import (
	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qenv"
)

// A Fault is what a model step stands for in a qenv simulation: a fault
// choice's kind and option (qenv.FaultPoint's kind, a double's such as
// "rpc.payments.response", or "nemesis.crash.<process>" for a crash).
// Recorded says the program records the step itself (a failed write
// recorded as such); a step it does not record (a crash) does not count
// toward the steps the next faults wait for.
type Fault struct {
	Kind   string
	Option int
	// Name names the option instead, for a declared kind (qenv.Faults).
	Name     string
	Recorded bool
}

// FaultMap maps the model's fault actions to their faults; a nil result
// means the step is not a fault after all.
type FaultMap map[string]func(s *Step) *Fault

// FaultScript reads a trace's faults into a qenv.Script: the trace drives
// a whole program, not through a driver but through its environment. The
// script makes the trace's faults, in order, each after the steps that
// precede it, and no others; the program makes every other step itself,
// and its recorded steps are then checked against the model
// (qtest.Validate). A run that cannot follow the trace leaves faults in
// Script.Remaining, or records steps the model rejects.
//
// The script waits on the run's own step recorder (the one in the
// simulation's context); give it a Base chooser for the schedule if it
// should not be the nominal one.
func FaultScript(tr *itf.Trace, faults FaultMap, opt ReplayOptions) (*qenv.Script, error) {
	sc := &qenv.Script{}
	recorded := 0
	err := Replay(tr, DriverFunc(func(s *Step) error {
		if s.Index == 0 {
			return nil
		}
		if f, ok := faults[s.Action]; ok {
			if fault := f(s); fault != nil {
				// in the trace's order: each fault follows the one before
				f := qenv.Scripted{Kind: fault.Kind, Option: fault.Option, Name: fault.Name, After: recorded}
				if n := len(sc.Faults); n > 0 {
					f.Follows = []int{n - 1}
				}
				sc.Faults = append(sc.Faults, f)
				if fault.Recorded {
					recorded++
				}
				return nil
			}
		}
		recorded++
		return nil
	}), opt)
	return sc, err
}
