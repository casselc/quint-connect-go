package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/casselc/quint-connect-go/qcover"
)

// runCover reads a real environment's coverage (GOCOVERDIRs of binaries
// built with -cover) and, given a simulation's coverage summary, what ran
// in one and not the other.
func runCover(args []string) error {
	fset := flag.NewFlagSet("cover", flag.ExitOnError)
	covdir := fset.String("covdir", "", "comma-separated GOCOVERDIRs of the real environment's runs")
	sim := fset.String("sim", "", "a simulation's coverage summary (qcover.Summary as JSON)")
	fset.Parse(args)
	if *covdir == "" {
		return fmt.Errorf("cover needs -covdir")
	}
	real, err := qcover.ReadGoCover(strings.Split(*covdir, ",")...)
	if err != nil {
		return err
	}
	fmt.Printf("real environment: %.1f%% of statements in %d blocks\n", 100*real.Ratio(), len(real.Blocks))
	if *sim == "" {
		return nil
	}
	b, err := os.ReadFile(*sim)
	if err != nil {
		return err
	}
	var s qcover.Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s: %w (want a qcover.Summary as JSON)", *sim, err)
	}
	fmt.Print(qcover.Compare(&s, real))
	return nil
}
