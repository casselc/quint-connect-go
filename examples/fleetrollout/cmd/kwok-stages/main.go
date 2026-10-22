// Command kwok-stages writes kwok Stage resources that time pods by a
// production trace's lifetimes (fleetrollout.KwokStagesFromTrace), for
// `kwokctl create cluster --config`:
//
//	kwok-stages -trace ../../sktrace/testdata/cronjob.sktrace -speed 10 > stages.yaml
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/casselc/quint-connect-go/examples/fleetrollout"
	"github.com/casselc/quint-connect-go/sktrace"
)

func main() {
	trace := flag.String("trace", "", "SimKube trace (.sktrace)")
	gvk := flag.String("gvk", "", "only pods owned by objects of this kind (\"\": any)")
	n := flag.Int("n", 3, "number of quantiles of the lifetimes: one stage each")
	speed := flag.Float64("speed", 10, "times faster than the trace")
	flag.Parse()
	if *trace == "" {
		fmt.Fprintln(os.Stderr, "kwok-stages: -trace is required")
		os.Exit(2)
	}
	tr, err := sktrace.ReadFile(*trace)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kwok-stages:", err)
		os.Exit(1)
	}
	y, err := fleetrollout.KwokStagesFromTrace(tr, *gvk, *n, *speed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kwok-stages:", err)
		os.Exit(1)
	}
	fmt.Print(y)
}
