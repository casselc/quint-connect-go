// Command actionmap prints the code each model action statically reaches
// from where its steps are recorded (qanalysis/actionmap) and, with
// -coverage, compares it with the code its steps ran: a qcover.Summary as
// JSON (examples/checkout's TestCodeCoverage writes one under
// $QUINTGO_KEEP when woven with qweave -cover). It lists dead functions
// first: the ones no root (main, init, exported API) reaches.
//
//	actionmap [-C dir] [-coverage summary.json] packages...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/casselc/quint-connect-go/qanalysis/actionmap"
	"github.com/casselc/quint-connect-go/qcover"
)

func main() {
	dir := flag.String("C", ".", "directory to load the packages from")
	cov := flag.String("coverage", "", "qcover.Summary JSON to compare with")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: actionmap [-C dir] [-coverage summary.json] packages...")
		os.Exit(2)
	}
	m, err := actionmap.Static(*dir, flag.Args()...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range m.Dead {
		fmt.Printf("dead: %s (no root reaches it)\n", f)
	}
	if *cov == "" {
		for _, a := range m.Actions() {
			fmt.Printf("%s: sites %v, reaches %d functions\n", a, m.Sites[a], len(m.Reach[a]))
		}
		return
	}
	b, err := os.ReadFile(*cov)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var s qcover.Summary
	if err := json.Unmarshal(b, &s); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", *cov, err)
		os.Exit(1)
	}
	fmt.Print(actionmap.Report(actionmap.Compare(m, &s)))
}
