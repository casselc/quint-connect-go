package qweave

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Config says what to weave.
type Config struct {
	// Dir is the module's directory (default: the working directory).
	Dir string
	// Tool is the tool file naming the aspect packages (default: Dir's
	// gosim.tool.go if GOSIM, else its orchestrion.tool.go).
	Tool string
	// Tags are build tags, as the build will use them.
	Tags []string
	// Patterns are the packages whose dependency graph is woven (default
	// "./...", with tests).
	Patterns []string
	// Env is the go command's environment (default os.Environ()).
	Env []string
	// GOSIM prefers gosim.tool.go over orchestrion.tool.go.
	GOSIM bool
}

// A Result is the woven files, by the path of the file they replace.
type Result struct {
	Files   map[string][]byte
	Skipped []string
	Aspects []*Aspect
	Hash    string // of the aspects and the woven files
}

// notAspects are tool imports that bring no aspects of their own.
var notAspects = map[string]bool{"github.com/DataDog/orchestrion": true}

// ToolFile returns the tool file of the module in dir: gosim.tool.go (with
// preferGosim) or orchestrion.tool.go, whichever exists, else "".
func ToolFile(dir string, preferGosim bool) string {
	names := []string{"orchestrion.tool.go", "gosim.tool.go"}
	if preferGosim {
		names = []string{"gosim.tool.go", "orchestrion.tool.go"}
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ToolImports lists the packages a tool file imports.
func ToolImports(path string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, p)
	}
	return out, nil
}

func (c Config) packagesConfig(mode packages.LoadMode, tests bool) *packages.Config {
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	cfg := &packages.Config{Mode: mode, Dir: c.Dir, Env: env, Tests: tests, Fset: token.NewFileSet()}
	if len(c.Tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(c.Tags, ",")}
	}
	return cfg
}

// Aspects loads the aspects of the tool file's packages.
func (c Config) Aspects() ([]*Aspect, []Skipped, error) {
	tool := c.Tool
	if tool == "" {
		tool = ToolFile(c.dir(), c.GOSIM)
	}
	if tool == "" {
		return nil, nil, nil
	}
	imports, err := ToolImports(tool)
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	for _, p := range imports {
		if !notAspects[p] {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}
	pkgs, err := packages.Load(c.packagesConfig(packages.NeedName|packages.NeedFiles, false), paths...)
	if err != nil {
		return nil, nil, err
	}
	var aspects []*Aspect
	var skipped []Skipped
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, nil, fmt.Errorf("qweave: aspect package %s: %v", p.PkgPath, p.Errors[0])
		}
		if len(p.GoFiles) == 0 {
			continue
		}
		file := filepath.Join(filepath.Dir(p.GoFiles[0]), "orchestrion.yml")
		if _, err := os.Stat(file); err != nil {
			continue
		}
		as, sk, err := Load(file)
		if err != nil {
			return nil, nil, err
		}
		aspects, skipped = append(aspects, as...), append(skipped, sk...)
	}
	return aspects, skipped, nil
}

func (c Config) dir() string {
	if c.Dir != "" {
		return c.Dir
	}
	d, _ := os.Getwd()
	return d
}

// Weave weaves the dependency graph of the config's packages (outside the
// standard library) with the tool file's aspects.
func Weave(c Config) (*Result, error) {
	aspects, skipped, err := c.Aspects()
	if err != nil {
		return nil, err
	}
	r := &Result{Files: map[string][]byte{}, Aspects: aspects}
	for _, s := range skipped {
		r.Skipped = append(r.Skipped, s.String())
	}
	if len(aspects) == 0 {
		return r, nil
	}
	patterns := c.Patterns
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes |
		packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps | packages.NeedModule
	pc := c.packagesConfig(mode, true)
	pkgs, err := packages.Load(pc, patterns...)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	var visitErr error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if visitErr != nil || p.Module == nil || len(p.Errors) > 0 {
			return // the standard library, or a package that does not build
		}
		pi := pkgInfo{fset: pc.Fset, info: p.TypesInfo, path: p.PkgPath, root: p.Module.Main}
		for i, f := range p.Syntax {
			name := p.CompiledGoFiles[i]
			if done[name] {
				continue
			}
			done[name] = true
			src, err := os.ReadFile(name)
			if err != nil {
				visitErr = err
				return
			}
			out, sk, err := weaveFile(pi, f, src, name, aspects)
			if err != nil {
				visitErr = fmt.Errorf("qweave: %s: %w", name, err)
				return
			}
			r.Skipped = append(r.Skipped, sk...)
			if out != nil {
				r.Files[name] = out
			}
		}
	})
	if visitErr != nil {
		return nil, visitErr
	}
	h := Hash(aspects)
	names := make([]string, 0, len(r.Files))
	for n := range r.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h = hash(h + n + string(r.Files[n]))
	}
	r.Hash = h
	return r, nil
}

// WriteOverlay writes the woven files under dir and the go command's
// overlay file (go build -overlay) naming them, and returns its path.
func (r *Result) WriteOverlay(dir string) (string, error) {
	replace := map[string]string{}
	names := make([]string, 0, len(r.Files))
	for n := range r.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		p := filepath.Join(dir, fmt.Sprintf("%04d_%s", i, filepath.Base(n)))
		if err := os.WriteFile(p, r.Files[n], 0o644); err != nil {
			return "", err
		}
		replace[n] = p
	}
	b, err := json.MarshalIndent(map[string]any{"Replace": replace}, "", "\t")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "overlay.json")
	return path, os.WriteFile(path, b, 0o644)
}
