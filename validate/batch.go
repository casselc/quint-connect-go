package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/casselc/quint-connect-go/qtrace"
)

// CheckRuns checks many independent runs (e.g. the test cases of a
// property-based or state-machine test), each as its own Check, running up
// to parallel quint processes at once (0: GOMAXPROCS). Run i's modules and
// ITF go to <Options.Dir>/run<i>. reports[i] is nil only if errs[i] is set.
func (c *Checker) CheckRuns(ctx context.Context, runs [][]qtrace.Step, parallel int) (reports []*Report, errs []error) {
	if parallel <= 0 {
		parallel = runtime.GOMAXPROCS(0)
	}
	reports, errs = make([]*Report, len(runs)), make([]error, len(runs))
	base := c.Options.Dir
	if base == "" {
		d, err := os.MkdirTemp("", "quintgo-runs-")
		if err != nil {
			for i := range errs {
				errs[i] = err
			}
			return reports, errs
		}
		base = d
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cc := *c
			cc.Options.Dir = filepath.Join(base, fmt.Sprintf("run%d", i))
			reports[i], errs[i] = cc.Check(ctx, runs[i])
		}(i)
	}
	wg.Wait()
	return reports, errs
}
