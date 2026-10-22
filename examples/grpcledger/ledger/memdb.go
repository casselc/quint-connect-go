package ledger

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/casselc/quint-connect-go/qenv"
)

// MemDB is an in-memory qenv.DB for running the server for real: one
// transaction at a time (serializable by construction), its commit hooks
// run under the lock, so steps are recorded in commit order. A production
// deployment would put an adapter over database/sql here.
type MemDB struct {
	mu   sync.Mutex
	rows map[string]any
}

// NewMemDB returns an empty store.
func NewMemDB() *MemDB { return &MemDB{rows: map[string]any{}} }

// Load writes initial data.
func (m *MemDB) Load(key string, val any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[key] = val
}

// Begin implements qenv.DB: it holds the store until the transaction ends.
func (m *MemDB) Begin(context.Context) qenv.Txn {
	m.mu.Lock()
	return &memTx{db: m, writes: map[string]*any{}}
}

type memTx struct {
	db     *MemDB
	writes map[string]*any // nil value: deleted
	hooks  []func()
	done   bool
}

func (t *memTx) Get(key string) (any, bool) {
	if w, ok := t.writes[key]; ok {
		if w == nil {
			return nil, false
		}
		return *w, true
	}
	v, ok := t.db.rows[key]
	return v, ok
}

func (t *memTx) GetForUpdate(key string) (any, bool, error) {
	v, ok := t.Get(key)
	return v, ok, nil
}

func (t *memTx) Put(key string, val any) { t.writes[key] = &val }
func (t *memTx) Delete(key string)       { t.writes[key] = nil }

func (t *memTx) Scan(prefix string) []qenv.KV {
	keys := map[string]bool{}
	for k := range t.db.rows {
		keys[k] = true
	}
	for k := range t.writes {
		keys[k] = true
	}
	var out []qenv.KV
	for k := range keys {
		if v, ok := t.Get(k); ok && strings.HasPrefix(k, prefix) {
			out = append(out, qenv.KV{Key: k, Val: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (t *memTx) Commit() error {
	if t.done {
		return qenv.ErrConflict
	}
	for k, w := range t.writes {
		if w == nil {
			delete(t.db.rows, k)
		} else {
			t.db.rows[k] = *w
		}
	}
	for _, h := range t.hooks {
		h()
	}
	t.end()
	return nil
}

func (t *memTx) Rollback() {
	if !t.done {
		t.end()
	}
}

func (t *memTx) end() {
	t.done = true
	t.db.mu.Unlock()
}

func (t *memTx) OnCommit(fn func()) { t.hooks = append(t.hooks, fn) }
