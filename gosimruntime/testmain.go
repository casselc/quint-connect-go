package gosimruntime

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

type Test struct {
	Name string
	Test any // func()
}

var (
	allTests      map[string]any
	allTestsSlice []Test
)

func SetAllTests(tests []Test) {
	allTests = make(map[string]any)
	allTestsSlice = tests
	for _, test := range tests {
		allTests[test.Name] = test.Test
	}
}

var metatest = flag.Bool("metatest", false, "")

// Copied in metatesting.RunConfig. Keep in sync.
type runConfig struct {
	Test     string
	Seed     int64
	ExtraEnv []string
	Simtrace string
	// Remote makes the runner the chooser of this run (choose.go).
	Remote bool
}

// Copied in metatesting.RunResult. Keep in sync.
type runResult struct {
	Seed      int64
	Checksum  []byte
	Failed    bool
	LogOutput []byte
	Err       string // TODO: reconsider this type?
}

type Runtime interface {
	Run(fn func())
	Setup()
	TestEntrypoint(match string, skip string, tests []Test) bool
}

// subset of flags defined by the testing package that we parse
// and support.
var supportedFlags = map[string]bool{
	// supported:
	"test.run":  true,
	"test.skip": true,

	// not supported but (hopefully) harmless to ignore:
	"test.paniconexit0": true,
	"test.testlogfile":  true,
	"test.timeout":      true,
	"test.v":            true,
}

type seedRange struct {
	start, end int64
}

func parseSeeds(seedsStr string) ([]seedRange, error) {
	var seeds []seedRange
	for _, rangeStr := range strings.Split(seedsStr, ",") {
		startStr, endStr, ok := strings.Cut(rangeStr, "-")
		if !ok {
			endStr = startStr
		}

		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad start of seed range %q: %v", startStr, err)
		}
		end, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad end of seed range %q: %v", endStr, err)
		}

		seeds = append(seeds, seedRange{start: start, end: end})
	}
	return seeds, nil
}

func TestMain(rt Runtime) {
	simtrace := flag.String("simtrace", "", "set of comma-separated traces to enable")
	seedsStr := flag.String("seeds", "1", "comma-separated list of seeds and ranges")

	// TODO: make this flag beter; it won't work with multiple test runs?
	jsonlogout := flag.String("jsonlogout", "", "path to a file to write json log to, for use with viewer")

	flag.Parse()

	rt.Setup()
	initializeRuntime(rt.Run)

	if !*metatest {
		// parallel := flag.Lookup("test.parallel").Value.(flag.Getter).Get().(int)
		match := flag.Lookup("test.run").Value.(flag.Getter).Get().(string)
		skip := flag.Lookup("test.skip").Value.(flag.Getter).Get().(string)

		// guard against unknown "test." flags showing up. Since we replace the
		// testing implementation, flags will (likely) not work as expected.
		flag.CommandLine.Visit(func(f *flag.Flag) {
			if strings.HasPrefix(f.Name, "test.") && !supportedFlags[f.Name] {
				log.Fatalf("flag %s is not supported by gosim", f.Name)
			}
		})

		if err := parseTraceflagsConfig(*simtrace); err != nil {
			log.Fatal(err)
		}

		seeds, err := parseSeeds(*seedsStr)
		if err != nil {
			log.Fatalf("parsing -seeds: %s", err)
		}

		enableTracer := true
		captureLog := true
		logLevelOverride := "INFO"

		// TODO: allow -count, -seeds

		outerOk := true

		var jsonout *os.File
		if *jsonlogout != "" {
			var err error
			jsonout, err = os.OpenFile(*jsonlogout, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				log.Fatalf("error opening jsonlogout: %s", err)
			}
		}

		for _, seedRange := range seeds {
			for seed := seedRange.start; seed <= seedRange.end; seed++ {
				// TODO: filter list of tests outside since each run call has quite some overhead
				// for skipped tests.
				for _, test := range allTestsSlice {
					result := run(func() {
						ok := rt.TestEntrypoint(match, skip, []Test{
							test,
						})
						if !ok {
							SetAbortError(ErrTestFailed)
						}
					}, int64(seed), enableTracer, captureLog, logLevelOverride, makeConsoleLogger(os.Stderr), []string{})

					if jsonout != nil {
						if _, err := jsonout.Write(result.LogOutput); err != nil {
							log.Fatalf("error writing jsonout: %s", err)
						}
					}

					if result.Failed {
						outerOk = false
					}
				}
			}
		}

		if jsonout != nil {
			if err := jsonout.Close(); err != nil {
				log.Fatalf("error closing jsonout: %s", err)
			}
		}

		if !outerOk {
			os.Exit(1)
		}
		os.Exit(0)
	}

	in := json.NewDecoder(os.Stdin)
	out := json.NewEncoder(os.Stdout)

	for {
		var req runConfig
		if err := in.Decode(&req); err != nil {
			log.Fatal(err)
		}

		// TODO: add "listtests" as a separate message type?
		if req.Test == "listtests" {
			if err := out.Encode(slices.Sorted(maps.Keys(allTests))); err != nil {
				log.Fatal(err)
			}
			continue
		}

		seed := req.Seed
		enableTracer := true
		captureLog := true
		logLevelOverride := "INFO"

		if err := parseTraceflagsConfig(req.Simtrace); err != nil {
			metaResult := runResult{
				Err: err.Error(),
			}
			if err := out.Encode(metaResult); err != nil {
				log.Fatal(err)
			}
			continue
		}

		var chooser Chooser
		if req.Remote {
			chooser = &remoteChooser{in: in, out: out}
		}
		result := runWith(func() {
			// TODO: add an entrypoint that takes a single test?
			// TODO: fail gracefully with non-existent tests?
			ok := rt.TestEntrypoint(req.Test, "", []Test{
				{
					Name: req.Test,
					Test: allTests[req.Test],
				},
			})
			if !ok {
				SetAbortError(ErrTestFailed)
			}
		}, seed, chooser, enableTracer, captureLog, logLevelOverride, io.Discard, req.ExtraEnv)

		metaResult := runResult{
			Seed:      result.Seed,
			Checksum:  result.Checksum,
			Failed:    result.Failed,
			LogOutput: result.LogOutput,
		}
		if result.Err != nil {
			metaResult.Err = result.Err.Error()
		}

		var msg any = metaResult
		if req.Remote {
			msg = childMsg{Result: &metaResult}
		}
		if err := out.Encode(msg); err != nil {
			log.Fatal(err)
		}
	}
}
