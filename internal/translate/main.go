package translate

import (
	"cmp"
	_ "embed"
	"flag"
	"go/token"
	"log"
	"os"
	"path"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/packages"

	"github.com/jellevandenhooff/gosim/internal/gosimtool"
	"github.com/jellevandenhooff/gosim/internal/translate/cache"
)

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

var skippedPackagesGo123 = map[string]bool{
	"runtime": true, // XXX wait what
	"errors":  true,
	"reflect": true,
	// "strings": true,
	"strconv": true,
	// embed: true, // this will also have io.EOF problems... unless we can just use the original there please???
	// XXX: for all unconverted packages, figure out all references to converted packages, and have a plan.
	"embed":    true, // XXX can we link back to the original here somehow???
	"math":     true,
	"math/big": true,

	"unsafe": true,

	"runtime/coverage": true, // XXX for now
	"runtime/metrics":  true, // XXX for now
	"runtime/pprof":    true, // XXX for now

	// XXX: rewrite internal/cpu to golang.org/x/sys/cpu?
	"vendor/golang.org/x/sys/cpu": true, // XXX for now

	"unique": true, // XXX: yes

	"testing":                     true,
	"testing/internal/testdeps":   true,
	"internal/reflectlite":        true,
	gosimruntimePackage:           true,
	gosimruntimePackage + "_test": true, // eh
	gosimruntimePackage + ".test": true, // eh
	// reflectPackage: true,
}

var keepAsmPackagesGo123 = map[string]bool{
	"crypto/aes":                                   true,
	"crypto/internal/boring/sig":                   true,
	"crypto/internal/nistec":                       true,
	"crypto/md5":                                   true,
	"crypto/sha1":                                  true,
	"crypto/sha256":                                true,
	"crypto/sha512":                                true,
	"crypto/subtle":                                true,
	"crypto/internal/bigmod":                       true,
	"crypto/internal/edwards25519/field":           true,
	"vendor/golang.org/x/crypto/chacha20":          true,
	"vendor/golang.org/x/crypto/internal/poly1305": true,
	"vendor/golang.org/x/crypto/chacha20poly1305":  true,
	"vendor/golang.org/x/crypto/sha3":              true,
	"hash/crc32":                                   true,
	"crypto/internal/fips140/aes":                  true,
	"crypto/internal/fips140/aes/gcm":              true,
	"crypto/internal/fips140/bigmod":               true,
	"crypto/internal/fips140/edwards25519/field":   true,
	"crypto/internal/fips140/nistec":               true,
	"crypto/internal/fips140/sha256":               true,
	"crypto/internal/fips140/sha3":                 true,
	"crypto/internal/fips140/sha512":               true,
	"crypto/internal/fips140/subtle":               true,

	"net/url":  true, // XXX: linkname setpath nonsense
	"net/http": true, // XXX: linkname roundtrip nonsense

	"github.com/cespare/xxhash/v2": true,
}

var PublicExportHacks = map[string][]string{
	"encoding/binary":                  {"littleEndian"},
	"internal/poll":                    {"errNetClosing"},
	"github.com/golang/protobuf/proto": {"enumsByName"},
}

// XXX: replace the package instead
var replacements = map[packageSelector]packageSelector{
	{Pkg: "runtime", Selector: "SetFinalizer"}: {Pkg: gosimruntimePackage, Selector: "SetFinalizer"},
	{Pkg: "runtime", Selector: "GOOS"}:         {Pkg: gosimruntimePackage, Selector: "GOOS"},
	{Pkg: "runtime", Selector: "Gosched"}:      {Pkg: gosimruntimePackage, Selector: "Yield"},
}

const gosimModPath = gosimtool.Module

const (
	gosimruntimePackage = gosimModPath + "/gosimruntime"
	hooksGo123Package   = gosimModPath + "/internal/hooks/go123"
	reflectPackage      = gosimModPath + "/internal/reflect"
	simulationPackage   = gosimModPath + "/internal/simulation"
	testingPackage      = gosimModPath + "/internal/testing"
)

var TranslatedRuntimePackages = []string{
	hooksGo123Package,
	reflectPackage,
	simulationPackage,
	testingPackage,
}

const (
	// loadDepGraph fairly quickly loads the dependency graph
	loadDepGraph = packages.NeedName | packages.NeedFiles | packages.NeedModule | packages.NeedImports | packages.NeedDeps
	// loadSyntaxAndTypes relatively slowly loads detailed types and syntax information
	loadSyntaxAndTypes = packages.NeedSyntax | packages.NeedName | packages.NeedTypes |
		packages.NeedTypesInfo | packages.NeedFiles | packages.NeedImports
)

func loadPackages(patterns []string, b gosimtool.BuildConfig, mode packages.LoadMode, tests bool) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode:  mode,
		Tests: tests,
		Fset:  token.NewFileSet(),
	}

	// apply build config to packages config
	// TODO: build tags plan (rename files, force GOOS and GOARCH)
	// purego: since Go 1.24 the crypto assembly (crypto/internal/fips140/...)
	// refers to package-level Go variables, which translation moves into
	// per-machine globals. Use the pure Go implementations instead.
	tags := []string{"sim", "purego"}
	if b.Race {
		tags = append(tags, "race")
	}
	// TODO: if we support varying GOARCH here, we should select the arch-specific hooks at runtime...
	env := append(os.Environ(), "GOOS="+b.GOOS, "GOARCH="+b.GOARCH, "CGO_ENABLED=0")
	cfg.BuildFlags = []string{"-tags", strings.Join(tags, ",")}
	cfg.Env = env
	cfg.Overlay = weaveOverlay

	// load packages
	packages, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		log.Println(err)
		log.Fatal("failed to load packages... missing go.mod deps?")
	}

	// check for errors
	for _, pkg := range packages {
		if pkg.Errors != nil {
			// TODO: ignore errors in tests outside of what we care about?
			log.Println("errors for ", pkg.PkgPath)
			for _, err := range pkg.Errors {
				log.Println(err)
			}
			os.Exit(1)
		}
	}

	return packages, nil
}

// reloadUncachedPackages loads the types and AST for the given uncached package
// paths. It takes special care to load tests only where appropriate, because
// broken tests in dependencies are not uncommon.
//
// loadPackages has a binary flag for loading tests, which if enabled loads
// tests for the explicitly listed packages. It does not load tests for
// dependencies.
//
// When we reload packages here we explicitly list all packages we want to load,
// including dependencies whose tests we do not care about. To not load tests
// for those we make to calls to loadPackages, one with and one without tests.
func reloadUncachedPackages(listedPkgs []*packages.Package, uncachedPackages map[string]struct{}, cfg gosimtool.BuildConfig) map[string]*packages.Package {
	// determine packages we want to load tests for based on
	// the original command line arguments
	listedPkgPaths := make(map[string]struct{})
	for _, pkg := range listedPkgs {
		if fromGosim := slices.Contains(TranslatedRuntimePackages, pkg.PkgPath); fromGosim {
			continue
		}
		listedPkgPaths[pkg.PkgPath] = struct{}{}
	}

	// reloadByTests is packages to reload, with tests enabled or not
	reloadByTests := make(map[bool][]string)
	for path := range uncachedPackages {
		_, ok := listedPkgPaths[path]
		reloadByTests[ok] = append(reloadByTests[ok], path)
	}

	// reload packages in parallel
	var mu sync.Mutex
	reloadedByTests := make(map[bool][]*packages.Package)
	var g errgroup.Group
	for test, pkgs := range reloadByTests {
		g.Go(func() error {
			reloaded, err := loadPackages(pkgs, cfg, loadSyntaxAndTypes, test)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			reloadedByTests[test] = reloaded
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		log.Fatal(err)
	}

	// merge results
	pkgsWithTypesAndAst := make(map[string]*packages.Package)
	for _, pkgs := range reloadedByTests {
		for _, pkg := range pkgs {
			pkgsWithTypesAndAst[pkg.ID] = pkg
		}
	}

	return pkgsWithTypesAndAst
}

type packageKind string

const (
	PackageKindTestBinary = "testbinary"
	PackageKindBase       = "base"
	PackageKindForTest    = "fortest"
	PackageKindTests      = "tests"
)

func classifyPackage(pkg *packages.Package) (packageKind, string) {
	switch {
	case pkg.Name == "main" && strings.HasSuffix(pkg.PkgPath, ".test"):
		// XXX: is this correct?
		return PackageKindTestBinary, strings.TrimSuffix(pkg.PkgPath, ".test")

	case strings.HasSuffix(pkg.Name, "_test"):
		return PackageKindTests, strings.TrimSuffix(pkg.PkgPath, "_test")

	case strings.HasSuffix(pkg.ID, ".test]"):
		return PackageKindForTest, pkg.PkgPath

	case !strings.HasSuffix(pkg.Name, "_test") && pkg.PkgPath == pkg.ID:
		return PackageKindBase, pkg.PkgPath

	default:
		log.Fatal("weird package", pkg.Name, pkg.PkgPath, pkg.ID)
		panic("unreachable")
	}
}

func collectImports(roots []*packages.Package, skip map[string]bool) []*packages.Package {
	seen := make(map[*packages.Package]bool)
	var order []*packages.Package
	var visit func(pkg *packages.Package)
	visit = func(pkg *packages.Package) {
		if skip[pkg.PkgPath] || seen[pkg] {
			return
		}
		seen[pkg] = true
		order = append(order, pkg)
		for _, dep := range pkg.Imports {
			visit(dep)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	slices.SortFunc(order, func(a, b *packages.Package) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return order
}

func writeGoModFile(modDir string, modFile *modfile.File, writer *outputWriter) {
	isGosim := modFile.Module.Mod.Path == gosimModPath
	// take the existing go.mod and make it work for a sub-directory containing
	// a module translated
	if err := modFile.AddModuleStmt("translated"); err != nil {
		log.Fatal(err)
	}

	if isGosim {
		// special case running translate in the gosim module
		if err := modFile.AddRequire(gosimModPath, "v0.0.0"); err != nil {
			log.Fatal(err)
		}
		if err := modFile.AddReplace(gosimModPath, "", "../../../", ""); err != nil {
			log.Fatal(err)
		}
	} else {
		// adjust relative paths for the new module's location
		for _, replace := range modFile.Replace {
			// > Third, filesystem paths found in "replace" directives are
			// represented by a path with an empty version.
			if replace.New.Version == "" && !path.IsAbs(replace.New.Path) {
				// The output module is located three directories deeper than the old module.
				newPath := path.Join("../../../", replace.New.Path)
				if err := modFile.AddReplace(replace.Old.Path, replace.Old.Version, newPath, ""); err != nil {
					log.Fatal(err)
				}
			}
		}
	}

	bytes, err := modFile.Format()
	if err != nil {
		log.Fatal(err)
	}
	if err := writer.stage("go.mod", bytes); err != nil {
		log.Fatal(err)
	}
	goSumBytes, err := os.ReadFile(path.Join(modDir, "go.sum"))
	if err != nil {
		log.Fatal(err)
	}
	if err := writer.stage("go.sum", goSumBytes); err != nil {
		log.Fatal(err)
	}
}

type TranslateInput struct {
	Packages []string
	Cfg      gosimtool.BuildConfig
}

func Translate(input *TranslateInput) (*gosimtool.TranslateOutput, error) {
	// XXX: dedup?
	modDir, err := gosimtool.FindGoModDir()
	if err != nil {
		log.Fatal(err)
	}
	rootOutputDir := path.Join(modDir, gosimtool.OutputDirectory, "translated", input.Cfg.AsDirname())

	cacheDir := path.Join(modDir, gosimtool.OutputDirectory)
	if override := os.Getenv("GOSIMCACHE"); override != "" {
		cacheDir = override
	}

	cachePath := path.Join(cacheDir, "cache.sqlite3")
	if err := os.MkdirAll(path.Dir(cachePath), 0o755); err != nil {
		log.Fatal(err)
	}
	db, err := cache.NewDB(cachePath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	cache := cache.NewCache(db)
	defer cache.Clean()
	// XXX: cap size at something reasonable? 100x working set?

	if err := os.MkdirAll(rootOutputDir, 0o755); err != nil {
		log.Fatal(err)
	}
	return translatePackages(cache, input.Packages, rootOutputDir, input.Cfg)
}

func buildReplacePackagesAndPackageNames(convertPkgs, allPkgs []*packages.Package) (replacedPkgs map[string]string, packageNames map[string]string) {
	packageNames = make(map[string]string)
	packageNames["golang.org/x/sys/cpu"] = "cpu" // XXX: here because we replace the vendored one with the common one
	for _, pkg := range allPkgs {
		packageNames[pkg.PkgPath] = pkg.Name
	}

	replacedPkgs = make(map[string]string)
	for _, pkg := range convertPkgs {
		inputPackage := pkg.PkgPath
		outputPackage := "translated/" + gosimtool.ReplaceSpecialPackages(pkg.PkgPath)
		replacedPkgs[inputPackage] = outputPackage
		packageNames[outputPackage] = packageNames[inputPackage]
	}

	// override reflect and testing
	replacedPkgs["reflect"] = replacedPkgs[reflectPackage]
	replacedPkgs["internal/reflectlite"] = replacedPkgs[reflectPackage]
	replacedPkgs["testing"] = replacedPkgs[testingPackage]

	// handle the linkname in the os package
	replacedPkgs["net"] = "translated/" + gosimtool.ReplaceSpecialPackages("net")

	// not replaced, but need to know for rewrites
	replacedPkgs[gosimruntimePackage] = gosimruntimePackage
	return
}

func checkGosimDep(modFile *modfile.File) {
	// work in the gosim module
	if isGosim := modFile.Module.Mod.Path == gosimModPath; isGosim {
		return
	}
	for _, req := range modFile.Require {
		// work if there is an explicit dependency
		if req.Mod.Path == gosimModPath {
			return
		}
	}
	// complain otherwise
	log.Fatalf("current module does not depend on %v, try running init", gosimModPath)
}

func checkSingleModule(modPath string, pkgs []*packages.Package) {
	for _, pkg := range pkgs {
		// allow packages from the current module
		if pkg.Module.GoMod == modPath {
			continue
		}
		// allow packages explicitly added by translate
		_, path := classifyPackage(pkg)
		if slices.Contains(TranslatedRuntimePackages, path) {
			continue
		}
		// complain about others
		log.Fatal("packages from outside module modules ", pkg.PkgPath, " ", modPath)
	}
}

func translatePackages(cache *cache.Cache, listPatterns []string, rootOutputDir string, cfg gosimtool.BuildConfig) (*gosimtool.TranslateOutput, error) {
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	listPatterns = append(listPatterns, TranslatedRuntimePackages...)

	loadOverlay(cfg)

	listedPkgs, err := loadPackages(listPatterns, cfg, loadDepGraph, true)
	if err != nil {
		log.Fatal(err)
	}
	if len(listedPkgs) == 0 {
		log.Fatal("no packages")
	}

	modPath, modFile, err := gosimtool.FindGoMod()
	if err != nil {
		log.Fatal(err)
	}
	modDir := path.Dir(modPath)

	checkGosimDep(modFile)
	checkSingleModule(modPath, listedPkgs)

	allPkgs := collectImports(listedPkgs, nil)
	convertPkgs := collectImports(listedPkgs, skippedPackagesGo123)

	packageGraph := newDepGraph()
	basePkgs := make(map[string]*packages.Package)
	pkgById := make(map[string]*packages.Package)
	for _, pkg := range convertPkgs {
		packageGraph.addNode(pkg.ID)
		pkgById[pkg.ID] = pkg

		if kind, path := classifyPackage(pkg); kind == PackageKindBase {
			basePkgs[path] = pkg
		}
	}

	for _, pkg := range convertPkgs {
		for _, dep := range pkg.Imports {
			if _, ok := packageGraph.nodes[dep.ID]; !ok {
				// XXX?
				continue
			}
			packageGraph.addDep(pkg.ID, dep.ID)
		}

		// XXX: add a package from the "for test" to the "main" package
		kind, path := classifyPackage(pkg)
		if kind == PackageKindForTest || kind == PackageKindTests {
			if basePkgs[path] != nil {
				packageGraph.addDep(pkg.ID, basePkgs[path].ID)
			} else {
				log.Println("huh", kind, pkg.PkgPath, path)
				log.Fatal(":(")
			}
		}
	}

	translateToolHash := computeTranslateToolHash(cfg)

	numWorkers := 32

	packageHashes := make(map[string]Hash)

	buildInParallel(packageGraph, numWorkers, packageHashes, func(pkgId string, importHashes map[string]Hash) Hash {
		return computePackageHash(translateToolHash, pkgById[pkgId], importHashes)
	})

	allResults := make(map[string]*TranslatePackageResult)
	cacheHits := make(map[Hash]bool)

	uncachedPackages := make(map[string]struct{})
	for pkgId, hash := range packageHashes {
		pkg := pkgById[pkgId]

		res, err := cacheGet(cache, hash)
		if err != nil {
			log.Fatal(err)
		}
		if res != nil {
			allResults[pkgId] = res
			cacheHits[hash] = true // record cache hits so we don't update the cache for them
		} else {
			uncachedPackages[strings.TrimSuffix(pkg.PkgPath, "_test")] = struct{}{}
		}
	}

	pkgsWithTypesAndAst := reloadUncachedPackages(listedPkgs, uncachedPackages, cfg)

	replacedPkgs, packageNames := buildReplacePackagesAndPackageNames(convertPkgs, allPkgs)

	buildInParallel(packageGraph, numWorkers, allResults, func(pkgId string, localResults map[string]*TranslatePackageResult) *TranslatePackageResult {
		return translatePackage(&translatePackageArgs{
			cfg:                cfg,
			pkg:                pkgById[pkgId],
			replacedPkgs:       replacedPkgs,
			hooksPackage:       hooksGo123Package,
			packageNames:       packageNames,
			importResults:      localResults,
			pkgWithTypesAndAst: pkgsWithTypesAndAst[pkgId],
		})
	})

	exitIfMissingBodies()

	for pkgId, res := range allResults {
		hash := packageHashes[pkgId]
		if !cacheHits[hash] {
			if err := cachePut(cache, hash, res); err != nil {
				log.Fatal(err)
			}
		}
	}

	writer := newOutputWriter()
	for _, res := range allResults {
		if err := writer.merge(res.TranslatedFiles); err != nil {
			log.Fatal(err)
		}
	}

	writeGoModFile(modDir, modFile, writer)

	if err := writer.writeFiles(rootOutputDir); err != nil {
		log.Fatal(err)
	}
	if err := writer.maybeDeleteGeneratedFiles(rootOutputDir); err != nil {
		log.Fatal(err)
	}

	var out []string
	for _, pkg := range listedPkgs {
		kind, _ := classifyPackage(pkg)
		if kind != PackageKindBase {
			continue
		}
		if fromGosim := slices.Contains(TranslatedRuntimePackages, pkg.PkgPath); fromGosim {
			continue
		}
		out = append(out, replacedPkgs[pkg.PkgPath])
	}

	deps := make(map[string]map[string]time.Time)
	modTimeCache := make(map[string]time.Time)
	for _, pkg := range listedPkgs {
		kind, path := classifyPackage(pkg)
		if kind != PackageKindTestBinary {
			continue
		}

		// TODO: also add deps from the mandatory linked packages?
		files := findAllDepFiles(pkg, modDir)
		times, err := loadModTimes(files, modTimeCache)
		if err != nil {
			log.Fatal(err)
		}
		deps[replacedPkgs[path]] = times
	}

	return &gosimtool.TranslateOutput{
		RootOutputDir: rootOutputDir,
		Packages:      out,
		Deps:          deps,
	}, nil
}

func findAllDepFiles(pkg *packages.Package, root string) []string {
	seen := make(map[*packages.Package]struct{})
	var walk func(*packages.Package)
	var files []string
	walk = func(pkg *packages.Package) {
		if _, ok := seen[pkg]; ok {
			return
		}
		seen[pkg] = struct{}{}
		for _, dep := range pkg.Imports {
			walk(dep)
		}
		for _, file := range pkg.GoFiles {
			if strings.HasPrefix(file, root) {
				files = append(files, file)
			}
		}
	}
	walk(pkg)
	slices.Sort(files)
	return files
}

func loadModTimes(files []string, cache map[string]time.Time) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	for _, file := range files {
		if t, ok := cache[file]; ok {
			result[file] = t
			continue
		}

		info, err := os.Stat(file)
		if err != nil {
			return nil, err
		}
		t := info.ModTime()
		cache[file] = t
		result[file] = t
	}
	return result, nil
}

// Go version?
// - GOTOOLCHAIN aware?

// Test perf?
// - only run once for all testdata?

// Cmd
// - nicer flags for gosim test

// TODO: hash more singletons?
// - env vars? anything influencing go packages?
// - use go packages export file hash?

// next:
// - verify determinism
// - deal with logging output

// log progress?
// log.Println(results.pkgID, hex.EncodeToString(results.inputHash[:]))
