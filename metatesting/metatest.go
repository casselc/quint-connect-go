package metatesting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jellevandenhooff/gosim/gosimruntime"
	"github.com/jellevandenhooff/gosim/internal/gosimtool"
	"github.com/jellevandenhooff/gosim/internal/prettylog"
)

// The structs below are copied from gosimruntime.runConfig and
// gosimruntime.runResult. Keep in sync.
// They are copied to give a nice documentation view without refering to
// internal types.

// A RunConfig configures a test invocation.
type RunConfig struct {
	Test     string
	Seed     int64
	ExtraEnv []string
	Simtrace string
}

// A RunResult contains the result fo a test run.
type RunResult struct {
	Seed      int64
	Checksum  []byte
	Failed    bool
	LogOutput []byte
	Err       string // TODO: reconsider this type?
}

// A MetaT runs gosim tests for a given package. It can run tests with various
// flags and returns their results.
//
// Behind the scenes a MetaT executes and controls a gosim test binary.
type MetaT struct {
	cmd  *exec.Cmd
	dead bool // stopped (kill, Close): it runs nothing more

	w *json.Encoder
	r *json.Decoder
}

type prefixer struct {
	out    *os.File
	prefix string
}

func (w prefixer) Write(b []byte) (n int, err error) {
	n = len(b)
	for len(b) > 0 {
		end := bytes.IndexByte(b, '\n')
		if end == -1 {
			end = len(b)
		} else {
			end++
		}
		line := b[:end]
		w.out.WriteString(w.prefix)
		w.out.Write(line)
		if line[len(line)-1] != '\n' {
			w.out.WriteString("\n")
		}
		b = b[end:]
	}
	return
}

func newRunner(path string) (*MetaT, error) {
	cmd := exec.Command(path, "-metatest")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	// TODO: Do not discard, and disable the formatted output print in gosimruntime instead
	// TODO: Prefix output to help identify stray logging?
	cmd.Stderr = &prefixer{out: os.Stderr, prefix: "metatest binary: "}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	w := json.NewEncoder(stdin)
	r := json.NewDecoder(stdout)

	return &MetaT{
		cmd: cmd,

		w: w,
		r: r,
	}, nil
}

// TODO: Support match expressions?
func (r *MetaT) ListTests() ([]string, error) {
	if err := r.w.Encode(&RunConfig{
		Test: "listtests",
	}); err != nil {
		return nil, err
	}

	var tests []string
	if err := r.r.Decode(&tests); err != nil {
		return nil, err
	}

	return tests, nil
}

/*
func (r *Runner) Close() {
	r.cmd.Process.Kill()
	r.cmd.Wait()
}
*/

func (r *MetaT) RunAllTests(t *testing.T) {
	// TODO: Parallel tests for multiple seeds/tests?

	tests, err := r.ListTests()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			run, err := r.Run(t, &RunConfig{
				Test: test,
				Seed: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if run.Failed {
				t.Error("failed")
			}
		})
	}
}

func (r *MetaT) Run(t *testing.T, config *RunConfig) (*RunResult, error) {
	t.Helper()

	// TODO: Handle concurrent runs (lock?)
	// TODO: Stop or restart process on error?

	b := new(bytes.Buffer)

	// TODO: include "gosim test ..." line?
	fmt.Fprintf(b, "\n> running %s [seed=%d]\n", config.Test, config.Seed)

	if err := r.w.Encode(config); err != nil {
		return nil, err
	}

	var result RunResult
	if err := r.r.Decode(&result); err != nil {
		// TODO: Print results that fail to decode
		return nil, err
	}

	w := prettylog.NewWriter(b)

	out := result.LogOutput
	for len(out) > 0 {
		idx := bytes.IndexByte(out, '\n')
		if idx == -1 {
			idx = len(out) - 1
		}
		line := out[:idx+1]
		out = out[idx+1:]
		w.Write([]byte(line))
	}
	t.Log(b.String())

	return &result, nil
}

var (
	runnerMapMu sync.Mutex
	runnerMap   = make(map[string]*MetaT)
)

// ForOtherPackage retursn a *MetaT for the specified package.
func ForOtherPackage(t *testing.T, pkg string) *MetaT {
	if gosimruntime.IsSim() {
		t.Fatalf("metatest cannot be used from within gosim")
	}

	runnerMapMu.Lock()
	defer runnerMapMu.Unlock()

	runner, ok := runnerMap[pkg]
	if ok {
		return runner
	}

	path := gosimtool.GetPathForPrecompiledTestBinary(t, pkg)
	runner, err := newRunner(path)
	if err != nil {
		t.Fatalf("failed to run test binary: %s", err)
	}
	runnerMap[pkg] = runner
	return runner
}

// getCurrentPackageFromWorkingdir guesses the package
// that `go test` is testing from the working directory.
func getCurrentPackageFromWorkingdir() string {
	// go test runs all tests with the working directory set to
	// the current package's source code. Walk upwards to find
	// the go.mod file, and then combine the module path
	// with the relative path to the working directory.
	goModPath, modFile, err := gosimtool.FindGoMod()
	if err != nil {
		log.Fatal(err)
	}
	goModDir := filepath.Dir(goModPath)
	baseDir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	relativePkg := strings.TrimPrefix(baseDir, goModDir)
	finalPkg := modFile.Module.Mod.Path + relativePkg
	return finalPkg
}

// ForCurrentPackage returns a *MetaT for the package currently
// being tested by `go test`.
//
// ForCurrentPackage relies on the working directory to identify the current
// package, so if a test changes directories ForCurrentPackage will not work
// correctly.
func ForCurrentPackage(t *testing.T) *MetaT {
	if gosimruntime.IsSim() {
		t.Fatalf("metatest cannot be used from within gosim")
	}

	return ForOtherPackage(t, getCurrentPackageFromWorkingdir())
}

func CheckDeterministic(t *testing.T, mt *MetaT) {
	tests, err := mt.ListTests()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			run1, err := mt.Run(t, &RunConfig{
				Test: test,
				Seed: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			run2, err := mt.Run(t, &RunConfig{
				Test: test,
				Seed: 1,
			})
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(run1.Checksum, run2.Checksum) {
				slog.Error("checksums differ: non-determinism found")
				t.Fail()
				// XXX debug view?
			}
			if !bytes.Equal(run1.LogOutput, run2.LogOutput) {
				slog.Error("logs differ: non-determinism found", "diff", cmp.Diff(run1.LogOutput, run2.LogOutput))
				t.Fail()
			}
		})
	}
}

func CheckSeeds(t *testing.T, mt *MetaT, numSeeds int) {
	tests, err := mt.ListTests()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			for seed := int64(0); seed < 5; seed++ {
				run, err := mt.Run(t, &RunConfig{
					Test: test,
					Seed: seed,
				})
				if err != nil {
					t.Fatal(err)
				}
				if run.Failed {
					t.Error("failed")
				}
				// XXX: how do we assert run success?
			}
		})
	}
}

func ParseLog(logs []byte) []map[string]any {
	var out []map[string]any

	for _, line := range bytes.Split(logs, []byte("\n")) {
		var log map[string]any
		if err := json.Unmarshal(line, &log); err != nil {
			// TODO: Is this ok?
			continue
		}
		out = append(out, log)
	}

	return out
}

func SimplifyParsedLog(logs []map[string]any) []string {
	var out []string
	for _, log := range logs {
		out = append(out, fmt.Sprintf("%s %s", log["level"], strings.Trim(log["msg"].(string), "\n")))
	}
	return out
}

func MustFindLogValue(logs []map[string]any, msg string, key string) any {
	for _, log := range logs {
		if log["msg"] == msg {
			val, ok := log[key]
			if !ok {
				panic("no matching key")
			}
			return val
		}
	}
	panic("no matching msg")
}

/*
func maybeRunInSubtest(t *testing.T, runInSubtest bool, name string, fun func(t *testing.T)) {
	if runInSubtest {
		t.Run(name, fun)
	} else {
		fun(t)
	}
}

type SimConfig struct {
	Seeds                []int64
	EnableTracer         bool
	CheckDeterminismRuns int // XXX: ensure this also checks logs
	SimSubtest           bool
	SeedSubtests         bool
	RunSubtests          bool
	DontReportFail       bool // rename?

	CaptureLog       bool
	LogLevelOverride string
	// xxx: time limit here? steps limit?
}

func RunNestedTest(t *testing.T, config SimConfig, test Test) []gosimruntime.RunResult {
	if config.CheckDeterminismRuns > 0 && !config.EnableTracer {
		// XXX: automatically set flag?
		panic("must enable tracer when checking determinism")
	}
	if config.CheckDeterminismRuns < 0 {
		panic("check determinism runs must be non-negative")
	}

	// XXX
	// dontReportFail := config.DontReportFail || s.dontReportFail
	dontReportFail := config.DontReportFail

	var results []gosimruntime.RunResult
	maybeRunInSubtest(t, config.SimSubtest, test.Name, func(t *testing.T) {
		for _, seed := range config.Seeds {
			maybeRunInSubtest(t, config.SeedSubtests, fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
				for run := 0; run < 1+config.CheckDeterminismRuns; run++ {
					maybeRunInSubtest(t, config.RunSubtests, fmt.Sprintf("run-%d", run), func(t *testing.T) {
						result := RunInner(test.Test, seed, config.EnableTracer, config.CaptureLog, config.LogLevelOverride, dontReportFail, t)

						if run == 0 {
							results = append(results, result)
						} else {
							prevResult := results[len(results)-1]
							if !bytes.Equal(result.Trace, prevResult.Trace) {
								slog.Error("traces differ: non-determinism found")
								t.Fail()
								// XXX debug view?
							}
							if !bytes.Equal(result.LogOutput, prevResult.LogOutput) {
								slog.Error("logs differ: non-determinism found", "diff", cmp.Diff(result.LogOutput, prevResult.LogOutput))
								t.Fail()
							}
							results = append(results, result)
						}
					})
				}
			})
		}
	})
	return results
}
*/
