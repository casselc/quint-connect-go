package translate

import (
	"log/slog"
	"os"
	"slices"
	"sync"
)

// missingBodies collects every bodyless function without a hook across all
// translated packages, so that porting to a new Go version sees them all in
// one run instead of one per run.
var missingBodies struct {
	sync.Mutex
	list []packageSelector
}

func reportMissingBody(pkg, name string) {
	missingBodies.Lock()
	defer missingBodies.Unlock()
	missingBodies.list = append(missingBodies.list, packageSelector{Pkg: pkg, Selector: name})
}

func exitIfMissingBodies() {
	missingBodies.Lock()
	defer missingBodies.Unlock()
	if len(missingBodies.list) == 0 {
		return
	}
	slices.SortFunc(missingBodies.list, func(a, b packageSelector) int {
		if a.Pkg != b.Pkg {
			if a.Pkg < b.Pkg {
				return -1
			}
			return 1
		}
		if a.Selector < b.Selector {
			return -1
		}
		if a.Selector > b.Selector {
			return 1
		}
		return 0
	})
	for _, m := range slices.Compact(missingBodies.list) {
		slog.Error("missing function body", "pkg", m.Pkg, "name", m.Selector)
	}
	os.Exit(1)
}

// emptyBodyGo126 lists functions whose body is replaced by an empty one.
var emptyBodyGo126 = map[packageSelector]bool{
	{Pkg: "internal/abi", Selector: "EscapeNonString"}: true, // panic("intrinsic")
}
