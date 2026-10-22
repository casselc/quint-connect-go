// Command qweave runs the go command on woven source: it weaves the
// module's package graph with the Orchestrion aspects its tool file names
// (orchestrion.tool.go), writes the woven files and an overlay, and runs
// the go command with -overlay.
//
//	qweave go test ./...
//	go run -C path/to/qweave ./cmd/qweave -C "$PWD" go test ./sim/
//	qweave -tool gosim.tool.go go build ./cmd/server
//
// Build tags given to the go command (-tags) are used for weaving too.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/casselc/quint-connect-go/qweave"
)

func main() { os.Exit(run()) }

func run() int {
	tool := flag.String("tool", "", "the tool file naming the aspect packages (default: orchestrion.tool.go, else gosim.tool.go)")
	verbose := flag.Bool("v", false, "list the woven files and the aspects skipped")
	chdir := flag.String("C", "", "change to `dir` first (the module to weave and build)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: qweave [-C dir] [-tool file] [-v] go <command> [arguments]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *chdir != "" {
		if err := os.Chdir(*chdir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	args := flag.Args()
	if len(args) < 2 || args[0] != "go" {
		flag.Usage()
		return 2
	}
	cfg := qweave.Config{Tool: *tool}
	for i, a := range args[2:] {
		if t, ok := strings.CutPrefix(a, "-tags="); ok {
			cfg.Tags = strings.Split(t, ",")
		} else if a == "-tags" && i+3 < len(args) {
			cfg.Tags = strings.Split(args[i+3], ",")
		}
	}
	r, err := qweave.Weave(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *verbose {
		for name := range r.Files {
			fmt.Fprintln(os.Stderr, "qweave: woven", name)
		}
		for _, s := range r.Skipped {
			fmt.Fprintln(os.Stderr, "qweave: skipped", s)
		}
	}
	goArgs := args[1:2]
	if len(r.Files) > 0 {
		dir, err := os.MkdirTemp("", "qweave")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		overlay, err := r.WriteOverlay(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		goArgs = append(goArgs, "-overlay="+overlay)
	}
	cmd := exec.Command("go", append(goArgs, args[2:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
