package qenv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// With CrashWindow, a server can die after its handler ran and before it
// answered: the call times out though the effect happened, with no
// network fault at all. Without it, a crash never falls there.
func TestServiceCrashWindow(t *testing.T) {
	afterEffect := func(window bool) int {
		n := 0
		for seed := uint64(1); seed <= 40; seed++ {
			c := qenv.NewSeeded(seed)
			c.Rates = qenv.Rates{Kinds: map[string]float64{"nemesis": 0.3}}
			s := qenv.New(c)
			applied := false
			proc := s.Process("server", true, func(context.Context) {})
			svc := qenv.NewService(s, "server", proc, func(ctx context.Context, _ int) (int, error) { applied = true; return 1, nil })
			svc.CrashWindow = window
			var err error
			s.Spawn("client", func(ctx context.Context) { _, err = svc.Call(ctx, 1, time.Second) })
			if e := s.Run(time.Minute); e != nil {
				t.Fatal(e)
			}
			s.Close()
			if applied && errors.Is(err, qenv.ErrTimeout) {
				n++
			}
		}
		return n
	}
	if n := afterEffect(false); n != 0 {
		t.Errorf("without the window, %d calls lost their response to a crash after the effect", n)
	}
	if n := afterEffect(true); n == 0 {
		t.Error("with the window, no crash fell between the effect and the response")
	}
}
