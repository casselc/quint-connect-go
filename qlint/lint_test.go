package qlint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qlint"
)

// Lint finds what escapes a simulation, in files that import qenv (tests
// too: a System's Run closure lives in one), and leaves the rest (and
// lines marked //qenv:ok) alone.
func TestLint(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sim.go", `package app

import (
	"context"
	clock "time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

func run(s *qenv.Sim) {
	go func() {}()
	_ = clock.Now()
	clock.Sleep(clock.Second)
	qobs.Record(context.Background(), "settle", nil)
	qobs.Record(s.Context(), "settle", nil)
	_ = clock.Now() //qenv:ok the real server's clock
	_ = clock.Duration(1)
}
`)
	write("server.go", `package app

import "time"

func serve() { go func() {}(); _ = time.Now() }
`)
	write("sim_test.go", `package app

import (
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

var _ = qenv.Nominal{}
var t0 = time.Now()
var t1 = time.Now() //qenv:ok measures the real cost
`)
	fs, err := qlint.Sim(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Pos[strings.LastIndex(f.Pos, "/")+1:]+" "+f.What)
	}
	want := []string{
		"sim.go:12:2 goroutine started with `go`, outside the simulation",
		"sim.go:13:6 time.Now reads the wall clock, which does not move in a simulation",
		"sim.go:14:2 time.Sleep reads the wall clock, which does not move in a simulation",
		"sim.go:15:2 step recorded with context.Background()",
		"sim_test.go:10:10 time.Now reads the wall clock, which does not move in a simulation",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// LintWoven finds go statements and lock method values that weaving
// cannot rewrite.
func TestLintWoven(t *testing.T) {
	dir := t.TempDir()
	src := `package app

import "sync"

type st struct{ mu sync.RWMutex }

type guarded struct {
	sync.Mutex
	inner struct{ *sync.WaitGroup }
}

func (s *st) acquire() func() {
	s.mu.RLock()
	go func() {}()
	//qenv:go
	go func() {}()
	go func() {}() //qenv:ok
	defer func() { s.mu.RUnlock() }()
	return s.mu.RUnlock
}
`
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := qlint.Woven(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Pos[strings.LastIndex(f.Pos, "/")+1:]+" "+f.What)
	}
	want := []string{
		"app.go:8:2 embedded sync.Mutex: calls promoted through the struct are not woven (weaving matches the receiver's type)",
		"app.go:9:16 embedded sync.WaitGroup: calls promoted through the struct are not woven (weaving matches the receiver's type)",
		"app.go:14:2 go statement not marked //qenv:go: its goroutine runs outside the simulation",
		"app.go:19:9 method value RUnlock is not woven (weaving rewrites calls)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
