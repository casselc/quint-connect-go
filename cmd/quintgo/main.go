// Command quintgo checks Go programs against Quint models from their
// telemetry.
//
//	quintgo scaffold -spec model.qnt -module M          print a binding skeleton for M
//	quintgo scaffold -system -binding b.yaml [-pkg p]   print a qtest.System test for the binding
//	quintgo lint     -binding b.yaml [-src DIR]         check a binding (and annotations, literal Record actions) against the model
//	quintgo lint     -sim DIR                           check simulation code for goroutines, wall-clock time and steps that escape it
//	quintgo lint     -weave DIR                         check code woven by qenv/weave for go statements and method values it cannot rewrite
//	quintgo steps    FILE...                            print the reconstructed per-actor traces
//	quintgo validate [-binding b.yaml] [-dir OUT] FILE...
//	                                                    reconstruct (or read), replay in Quint, report
//	quintgo itf      -binding b.yaml -out DIR FILE...   recorded steps -> model-level ITF, per actor
//	quintgo convert  -rules r.yaml [-out steps.jsonl] FILE...
//	                                                    an app's own OTLP/JSON spans -> a native step log
//	quintgo skills   [list | install [-target T] [-user] [-dir DIR] [-force]]
//	                                                    the toolkit's agent skills, into .agents/skills and .claude/skills
//
// FILE is detected by content: OTLP/JSON (e.g. the collector's file
// exporter output), a native step log (qobs.JSONLSink), or a model-level
// ITF trace (quintgo's, or `quint run --mbt` output with -spec/-module/-instance).
// Exit status of validate: 0 all actors conform and satisfy the invariants,
// 1 some do not, 2 usage or tool error.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/casselc/quint-connect-go/binding"
	"github.com/casselc/quint-connect-go/convert"
	"github.com/casselc/quint-connect-go/load"
	"github.com/casselc/quint-connect-go/otelio"
	"github.com/casselc/quint-connect-go/qlint"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/skills"
	"github.com/casselc/quint-connect-go/validate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	code := 0
	switch os.Args[1] {
	case "scaffold":
		err = scaffold(os.Args[2:])
	case "lint":
		code, err = lint(os.Args[2:])
	case "steps":
		err = steps(os.Args[2:])
	case "validate":
		code, err = runValidate(os.Args[2:])
	case "itf":
		err = toITF(os.Args[2:])
	case "convert":
		err = runConvert(os.Args[2:])
	case "skills":
		err = runSkills(os.Args[2:])
	case "cover":
		err = runCover(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quintgo:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  quintgo scaffold -spec model.qnt -module M
  quintgo scaffold -system -binding b.yaml [-pkg p]
  quintgo lint     -binding b.yaml [-src DIR] [-sim DIR]
  quintgo lint     -sim DIR | -weave DIR
  quintgo steps    [-actors a,b] [-actions a,b] [-since T] [-until T] [-pseudonymize f,g] [-binding b.yaml]
                   [-out steps.jsonl] FILE...   list, or select and sanitize (QUINTGO_SALT) into a step log
  quintgo validate [-binding b.yaml] [-dir OUT] [-backend rust|typescript] [-format auto|otlp|steps|itf]
                   [-spec M.qnt -module M -instance I] [-novars]
                   [-convert rules.yaml] [-invariants a,b] [-noitf] FILE...
  quintgo itf      -binding b.yaml -out DIR FILE...      recorded steps -> model-level ITF per actor
  quintgo cover    -covdir D1,D2 [-sim summary.json]     a real environment's coverage (binaries built with -cover),
                   against a simulation's (qcover.Summary JSON): what ran only for real, only simulated
  quintgo convert  -rules r.yaml [-out steps.jsonl] FILE...   an app's OTLP/JSON spans -> step log (see package convert)

  quintgo skills   [list | install [-target agents,claude,github,cursor|all] [-user] [-dir DIR] [-force]]
                   agent skills for this toolkit (default: .agents/skills and .claude/skills)

validate, steps and itf also take -convert r.yaml to convert OTLP/JSON spans on the way in.`)
	os.Exit(2)
}

func scaffold(args []string) error {
	fset := flag.NewFlagSet("scaffold", flag.ExitOnError)
	spec := fset.String("spec", "", "Quint spec")
	module := fset.String("module", "", "parameterised module to bind")
	system := fset.Bool("system", false, "print a qtest.System test for -binding instead of a binding")
	bpath := fset.String("binding", "", "binding file (with -system)")
	pkg := fset.String("pkg", "app", "package of the test (with -system)")
	quint := fset.String("quint", "quint", "quint binary")
	fset.Parse(args)
	if *system {
		if *bpath == "" {
			return fmt.Errorf("scaffold -system needs -binding (write one first: quintgo scaffold -spec model.qnt -module M)")
		}
		b, err := binding.Load(*bpath)
		if err != nil {
			return err
		}
		m, err := binding.LoadModel(*quint, b.SpecPath(), b.Module)
		if err != nil {
			return err
		}
		fmt.Print(systemTest(*pkg, *bpath, b, m))
		return nil
	}
	if *spec == "" || *module == "" {
		return fmt.Errorf("scaffold needs -spec and -module (or -system -binding b.yaml)")
	}
	m, err := binding.LoadModel(*quint, *spec, *module)
	if err != nil {
		return err
	}
	fmt.Printf("# quintgo binding for %s (generated skeleton: set the constants; override an argument by uncommenting it)\nspec: %s   # relative to this file\nmodule: %s\n\nconstants:\n", *module, *spec, *module)
	for _, c := range sortedKeys(m.Consts) {
		fmt.Printf("  %s: ''   # %s\n", c, m.Consts[c])
	}
	fmt.Printf("\nintern: {}      # arg: actor|process, for opaque ids the model names by small integers\n")
	fmt.Printf("outcomes: {}    # ok/error/<custom> -> Quint expression, used as {{.outcome}}\n")
	fmt.Printf("restart: ''     # parameterless action inserted when an actor's process changes\n\nactions:\n")
	for _, a := range m.ActionNames() {
		if a == "init" || a == "step" {
			continue // the state machine itself, not steps
		}
		fmt.Printf("  %s:   # %s\n", a, m.Signature(a))
		if ps := m.Actions[a]; len(ps) > 0 {
			// the defaults, commented out: uncomment to map a field differently
			fmt.Printf("    args:   # default: the step field of the same name\n")
			for _, p := range ps {
				fmt.Printf("      # %s: '%s'   # %s\n", p.Name, p.DefaultTemplate(), p.Type)
			}
		}
		fmt.Printf("    # expect: ''  # Quint bool over the post-state, from observed .obs.<name>\n")
	}
	fmt.Printf("\ninvariants: []  # vals of %s: %s\n", *module, strings.Join(sortedKeys(m.Vals), ", "))
	return nil
}

func lint(args []string) (int, error) {
	fset := flag.NewFlagSet("lint", flag.ExitOnError)
	bpath := fset.String("binding", "", "binding file")
	src := fset.String("src", "", "Go source tree to scan for //quint:action annotations and literal qobs.Record actions")
	sim := fset.String("sim", "", "Go source tree of simulation code (files importing qenv) to check for what escapes the simulation")
	weave := fset.String("weave", "", "Go source tree woven by qenv/weave to check for what weaving cannot rewrite")
	quint := fset.String("quint", "quint", "quint binary")
	fset.Parse(args)
	simFindings := 0
	if *weave != "" {
		fs, err := qlint.Woven(*weave)
		if err != nil {
			return 2, err
		}
		for _, f := range fs {
			fmt.Printf("%s: %s (%s)\n", f.Pos, f.What, f.Fix)
		}
		fmt.Printf("%d places under %s that weaving cannot rewrite\n", len(fs), *weave)
		simFindings += len(fs)
		if *bpath == "" && *sim == "" {
			if simFindings > 0 {
				return 1, nil
			}
			return 0, nil
		}
	}
	if *sim != "" {
		fs, err := qlint.Sim(*sim)
		if err != nil {
			return 2, err
		}
		for _, f := range fs {
			fmt.Printf("%s: %s (%s)\n", f.Pos, f.What, f.Fix)
		}
		fmt.Printf("%d places under %s escape the simulation\n", len(fs), *sim)
		simFindings += len(fs)
		if *bpath == "" {
			if simFindings > 0 {
				return 1, nil
			}
			return 0, nil
		}
	}
	b, err := binding.Load(*bpath)
	if err != nil {
		return 2, err
	}
	m, err := binding.LoadModel(*quint, b.SpecPath(), b.Module)
	if err != nil {
		return 2, err
	}
	errs := b.Lint(m)
	var warns []string
	if *src != "" {
		anns, err := binding.ScanAnnotations(*src)
		if err != nil {
			return 2, err
		}
		fmt.Printf("%d //quint:action annotations under %s\n", len(anns), *src)
		for _, a := range anns {
			fmt.Printf("  %-28s %-14s %s\n", a.Pos[strings.LastIndex(a.Pos, "/")+1:], a.Action, a.Func)
		}
		recs, err := binding.ScanRecords(*src)
		if err != nil {
			return 2, err
		}
		fmt.Printf("%d literal Record calls under %s\n", len(recs), *src)
		e, w := b.LintSource(m, anns, recs)
		errs, warns = append(errs, e...), w
	}
	for _, w := range warns {
		fmt.Println("warning:", w)
	}
	for _, e := range errs {
		fmt.Println("error:", e)
	}
	if len(errs) > 0 || simFindings > 0 {
		return 1, nil
	}
	fmt.Printf("binding %s fits %s (%d actions bound)\n", *bpath, b.Module, len(b.Actions))
	return 0, nil
}

func readSteps(files []string, rules *convert.Rules) ([]qtrace.Step, error) {
	var all []qtrace.Step
	for _, f := range files {
		if rules != nil {
			steps, ok, err := convertFile(f, rules)
			if err != nil {
				return nil, err
			}
			if ok {
				all = append(all, steps...)
				continue
			}
		}
		in, err := load.File(f, load.Unknown)
		if err != nil {
			return nil, err
		}
		if in.Format == load.ITF {
			return nil, fmt.Errorf("%s is an ITF trace, not recorded steps", f)
		}
		all = append(all, in.Steps...)
	}
	return all, nil
}

func steps(args []string) error {
	fset := flag.NewFlagSet("steps", flag.ExitOnError)
	conv := fset.String("convert", "", "convert OTLP/JSON spans with this rule file")
	out := fset.String("out", "", "write the selected, sanitized steps to this step log instead of listing them")
	actors := fset.String("actors", "", "keep these actors (comma-separated path.Match patterns)")
	actions := fset.String("actions", "", "keep these actions (comma-separated patterns)")
	since := fset.String("since", "", "keep steps recorded at or after this RFC 3339 time")
	until := fset.String("until", "", "keep steps recorded before this RFC 3339 time")
	pseud := fset.String("pseudonymize", "", "fields whose values become salted pseudonyms (arguments, obs.<name>, actor)")
	bpath := fset.String("binding", "", "keep only the fields this binding reads (and drop error texts)")
	quint := fset.String("quint", "quint", "quint binary (with -binding)")
	fset.Parse(args)
	rules, err := loadRules(*conv)
	if err != nil {
		return err
	}
	all, err := readSteps(fset.Args(), rules)
	if err != nil {
		return err
	}
	f := qtrace.Filter{Actors: qtrace.Fields(*actors), Actions: qtrace.Fields(*actions),
		Pseudonymize: qtrace.Fields(*pseud), Salt: os.Getenv("QUINTGO_SALT")}
	for _, t := range []struct {
		s  string
		to *time.Time
	}{{*since, &f.Since}, {*until, &f.Until}} {
		if t.s != "" {
			if *t.to, err = time.Parse(time.RFC3339, t.s); err != nil {
				return fmt.Errorf("steps: %w", err)
			}
		}
	}
	if len(f.Pseudonymize) > 0 && f.Salt == "" {
		fmt.Fprintln(os.Stderr, "quintgo: QUINTGO_SALT is not set: pseudonyms can be reversed by guessing the values")
	}
	if *bpath != "" {
		b, err := binding.Load(*bpath)
		if err != nil {
			return err
		}
		m, err := binding.LoadModel(*quint, b.SpecPath(), b.Module)
		if err != nil {
			return err
		}
		f.Keep = b.UsedFields(m)
	}
	all = f.Apply(all)
	if *out != "" {
		w, err := os.Create(*out)
		if err != nil {
			return err
		}
		if err := qtrace.WriteJSONL(w, all); err != nil {
			w.Close()
			return err
		}
		fmt.Printf("%d steps written to %s\n", len(all), *out)
		return w.Close()
	}
	traces, issues := qtrace.Reconstruct(all)
	for _, i := range issues {
		fmt.Println("issue:", i)
	}
	for _, t := range traces {
		fmt.Printf("actor %s: %d steps, processes %v\n", t.Actor, len(t.Steps), t.Processes)
		for _, i := range t.Issues {
			fmt.Println("  issue:", i)
		}
		for i, s := range t.Steps {
			fmt.Printf("  [%2d] seq %-3d %-10.10s %s\n", i, s.Seq, s.Process, s.String())
		}
	}
	return nil
}

func runValidate(args []string) (int, error) {
	fset := flag.NewFlagSet("validate", flag.ExitOnError)
	bpath := fset.String("binding", "", "binding file (optional for ITF written by quintgo)")
	dir := fset.String("dir", "", "where to write generated modules and ITF (default: a temp dir)")
	backend := fset.String("backend", "", "quint test backend (default $QUINTGO_BACKEND or rust)")
	quint := fset.String("quint", "quint", "quint binary")
	format := fset.String("format", "auto", "input format: auto, otlp, steps (native step log) or itf")
	spec := fset.String("spec", "", "ITF input: the model, if neither the trace nor a binding names it")
	module := fset.String("module", "", "ITF input: the parameterised module")
	instance := fset.String("instance", "", "ITF input: replay against this instance module (e.g. currentDesign), e.g. for quint run --mbt traces")
	novars := fset.Bool("novars", false, "ITF input: do not compare model variables carried by the trace")
	verbose := fset.Bool("v", false, "print quint's output")
	conv := fset.String("convert", "", "convert OTLP/JSON spans with this rule file (see quintgo convert)")
	invs := fset.String("invariants", "", "comma-separated model invariants to check instead of the binding's")
	noITF := fset.Bool("noitf", false, "do not write the ITF of conforming runs (faster for models with a large state)")
	fset.Parse(args)
	rules, err := loadRules(*conv)
	if err != nil {
		return 2, err
	}
	if fset.NArg() == 0 {
		usage()
	}
	opts := validate.Options{Quint: *quint, Backend: *backend, Dir: *dir, NoITF: *noITF}
	var b *binding.Binding
	if *bpath != "" {
		if b, err = binding.Load(*bpath); err != nil {
			return 2, err
		}
	}
	var steps []qtrace.Step
	var itfs []*load.Input
	for _, f := range fset.Args() {
		if rules != nil {
			cs, ok, err := convertFile(f, rules)
			if err != nil {
				return 2, err
			}
			if ok {
				fmt.Printf("input %s: OTLP/JSON spans, converted with %s: %d steps\n", f, *conv, len(cs))
				steps = append(steps, cs...)
				continue
			}
		}
		in, err := load.File(f, load.Format(*format))
		if err != nil {
			return 2, err
		}
		fmt.Printf("input %s: %s\n", f, map[load.Format]string{load.OTLP: "OTLP/JSON spans", load.StepLog: "native step log",
			load.ITF: "model-level ITF", load.Unknown: "empty"}[in.Format])
		if in.Format == load.ITF {
			itfs = append(itfs, in)
		} else {
			steps = append(steps, in.Steps...)
		}
	}
	ok := true
	if len(steps) > 0 || len(itfs) == 0 {
		if *bpath == "" {
			return 2, fmt.Errorf("recorded steps need -binding")
		}
		c, err := validate.NewChecker(*bpath, opts)
		if err != nil {
			return 2, err
		}
		if *invs != "" {
			if c, err = c.WithInvariants(strings.Split(*invs, ",")...); err != nil {
				return 2, err
			}
		}
		rep, err := c.Check(context.Background(), steps)
		if err != nil {
			return 2, err
		}
		fmt.Print(rep.Summary())
		if *verbose {
			for _, a := range rep.Actors {
				fmt.Println(a.Output)
			}
		}
		ok = ok && rep.OK()
	}
	for i, in := range itfs {
		o := opts
		if o.Dir != "" && len(itfs) > 1 {
			o.Dir = fmt.Sprintf("%s/itf%d", o.Dir, i)
		}
		r, err := validate.ValidateITF(context.Background(), in.ITF, validate.ITFOptions{Options: o, Binding: b,
			Spec: *spec, Module: *module, Instance: *instance, NoVarCheck: *novars})
		if err != nil {
			return 2, fmt.Errorf("%s: %w", in.Path, err)
		}
		fmt.Print(r.Summary())
		if *verbose {
			fmt.Println(r.Output)
		}
		ok = ok && r.OK()
	}
	if ok {
		fmt.Println("RESULT: trace conforms to the model and satisfies the invariants")
		return 0, nil
	}
	fmt.Println("RESULT: VIOLATION")
	return 1, nil
}

// toITF writes one model-level ITF per actor from recorded steps.
func toITF(args []string) error {
	fset := flag.NewFlagSet("itf", flag.ExitOnError)
	bpath := fset.String("binding", "", "binding file")
	out := fset.String("out", ".", "output directory: <actor>.itf.json per actor")
	conv := fset.String("convert", "", "convert OTLP/JSON spans with this rule file")
	fset.Parse(args)
	rules, err := loadRules(*conv)
	if err != nil {
		return err
	}
	if *bpath == "" || fset.NArg() == 0 {
		usage()
	}
	b, err := binding.Load(*bpath)
	if err != nil {
		return err
	}
	all, err := readSteps(fset.Args(), rules)
	if err != nil {
		return err
	}
	traces, issues, err := b.StepsToITF(all)
	for _, i := range issues {
		fmt.Println("issue:", i)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for actor, data := range traces {
		f := filepath.Join(*out, strings.Map(func(r rune) rune {
			if r == '/' || r == ' ' {
				return '_'
			}
			return r
		}, actor)+".itf.json")
		if err := os.WriteFile(f, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("actor %s -> %s\n", actor, f)
	}
	return nil
}

func loadRules(p string) (*convert.Rules, error) {
	if p == "" {
		return nil, nil
	}
	return convert.Load(p)
}

// convertFile converts an OTLP/JSON file; ok is false for other formats.
func convertFile(path string, rules *convert.Rules) ([]qtrace.Step, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if load.Sniff(data) != load.OTLP {
		return nil, false, nil
	}
	tds, err := otelio.ReadTracesData(bytes.NewReader(data))
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	steps, err := rules.Steps(tds)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return steps, true, nil
}

func runConvert(args []string) error {
	fset := flag.NewFlagSet("convert", flag.ExitOnError)
	rpath := fset.String("rules", "", "rule file mapping spans to model actions")
	out := fset.String("out", "-", "native step log to write (- for stdout)")
	fset.Parse(args)
	if *rpath == "" || fset.NArg() == 0 {
		usage()
	}
	rules, err := convert.Load(*rpath)
	if err != nil {
		return err
	}
	var all []qtrace.Step
	for _, f := range fset.Args() {
		steps, ok, err := convertFile(f, rules)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s is not OTLP/JSON", f)
		}
		all = append(all, steps...)
	}
	w := os.Stdout
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := qtrace.WriteJSONL(w, all); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d steps\n", len(all))
	return nil
}

func runSkills(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		list, err := skills.List()
		if err != nil {
			return err
		}
		for _, s := range list {
			fmt.Printf("%s\n    %s\n", s.Name, s.Description)
		}
		fmt.Println("\ninstall into a project: quintgo skills install   (.agents/skills and .claude/skills; -target, -user, -dir)")
		return nil
	}
	if args[0] != "install" {
		usage()
	}
	fset := flag.NewFlagSet("skills install", flag.ExitOnError)
	target := fset.String("target", "", "comma-separated: agents (.agents/skills), claude (.claude/skills), github, cursor, or all (default agents,claude)")
	dir := fset.String("dir", "", "install into this skills directory instead of the targets")
	user := fset.Bool("user", false, "install for the user (under $HOME) instead of the current project")
	force := fset.Bool("force", false, "overwrite existing skill files")
	fset.Parse(args[1:])
	dirs := []string{*dir}
	if *dir == "" {
		root := "."
		if *user {
			h, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			root = h
		}
		var names []string
		if *target != "" {
			names = strings.Split(*target, ",")
		}
		var err error
		if dirs, err = skills.TargetDirs(root, names); err != nil {
			return err
		}
	}
	for _, d := range dirs {
		paths, err := skills.Install(d, *force)
		for _, p := range paths {
			fmt.Println("wrote", p)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// systemTest is a qtest.System test for the binding: a Run that deploys
// nothing yet, one commented qobs.Record per bound or modelled action with
// the fields the binding reads, a conformance test that fails until steps
// are recorded, and a mutant claim to fill in.
func systemTest(pkg, bpath string, b *binding.Binding, m *binding.Model) string {
	var rec strings.Builder
	for _, a := range m.ActionNames() {
		if a == "init" || a == "step" || a == b.Restart {
			continue
		}
		// the parameters' fields in the signature's order, then the rest
		// (observations) as Fields sorts them
		var fields []string
		rest := map[string]bool{}
		for _, f := range b.Actions[a].Fields(m.Actions[a]...) {
			rest[f] = true
		}
		for _, p := range m.Actions[a] {
			if rest[p.Name] {
				fields = append(fields, p.Name)
				delete(rest, p.Name)
			}
		}
		fields = append(fields, sortedKeys(rest)...)
		kv := ""
		for _, f := range fields {
			v := f
			if o, ok := strings.CutPrefix(f, "obs."); ok {
				v = "observed" + strings.ToUpper(o[:1]) + o[1:]
			}
			kv += fmt.Sprintf(", %q, %s", f, v)
		}
		fmt.Fprintf(&rec, "\t\t\t// qobs.Record(ctx, %q, err%s) // %s\n", a, kv, m.Signature(a))
	}
	claim := "qtest.Some(qtest.Rejects(\"ACTION\"))"
	if len(b.Invariants) > 0 {
		claim = fmt.Sprintf("qtest.Some(qtest.Violates(%q))", b.Invariants[0])
	}
	return fmt.Sprintf(`package %[1]s

import (
	"context"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

// system deploys the application, with mutation injected (0: none), in a
// simulation made from c. Every workflow runs it: Explore (seeds),
// quinthegel.Shrink (Hegel), Sweep, Fuzz, Mutate.
func system(mutation int) qtest.System {
	return qtest.System{Binding: %[2]q, Run: func(c qenv.Chooser) error {
		s := qenv.New(c)
		defer s.Close()
		// Deploy the application here: its services (qenv.NewService),
		// stores (qenv.NewStore), queues (qenv.NewQueue) and crashable
		// processes (s.Process), its code written against those seams and
		// qenv.Go, qenv.Now, qenv.Sleep. Record each model step where it
		// takes effect, with the fields the binding reads:
		s.Spawn("client", func(ctx context.Context) {
%[3]s		})
		return s.Run(time.Minute) // a task waiting forever is an error (qenv.ErrStuck)
	}}
}

// The faithful application conforms through every seed's faults.
func TestConforms(t *testing.T) {
	qtest.RequireQuint(t)
	x := system(0).Explore(t, qtest.ExploreConfig{Seeds: 30})
	if len(x.Runs[0].Steps) == 0 {
		t.Fatal("scaffold: Run records no steps yet: deploy the application in it and record its steps")
	}
	x.Expect(t, qtest.Conforms())
}

// A mutant must be caught, and how: say which check fails (Rejects,
// Mismatch, Violates; over runs Some, All, None). A check no mutant can
// fail proves nothing.
func TestMutantIsCaught(t *testing.T) {
	qtest.RequireQuint(t)
	t.Skip("scaffold: inject a bug with system(1), state how it shows, and remove this line")
	system(1).Explore(t, qtest.ExploreConfig{Seeds: 30}).Expect(t, %[4]s)
}
`, pkg, bpath, rec.String(), claim)
}
