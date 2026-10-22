package qmeta_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
)

type buf struct{ bytes.Buffer }

func (*buf) Close() error { return nil }

// A watch writes the flight recorder's trace once the state has been in
// the region For samples in a row, and not again within Gap.
func TestWatchSnapshotsOnEntering(t *testing.T) {
	c := qmeta.New(qmeta.Params{Lambda: 9.5, Mu: 10, Timeout: 9, Retries: 3, Queue: 100})
	region := c.Region(func(s qmeta.State) bool { return s.U <= 10 }, 120)
	storm, calm := qmeta.State{U: 100, V: 150}, qmeta.State{U: 2}
	if !region.Contains(storm) || region.Contains(calm) {
		t.Fatalf("region: storm %v, calm %v", region.Contains(storm), region.Contains(calm))
	}
	now, state := time.Unix(0, 0), calm
	var snaps []*buf
	w := &qmeta.Watch{Region: region, For: 2, Gap: time.Minute, State: func() qmeta.State { return state },
		Snapshot: func(qmeta.State) (io.WriteCloser, error) { b := &buf{}; snaps = append(snaps, b); return b, nil }}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	step := func(s qmeta.State) bool {
		state, now = s, now.Add(time.Second)
		fired, err := w.Observe(now)
		if err != nil {
			t.Fatal(err)
		}
		return fired
	}
	if step(calm) || step(storm) {
		t.Fatal("fired before For samples in the region")
	}
	if !step(storm) {
		t.Fatal("did not fire on the second sample in the region")
	}
	if step(storm) {
		t.Fatal("fired again within Gap")
	}
	if len(snaps) != 1 || !bytes.HasPrefix(snaps[0].Bytes(), []byte("go 1.")) {
		t.Fatalf("snapshots %d; want one execution trace", len(snaps))
	}
}
