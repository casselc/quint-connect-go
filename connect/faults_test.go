package connect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

var errDisk = errors.New("disk full")

// journal is the program: it knows nothing of traces. Its writes are a
// fault point, its process crashable; what survives a crash is the durable
// count (forgets: the mutant that kept it in memory only).
type journal struct {
	written, attempts int
	forgets           bool
}

func (j *journal) run(ctx context.Context, p **qenv.Process) {
	if (*p) != nil && (*p).Incarnation > 1 {
		if j.forgets {
			j.written = 0
		}
		qobs.Record(ctx, "restart", nil, "obs.written", j.written)
	}
	for j.attempts < 6 {
		err := qenv.FaultPoint(ctx, "journal.write", false, errDisk)
		j.attempts++
		if err == nil {
			j.written++
			qobs.Record(ctx, "write", nil, "obs.written", j.written)
		} else {
			qobs.Record(ctx, "writeFail", nil, "obs.written", j.written)
		}
		qenv.Sleep(ctx, time.Second)
	}
}

var journalFaults = connect.FaultMap{
	"writeFail": func(*connect.Step) *connect.Fault {
		return &connect.Fault{Kind: "journal.write", Option: 1, Recorded: true}
	},
	"restart": func(*connect.Step) *connect.Fault {
		return &connect.Fault{Kind: "nemesis.crash.writer", Recorded: true}
	},
}

// journalSystem runs the journal in a simulation of its own.
func journalSystem(forgets bool) qtest.System {
	return qtest.System{Binding: "testdata/journal.binding.yaml", Run: func(c qenv.Chooser) error {
		s := qenv.New(c)
		defer s.Close()
		j := &journal{forgets: forgets}
		var p *qenv.Process
		p = s.Process("writer", true, func(ctx context.Context) { j.run(ctx, &p) })
		return s.Run(time.Minute)
	}}
}

var journalTraces = connect.Config{Spec: "testdata/journal.qnt", MaxSamples: 8, MaxSteps: 8, Seed: "11"}

// Quint traces drive the whole program through its environment: each
// trace's failed writes and restart are imposed as faults, the program
// makes every other step itself, and every run follows its trace and
// conforms to the model.
func TestFaultScriptDrivesProgram(t *testing.T) {
	qtest.RequireQuint(t)
	x := journalSystem(false).FollowTraces(t, journalTraces, journalFaults)
	x.Expect(t, qtest.Conforms())
	cov := x.Coverage()
	if w, r := cov.Point("journal.write"), cov.Point("nemesis"); w == nil || w.Faulted() == 0 || r == nil || r.Faulted() == 0 {
		t.Fatalf("the traces lack a failed write or a restart:\n%s", cov)
	}
}

// The mutant that keeps its count in memory follows the same traces, and
// the model rejects its restart: the trace's crash is what exposes it.
func TestFaultScriptCatchesForgetfulRestart(t *testing.T) {
	qtest.RequireQuint(t)
	journalSystem(true).FollowTraces(t, journalTraces, journalFaults).Expect(t, qtest.Mismatch("restart"))
}
