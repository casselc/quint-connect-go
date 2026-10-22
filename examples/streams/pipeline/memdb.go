package pipeline

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/casselc/quint-connect-go/qenv"
)

// MemDB is the projection's store outside a simulation: an in-memory
// qenv.DB whose transactions run one at a time (serializable by
// construction), for the real brokers' tiers. In production an adapter
// over database/sql takes its place.
type MemDB struct {
	mu   sync.Mutex
	rows map[string]any
}

// NewMemDB returns an empty store.
func NewMemDB() *MemDB { return &MemDB{rows: map[string]any{}} }

// Begin implements qenv.DB; the transaction holds the store until it ends.
func (db *MemDB) Begin(context.Context) qenv.Txn {
	db.mu.Lock()
	return &memTx{db: db, writes: map[string]any{}, deleted: map[string]bool{}}
}

type memTx struct {
	db      *MemDB
	writes  map[string]any
	deleted map[string]bool
	hooks   []func()
	done    bool
}

func (tx *memTx) Get(key string) (any, bool) {
	if tx.deleted[key] {
		return nil, false
	}
	if v, ok := tx.writes[key]; ok {
		return v, true
	}
	v, ok := tx.db.rows[key]
	return v, ok
}

func (tx *memTx) GetForUpdate(key string) (any, bool, error) {
	v, ok := tx.Get(key)
	return v, ok, nil
}

func (tx *memTx) Put(key string, val any) { tx.writes[key] = val; delete(tx.deleted, key) }

func (tx *memTx) Delete(key string) { tx.deleted[key] = true; delete(tx.writes, key) }

func (tx *memTx) Scan(prefix string) []qenv.KV {
	var keys []string
	for k := range tx.db.rows {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	for k := range tx.writes {
		if _, ok := tx.db.rows[k]; !ok && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []qenv.KV
	for _, k := range keys {
		if v, ok := tx.Get(k); ok {
			out = append(out, qenv.KV{Key: k, Val: v})
		}
	}
	return out
}

func (tx *memTx) Commit() error {
	if tx.done {
		return qenv.ErrConflict
	}
	for k, v := range tx.writes {
		tx.db.rows[k] = v
	}
	for k := range tx.deleted {
		delete(tx.db.rows, k)
	}
	for _, h := range tx.hooks {
		h()
	}
	tx.end()
	return nil
}

func (tx *memTx) Rollback() { tx.end() }

func (tx *memTx) end() {
	if !tx.done {
		tx.done = true
		tx.db.mu.Unlock()
	}
}

func (tx *memTx) OnCommit(fn func()) { tx.hooks = append(tx.hooks, fn) }
