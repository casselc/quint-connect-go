package connect

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/itf"
)

// Config says how to generate traces and how to read them. The zero value
// of every field means quint's default (or the environment's).
type Config struct {
	Spec string // the .qnt file (required), relative to the test's directory
	Main string // --main: the module to run, when the file has several

	// quint run only:
	Init     string // --init (default "init")
	StepName string // --step (default "step")
	MaxSteps int    // --max-steps; $QUINT_MAX_STEPS

	// MaxSamples is --max-samples, and also --n-traces for quint run: how many
	// traces to generate and replay (default 100; $QUINT_MAX_SAMPLES).
	MaxSamples int
	// Seed is quint's --seed (e.g. "0x2a" or "42"). Default $QUINT_SEED, else
	// random; a failure prints the seed that reproduces it.
	Seed string

	Quint   string   // the quint binary (default $QUINT_BIN, else "quint")
	Backend string   // --backend (default $QUINT_BACKEND, else quint's own default)
	Args    []string // extra arguments for quint, e.g. --invariant
	Dir     string   // where traces are written (default a temp dir; t.TempDir() in Run and Test)

	// How to read the traces (see ReplayOptions).
	StatePath  []string
	NondetPath []string

	// Verbose: 0 quiet, 1 logs every step (action and picks), 2 also the
	// model state. Default $QUINT_VERBOSE. Failures always show the step.
	Verbose int
}

// FromEnv fills unset fields from the environment, quint-connect's
// variables (read here at run time, not at compile time) and a few more:
//
//	QUINT_SEED         Seed
//	QUINT_VERBOSE      Verbose
//	QUINT_MAX_SAMPLES  MaxSamples
//	QUINT_MAX_STEPS    MaxSteps
//	QUINT_BACKEND      Backend (also QUINTGO_BACKEND)
//	QUINT_BIN          Quint
func (c Config) FromEnv() Config {
	if c.Seed == "" {
		c.Seed = os.Getenv("QUINT_SEED")
	}
	envInt := func(dst *int, name string) {
		if *dst == 0 {
			if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
				*dst = n
			}
		}
	}
	envInt(&c.Verbose, "QUINT_VERBOSE")
	envInt(&c.MaxSamples, "QUINT_MAX_SAMPLES")
	envInt(&c.MaxSteps, "QUINT_MAX_STEPS")
	if c.Backend == "" {
		c.Backend = os.Getenv("QUINT_BACKEND")
	}
	if c.Backend == "" {
		c.Backend = os.Getenv("QUINTGO_BACKEND")
	}
	if c.Quint == "" {
		c.Quint = os.Getenv("QUINT_BIN")
	}
	return c
}

func (c Config) resolve() (Config, error) {
	c = c.FromEnv()
	if c.Spec == "" {
		return c, errors.New("connect: Config.Spec is required")
	}
	if c.Quint == "" {
		c.Quint = "quint"
		if runtime.GOOS == "windows" {
			c.Quint = "quint.cmd"
		}
	}
	if c.MaxSamples == 0 {
		c.MaxSamples = 100
	}
	if c.Seed == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		c.Seed = fmt.Sprintf("0x%x", binary.BigEndian.Uint32(b[:]))
	}
	if c.Dir == "" {
		d, err := os.MkdirTemp("", "quint-connect-")
		if err != nil {
			return c, err
		}
		c.Dir = d
	}
	if abs, err := filepath.Abs(c.Dir); err == nil {
		c.Dir = abs
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return c, err
	}
	return c, nil
}

// QuintError is a failed quint invocation.
type QuintError struct {
	Args   []string
	Output string
	Err    error
}

func (e *QuintError) Error() string {
	return fmt.Sprintf("quint %s: %v\n%s", strings.Join(e.Args, " "), e.Err, e.Output)
}

func (e *QuintError) Unwrap() error { return e.Err }

// GenerateRun runs `quint run --mbt` and returns the trace files, in order,
// and the resolved config (with the seed used).
func GenerateRun(ctx context.Context, c Config) ([]string, Config, error) {
	c, err := c.resolve()
	if err != nil {
		return nil, c, err
	}
	spec, _ := filepath.Abs(c.Spec)
	n := strconv.Itoa(c.MaxSamples)
	args := []string{"run", spec, "--mbt", "--seed", c.Seed, "--max-samples", n, "--n-traces", n,
		"--out-itf", filepath.Join(c.Dir, "run_{seq}.itf.json"), "--verbosity", "0"}
	if c.Main != "" {
		args = append(args, "--main", c.Main)
	}
	if c.Init != "" {
		args = append(args, "--init", c.Init)
	}
	if c.StepName != "" {
		args = append(args, "--step", c.StepName)
	}
	if c.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(c.MaxSteps))
	}
	files, err := quint(ctx, c, append(args, c.Args...), "run_")
	return files, c, err
}

// GenerateTest runs the `run` named test of the spec with `quint test` and
// returns its trace files. Such traces carry no mbt:: variables: replay
// them with Config.NondetPath, or validate them instead (package validate).
func GenerateTest(ctx context.Context, c Config, test string) ([]string, Config, error) {
	c, err := c.resolve()
	if err != nil {
		return nil, c, err
	}
	spec, _ := filepath.Abs(c.Spec)
	args := []string{"test", spec, "--seed", c.Seed, "--match", "^" + regexp.QuoteMeta(test) + "$",
		"--max-samples", strconv.Itoa(c.MaxSamples), "--out-itf", filepath.Join(c.Dir, "test_{seq}.itf.json"), "--verbosity", "0"}
	if c.Main != "" {
		args = append(args, "--main", c.Main)
	}
	files, err := quint(ctx, c, append(args, c.Args...), "test_")
	return files, c, err
}

func quint(ctx context.Context, c Config, args []string, prefix string) ([]string, error) {
	if c.Backend != "" {
		args = append(args, "--backend", c.Backend)
	}
	cmd := exec.CommandContext(ctx, c.Quint, args...)
	cmd.Dir = filepath.Dir(must(filepath.Abs(c.Spec)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			err = fmt.Errorf("%w (install quint: npm i -g @informalsystems/quint)", err)
		}
		return nil, &QuintError{Args: args, Output: string(out), Err: err}
	}
	files, _ := filepath.Glob(filepath.Join(c.Dir, prefix+"*.itf.json"))
	sort.Slice(files, func(i, j int) bool { return seqOf(files[i]) < seqOf(files[j]) })
	if len(files) == 0 {
		return nil, &QuintError{Args: args, Output: string(out), Err: errors.New("no traces were generated: check the spec and the configuration")}
	}
	return files, nil
}

var reSeq = regexp.MustCompile(`_(\d+)\.itf\.json$`)

func seqOf(path string) int {
	if m := reSeq.FindStringSubmatch(path); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func must[T any](t T, _ error) T { return t }

// Run generates traces with `quint run --mbt` and replays each on a fresh
// driver from newDriver, failing t at the first divergence, with the seed
// that reproduces it. It is quint-connect's #[quint_run].
func Run(t testing.TB, c Config, newDriver func() Driver) {
	t.Helper()
	if c.Dir == "" {
		c.Dir = t.TempDir()
	}
	files, c, err := GenerateRun(context.Background(), c)
	if err != nil {
		t.Fatalf("connect: generating traces: %v", err)
	}
	replayAll(t, c, files, newDriver, "run")
}

// Test replays the traces of the spec's `run` named test (quint test --match)
// on a fresh driver each. It is quint-connect's #[quint_test]. Unless the
// spec records the action itself (Config.NondetPath), use package validate
// to check such tests against Go instead.
func Test(t testing.TB, c Config, test string, newDriver func() Driver) {
	t.Helper()
	if c.Dir == "" {
		c.Dir = t.TempDir()
	}
	files, c, err := GenerateTest(context.Background(), c, test)
	if err != nil {
		t.Fatalf("connect: generating traces of %s: %v", test, err)
	}
	replayAll(t, c, files, newDriver, "test "+test)
}

// ReplayFiles replays already generated (or recorded and validated) ITF
// trace files, each on a fresh driver.
func ReplayFiles(t testing.TB, c Config, files []string, newDriver func() Driver) {
	t.Helper()
	replayAll(t, c.FromEnv(), files, newDriver, "replay")
}

func replayAll(t testing.TB, c Config, files []string, newDriver func() Driver, what string) {
	t.Helper()
	t.Logf("connect: %s %s: %d traces, seed %s", what, c.Spec, len(files), c.Seed)
	for i, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatalf("connect: %s: %v", f, err)
		}
		opt := ReplayOptions{Trace: i, StatePath: c.StatePath, NondetPath: c.NondetPath}
		if c.Verbose > 0 {
			opt.OnStep = func(s *Step) {
				if c.Verbose > 1 {
					t.Logf("[trace %d step %d]\n%s", s.Trace, s.Index, s)
				} else {
					t.Logf("[trace %d step %d] %s", s.Trace, s.Index, s.summary())
				}
			}
		}
		if err := Replay(tr, newDriver(), opt); err != nil {
			var se *StepError
			msg := err.Error()
			if errors.As(err, &se) && se.Kind != "trace" {
				msg += "\n\n" + indent(se.Step.String(), "    ")
			}
			repro := ""
			if c.Seed != "" && what == "run" {
				repro = fmt.Sprintf("\nreproduce with QUINT_SEED=%s (trace file %s)", c.Seed, f)
			}
			t.Fatalf("connect: %s\n%s", msg, repro)
		}
	}
}

func indent(s, pfx string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = pfx + lines[i]
	}
	return strings.Join(lines, "\n")
}
