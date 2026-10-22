package translate

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"log"
	"maps"
	"os"
	"slices"

	"golang.org/x/tools/go/packages"

	"github.com/jellevandenhooff/gosim/internal/gosimtool"
	"github.com/jellevandenhooff/gosim/internal/translate/cache"
)

type Hash [32]byte

func hashfile(path string) (Hash, error) {
	// XXX: cache?
	bytes, err := os.ReadFile(path)
	if err != nil {
		return Hash{}, err
	}
	return sha256.Sum256(bytes), nil
}

type Hasher struct {
	buffer *bytes.Buffer
}

func NewHasher() *Hasher {
	return &Hasher{
		buffer: bytes.NewBuffer(nil),
	}
}

func (h *Hasher) addHash(hash Hash) {
	h.buffer.Write(hash[:])
}

func (h *Hasher) addString(s string) {
	hash := sha256.Sum256([]byte(s))
	h.addHash(hash)
}

func (h *Hasher) addFileContents(path string) error {
	hash, err := hashfile(path)
	if err != nil {
		return err
	}
	h.addHash(hash)
	return nil
}

func (h *Hasher) addFile(path string) error {
	h.addString(path)
	return h.addFileContents(path)
}

func (h *Hasher) Sum256() Hash {
	return sha256.Sum256(h.buffer.Bytes())
}

func computeTranslateToolHash(cfg gosimtool.BuildConfig) Hash {
	h := NewHasher()
	h.addString("runner")
	h.addString(cfg.AsDirname())
	h.addString(weaveHash) // the aspects woven in (weave.go)
	binaryPath, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	// log.Println(binaryPath)
	// binaryPath = "./foobar"
	// do not hash path because it might change. also it does not matter.
	if err := h.addFileContents(binaryPath); err != nil {
		log.Fatal(err)
	}
	return h.Sum256()
}

func computePackageHash(translateToolHash Hash, pkg *packages.Package, importHashes map[string]Hash) Hash {
	cacheKeyHasher := NewHasher()
	cacheKeyHasher.addHash(translateToolHash)
	cacheKeyHasher.addString(pkg.ID)
	for _, path := range pkg.GoFiles {
		// XXX: why not compiled go files?
		if err := cacheKeyHasher.addFile(path); err != nil {
			log.Fatal(err)
		}
	}
	for _, path := range pkg.OtherFiles {
		if err := cacheKeyHasher.addFile(path); err != nil {
			log.Fatal(err)
		}
	}
	keys := slices.Sorted(maps.Keys(importHashes))
	for _, key := range keys {
		if key == pkg.ID {
			continue
		}
		cacheKeyHasher.addString(key)
		cacheKeyHasher.addHash(importHashes[key])
	}
	return cacheKeyHasher.Sum256()
}

func marshalCachedResult(res *TranslatePackageResult) ([]byte, error) {
	var buf bytes.Buffer
	cacheWriter := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(cacheWriter).Encode(res); err != nil {
		log.Fatal(err)
	}
	if err := cacheWriter.Close(); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes(), nil
}

func cachePut(c *cache.Cache, h Hash, res *TranslatePackageResult) error {
	b, err := marshalCachedResult(res)
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Put(hex.EncodeToString(h[:]), b); err != nil {
		log.Fatal(err)
	}
	return nil
}

func unmarshalCachedResult(b []byte) (*TranslatePackageResult, error) {
	reader, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		log.Fatal(err)
	}
	var result TranslatePackageResult
	if err := gob.NewDecoder(reader).Decode(&result); err != nil {
		log.Fatal(err)
	}
	return &result, nil
}

func cacheGet(c *cache.Cache, h Hash) (*TranslatePackageResult, error) {
	blob, err := c.Get(hex.EncodeToString(h[:]))
	if err != nil && err != cache.ErrNoSuchKey {
		log.Fatal(err)
	}

	if blob == nil {
		return nil, nil
	}

	res, err := unmarshalCachedResult(blob)
	if err != nil {
		log.Fatal(err)
	}

	return res, nil
}
