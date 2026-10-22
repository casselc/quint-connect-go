package connect_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
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

// followTraces runs the journal under each of the model's traces' faults
// and checks that the run took the trace's actions; it returns the runs'
// steps and the faults imposed, by kind.
func followTraces(t *testing.T, forgets bool) ([][]qtrace.Step, map[string]int) {
	t.Helper()
	files, _, err := connect.GenerateRun(context.Background(), connect.Config{
		Spec: "testdata/journal.qnt", MaxSamples: 8, MaxSteps: 8, Seed: "11", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var runs [][]qtrace.Step
	for i, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := connect.FaultScript(tr, journalFaults, connect.ReplayOptions{Trace: i})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range sc.Faults {
			kinds[f.Kind]++
		}
		mem := &qobs.MemorySink{}
		sc.Progress = func() int { return len(mem.Steps()) }
		s := qenv.New(qenv.WithContext(sc, qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))))
		j := &journal{forgets: forgets}
		var p *qenv.Process
		p = s.Process("writer", true, func(ctx context.Context) { j.run(ctx, &p) })
		if err := s.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		s.Close()
		var want, got []string
		for _, st := range tr.States[1:] {
			want = append(want, st.Action)
		}
		for _, st := range mem.Steps() {
			got = append(got, st.Action)
		}
		// the trace is a prefix: the program goes on to its sixth write
		if r := sc.Remaining(); len(r) > 0 || !reflect.DeepEqual(got[:min(len(got), len(want))], want) {
			t.Fatalf("trace %d: the run did not follow it (faults left %v)\nwant %v\ngot  %v", i, r, want, got)
		}
		runs = append(runs, mem.Steps())
	}
	return runs, kinds
}

// Quint traces drive the whole program through its environment: each
// trace's failed writes and restart are imposed as faults, the program
// makes every other step itself, and its recorded run is the trace's,
// action for action, and conforms to the model.
func TestFaultScriptDrivesProgram(t *testing.T) {
	qtest.RequireQuint(t)
	runs, kinds := followTraces(t, false)
	t.Logf("faults imposed: %v", kinds)
	if kinds["journal.write"] == 0 || kinds["nemesis.crash.writer"] == 0 {
		t.Fatalf("the traces lack a failed write or a restart: %v", kinds)
	}
	for _, steps := range runs {
		qtest.Check(t, "testdata/journal.binding.yaml", steps)
	}
}

// The mutant that keeps its count in memory follows the same traces, and
// the model rejects its restart: the trace's crash is what exposes it.
func TestFaultScriptCatchesForgetfulRestart(t *testing.T) {
	qtest.RequireQuint(t)
	runs, _ := followTraces(t, true)
	caught := 0
	for _, steps := range runs {
		wrote := false
		for _, st := range steps {
			wrote = wrote || st.Action == "write"
			if st.Action == "restart" && wrote {
				qtest.Expect(t, qtest.Validate(t, "testdata/journal.binding.yaml", steps).Report, qtest.Mismatch("restart"))
				caught++
				break
			}
		}
	}
	if caught == 0 {
		t.Fatal("no trace restarts after a write")
	}
}
