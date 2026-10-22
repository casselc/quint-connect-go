package simapp

import (
	"context"
	clock "time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

func run(s *qenv.Sim) {
	go func() {}()                                   // want "goroutine started with `go`, outside the simulation: start it with qenv.Go"
	_ = clock.Now()                                  // want `time.Now reads the wall clock`
	clock.Sleep(clock.Second)                        // want `time.Sleep reads the wall clock`
	qobs.Record(context.Background(), "settle", nil) // want `step recorded with context.Background\(\)`
	qobs.Record(s.Context(), "settle", nil)
	_ = clock.Now() //qenv:ok the real server's clock
	_ = clock.Duration(1)
}
