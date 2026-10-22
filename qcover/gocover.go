package qcover

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A GoBlock is a block of Go's own coverage (go build -cover): a real
// environment's binaries count with it, globally per process.
type GoBlock struct {
	File                       string // import path and file name
	Line, Col, EndLine, EndCol int
	Stmts, Count               int
}

// A GoProfile is the coverage of one or more runs of binaries built with
// -cover (GOCOVERDIR), merged.
type GoProfile struct {
	Blocks []GoBlock
}

// ReadGoCover merges the coverage data in dirs (each a GOCOVERDIR) with
// `go tool covdata` and reads it.
func ReadGoCover(dirs ...string) (*GoProfile, error) {
	tmp, err := os.MkdirTemp("", "qcover")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "cover.txt")
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+strings.Join(dirs, ","), "-o="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qcover: go tool covdata textfmt -i=%s: %v\n%s", strings.Join(dirs, ","), err, b)
	}
	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseGoCover(f)
}

// ParseGoCover reads Go's text coverage format (go tool covdata textfmt,
// go test -coverprofile). Blocks seen several times are merged.
func ParseGoCover(r io.Reader) (*GoProfile, error) {
	merged := map[string]*GoBlock{}
	var keys []string
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		var b GoBlock
		file, rest, ok := strings.Cut(line, ":")
		f := strings.Fields(rest)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("qcover: line %d: %q is not file:l.c,l.c stmts count", n, line)
		}
		b.File = file
		if _, err := fmt.Sscanf(f[0], "%d.%d,%d.%d", &b.Line, &b.Col, &b.EndLine, &b.EndCol); err != nil {
			return nil, fmt.Errorf("qcover: line %d: %v", n, err)
		}
		var err error
		if b.Stmts, err = strconv.Atoi(f[1]); err == nil {
			b.Count, err = strconv.Atoi(f[2])
		}
		if err != nil {
			return nil, fmt.Errorf("qcover: line %d: %v", n, err)
		}
		k := file + ":" + f[0]
		if m := merged[k]; m != nil {
			m.Count += b.Count
			continue
		}
		merged[k] = &b
		keys = append(keys, k)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	p := &GoProfile{}
	for _, k := range keys {
		p.Blocks = append(p.Blocks, *merged[k])
	}
	return p, nil
}

// Ratio is the share of statements run.
func (p *GoProfile) Ratio() float64 {
	total, run := 0, 0
	for _, b := range p.Blocks {
		total += b.Stmts
		if b.Count > 0 {
			run += b.Stmts
		}
	}
	if total == 0 {
		return 0
	}
	return float64(run) / float64(total)
}

// sameFile reports whether a Go coverage file (import path) is a
// module-relative file of qcover's.
func sameFile(goFile, file string) bool {
	return goFile == file || strings.HasSuffix(goFile, "/"+file)
}

// has reports whether the profile covers file at all, and ran whether a
// block with a count covers line.
func (p *GoProfile) has(file string, line int) (has, ran bool) {
	for _, b := range p.Blocks {
		if !sameFile(b.File, file) {
			continue
		}
		has = true
		if b.Count > 0 && b.Line <= line && line <= b.EndLine {
			return true, true
		}
	}
	return has, false
}

// A Gap is where a real environment's runs and the simulation's disagree
// on what ran.
type Gap struct {
	// RealOnly ran in the real environment and in no simulated run: code
	// the simulation's environment never leads to (a fault it does not
	// inject, a path its doubles do not take). The simulation's verdicts
	// say nothing about it.
	RealOnly []Block
	// SimOnly ran in simulations and never in the real environment: what
	// the real runs did not exercise (faults that did not happen there).
	SimOnly []Block
}

// Compare reads a simulation's code coverage (Summarize, of runs woven by
// qweave -cover) against a real environment's (ReadGoCover), block by
// block, over the files the real binaries contain.
func Compare(sim *Summary, real *GoProfile) *Gap {
	g := &Gap{}
	for _, b := range sim.Blocks {
		has, ran := real.has(b.File, b.Line)
		if !has {
			continue
		}
		simRan := sim.Hit[b.ID] > 0
		switch {
		case ran && !simRan:
			g.RealOnly = append(g.RealOnly, b)
		case simRan && !ran:
			g.SimOnly = append(g.SimOnly, b)
		}
	}
	return g
}

func (g *Gap) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "real environment vs simulation: %d blocks ran only in the real environment, %d only in simulations\n", len(g.RealOnly), len(g.SimOnly))
	for _, x := range g.RealOnly {
		fmt.Fprintf(&b, "  real only: %s %s\n", x.Pos(), x.Func)
	}
	for _, x := range g.SimOnly {
		fmt.Fprintf(&b, "  simulation only: %s %s\n", x.Pos(), x.Func)
	}
	return b.String()
}
