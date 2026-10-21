package qenv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
)

// Lint finds what escapes a simulation, in files that import qenv, and
// leaves the rest (and lines marked //qenv:ok) alone.
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
`)
	fs, err := qenv.Lint(dir)
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
	fs, err := qenv.LintWoven(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Pos[strings.LastIndex(f.Pos, "/")+1:]+" "+f.What)
	}
	want := []string{
		"app.go:9:2 go statement not marked //qenv:go: its goroutine runs outside the simulation",
		"app.go:14:9 method value RUnlock is not woven (weaving rewrites calls)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
