package qenv_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// The scheduler's self-check under frequent crashes: a task killed while it
// waits keeps the deadline it waited for, and the check once took it for
// a task stuck past its deadline.
func TestSelfCheckUnderCrashes(t *testing.T) {
	for _, sleep := range []bool{false, true} {
		for seed := uint64(1); seed <= 50; seed++ {
			c := qenv.NewSeeded(seed)
			c.Rates = qenv.Rates{Default: 0.3}
			s := qenv.New(c)
			s.Check = true
			for _, name := range []string{"a", "b", "c"} {
				s.Process(name, true, func(ctx context.Context) {
					for range 5 {
						d := 10 * time.Millisecond
						if sleep {
							d = time.Duration(s.Choose("sched.wait", 300)) * time.Millisecond
						}
						qenv.Sleep(ctx, d)
					}
				})
			}
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("%v", r)
					}
				}()
				return s.Run(time.Minute)
			}()
			s.Close()
			if err != nil {
				t.Fatalf("sleep choice %v, seed %d: %.300v", sleep, seed, err)
			}
		}
	}
}
