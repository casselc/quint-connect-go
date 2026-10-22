package translate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/jellevandenhooff/gosim/internal/gosimtool"
)

// weaveOverlay holds replacement sources for the packages being
// translated, by file path, and every package load reads them instead of
// the files on disk. They come from GOSIM_OVERLAY, a file in the go
// command's overlay format (go build -overlay): {"Replace": {path:
// replacement path}}. A weaver (quint-connect-go's qweave) writes it, so
// that annotated code is woven in a simulation as in an ordinary build.
// weaveHash identifies the overlay's contents, for the translation cache.
var (
	weaveOverlay map[string][]byte
	weaveHash    string
)

// loadOverlay reads GOSIM_OVERLAY, if set.
func loadOverlay(gosimtool.BuildConfig) {
	path := os.Getenv("GOSIM_OVERLAY")
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("GOSIM_OVERLAY: %v", err)
	}
	var o struct{ Replace map[string]string }
	if err := json.Unmarshal(b, &o); err != nil {
		log.Fatalf("GOSIM_OVERLAY %s: %v", path, err)
	}
	overlay := map[string][]byte{}
	h := sha256.New()
	names := make([]string, 0, len(o.Replace))
	for n := range o.Replace {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		src, err := os.ReadFile(o.Replace[n])
		if err != nil {
			log.Fatalf("GOSIM_OVERLAY %s: %v", path, err)
		}
		overlay[n] = src
		fmt.Fprintf(h, "%s\x00%x\x00", n, sha256.Sum256(src))
	}
	weaveOverlay = overlay
	weaveHash = fmt.Sprintf("%x", h.Sum(nil))
}
