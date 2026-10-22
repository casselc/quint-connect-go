package translate

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/jellevandenhooff/gosim/internal/gosimtool"
	"github.com/jellevandenhooff/gosim/weave"
)

// weaveOverlay holds the woven sources of the packages being translated, by
// file path: every package load reads them instead of the files on disk.
// weaveHash identifies the aspects, for the translation cache.
var (
	weaveOverlay map[string][]byte
	weaveHash    string
)

// notAspects are tool imports that bring no aspects of their own.
var notAspects = map[string]bool{"github.com/DataDog/orchestrion": true}

// weaveModule weaves the packages of patterns with the aspects of the
// module's tool file (weave.ToolFile), setting weaveOverlay and weaveHash.
func weaveModule(patterns []string, cfg gosimtool.BuildConfig) {
	modDir, err := gosimtool.FindGoModDir()
	if err != nil {
		log.Fatal(err)
	}
	tool := weave.ToolFile(modDir)
	if tool == "" {
		return
	}
	imports, err := weave.Tool(tool)
	if err != nil {
		log.Fatal(err)
	}
	var aspectPkgs []string
	for _, p := range imports {
		if !notAspects[p] {
			aspectPkgs = append(aspectPkgs, p)
		}
	}
	if len(aspectPkgs) == 0 {
		return
	}
	pkgs, err := loadPackages(aspectPkgs, cfg, packages.NeedName|packages.NeedFiles, false)
	if err != nil {
		log.Fatal(err)
	}
	var aspects []*weave.Aspect
	for _, p := range pkgs {
		if len(p.GoFiles) == 0 {
			continue
		}
		file := filepath.Join(filepath.Dir(p.GoFiles[0]), "orchestrion.yml")
		if _, err := os.Stat(file); err != nil {
			continue
		}
		as, skipped, err := weave.Load(file)
		if err != nil {
			log.Fatal(err)
		}
		for _, s := range skipped {
			slog.Debug("weave: skipping aspect", "file", s.File, "aspect", s.ID, "why", s.Why)
		}
		aspects = append(aspects, as...)
	}
	if len(aspects) == 0 {
		return
	}
	weaveHash = weave.Hash(aspects)

	graph, err := loadPackages(patterns, cfg, loadDepGraph, true)
	if err != nil {
		log.Fatal(err)
	}
	overlay := map[string][]byte{}
	seen := map[string]bool{}
	var visit func(p *packages.Package)
	visit = func(p *packages.Package) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		if p.Module != nil { // not the standard library
			for _, f := range p.GoFiles {
				if _, done := overlay[f]; done {
					continue
				}
				src, err := os.ReadFile(f)
				if err != nil {
					log.Fatal(err)
				}
				out, err := weave.File(f, src, aspects)
				if err != nil {
					log.Fatal(err)
				}
				if out != nil {
					overlay[f] = out
				}
			}
		}
		for _, imp := range p.Imports {
			visit(imp)
		}
	}
	for _, p := range graph {
		visit(p)
	}
	weaveOverlay = overlay
	slog.Debug("weave: woven files", "count", len(overlay))
}
