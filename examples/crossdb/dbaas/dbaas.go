// Package dbaas is a database-as-a-service cloud API, the external system
// the Crossplane provider manages. One implementation (Cloud) serves both
// as a qenv service in simulation and behind an HTTP server
// (cmd/dbaas-server) in the real environment.
//
// Its contract, like real cloud APIs:
//   - Create returns an ID the cloud generates. With a client token it is
//     idempotent: a second Create with the same token returns the first
//     database (as EC2's ClientToken does). Without one, every Create
//     makes a new database.
//   - Databases provision asynchronously (creating, then available) and
//     are deleted asynchronously (deleting, then gone).
//   - Reads are eventually consistent: a new database is invisible to Get
//     and FindByToken for VisibilityLag after it is created (Create's
//     idempotency is not: it always sees the token).
//   - Passwords are never returned, only their fingerprints.
//
// The cloud records its own steps (dbCreated, dbPassword, dbDeleted) where
// they take effect, under its lock.
package dbaas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Errors of the API.
var (
	ErrNotFound  = errors.New("dbaas: database not found")
	ErrThrottled = errors.New("dbaas: throttled (429); retry later")
)

// Database is the API's view of a database.
type Database struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Owner               string `json:"owner"`
	ClientToken         string `json:"clientToken,omitempty"`
	Engine              string `json:"engine"`
	Status              string `json:"status"` // creating, available, deleting
	Endpoint            string `json:"endpoint"`
	Username            string `json:"username"`
	PasswordFingerprint string `json:"passwordFingerprint"`
}

// CreateInput is a create request.
type CreateInput struct {
	Name        string `json:"name"`
	Owner       string `json:"owner"`                 // the managing resource, as a tag
	ClientToken string `json:"clientToken,omitempty"` // idempotency
	Engine      string `json:"engine"`
	Password    string `json:"password"`
}

// API is the cloud API as clients see it. The simulated and HTTP clients
// implement it.
type API interface {
	Create(ctx context.Context, in CreateInput) (Database, error)
	Get(ctx context.Context, id string) (Database, error)
	FindByToken(ctx context.Context, token string) (Database, error)
	SetPassword(ctx context.Context, id, password string) error
	Delete(ctx context.Context, id string) error
}

// Fingerprint of a password, as the API reports it.
func Fingerprint(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:4])
}

type db struct {
	Database
	password  string
	createdAt time.Time
	readyAt   time.Time
	goneAt    time.Time
}

// Cloud is the service: its state and its behaviour.
type Cloud struct {
	// Now is the cloud's clock (the virtual clock in simulation).
	Now func() time.Time
	// ProvisionTime and DeleteTime are how long creating and deleting take.
	ProvisionTime, DeleteTime time.Duration
	// VisibilityLag is how long a new database is invisible to reads.
	VisibilityLag time.Duration
	// Record records a model step (default: none).
	Record func(ctx context.Context, action string, kv ...any)

	mu   sync.Mutex
	dbs  map[string]*db
	next int
}

// NewCloud returns an empty cloud with the real clock.
func NewCloud() *Cloud {
	return &Cloud{Now: time.Now, ProvisionTime: 2 * time.Second, DeleteTime: time.Second, dbs: map[string]*db{}}
}

func (c *Cloud) record(ctx context.Context, action string, kv ...any) {
	if c.Record != nil {
		c.Record(ctx, action, kv...)
	}
}

// refresh advances a database's lifecycle to the current time; it reports
// whether the database still exists.
func (c *Cloud) refresh(d *db) bool {
	now := c.Now()
	if d.Status == "creating" && !now.Before(d.readyAt) {
		d.Status = "available"
	}
	if d.Status == "deleting" && !now.Before(d.goneAt) {
		delete(c.dbs, d.ID)
		return false
	}
	return true
}

func (c *Cloud) visible(d *db) bool { return !c.Now().Before(d.createdAt.Add(c.VisibilityLag)) }

// Create implements API.
func (c *Cloud) Create(ctx context.Context, in CreateInput) (Database, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if in.ClientToken != "" {
		for _, id := range c.ids() {
			if d := c.dbs[id]; d.ClientToken == in.ClientToken && c.refresh(d) {
				return d.Database, nil
			}
		}
	}
	c.next++
	id := fmt.Sprintf("db-%04d", c.next)
	now := c.Now()
	d := &db{Database: Database{ID: id, Name: in.Name, Owner: in.Owner, ClientToken: in.ClientToken, Engine: in.Engine,
		Status: "creating", Endpoint: id + ".dbaas.example:5432", Username: "admin", PasswordFingerprint: Fingerprint(in.Password)},
		password: in.Password, createdAt: now, readyAt: now.Add(c.ProvisionTime)}
	c.dbs[id] = d
	c.record(ctx, "dbCreated", "id", id, "owner", in.Owner, "endpoint", d.Endpoint, "password", d.PasswordFingerprint)
	return d.Database, nil
}

func (c *Cloud) ids() []string {
	ids := make([]string, 0, len(c.dbs))
	for id := range c.dbs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Get implements API.
func (c *Cloud) Get(_ context.Context, id string) (Database, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.dbs[id]
	if !ok || !c.refresh(d) || !c.visible(d) {
		return Database{}, ErrNotFound
	}
	return d.Database, nil
}

// FindByToken implements API: the database created with a client token.
func (c *Cloud) FindByToken(_ context.Context, token string) (Database, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.ids() {
		if d := c.dbs[id]; d.ClientToken == token && c.refresh(d) && c.visible(d) {
			return d.Database, nil
		}
	}
	return Database{}, ErrNotFound
}

// SetPassword implements API.
func (c *Cloud) SetPassword(ctx context.Context, id, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.dbs[id]
	if !ok || !c.refresh(d) || d.Status == "deleting" {
		return ErrNotFound
	}
	d.password, d.PasswordFingerprint = password, Fingerprint(password)
	c.record(ctx, "dbPassword", "id", id, "password", d.PasswordFingerprint)
	return nil
}

// Delete implements API. Deleting a deleting database is a no-op.
func (c *Cloud) Delete(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.dbs[id]
	if !ok || !c.refresh(d) {
		return ErrNotFound
	}
	if d.Status != "deleting" {
		d.Status, d.goneAt = "deleting", c.Now().Add(c.DeleteTime)
		c.record(ctx, "dbDeleted", "id", id)
	}
	return nil
}

// Connect reports whether a client could log in to endpoint with these
// credentials: what a working connection secret must allow.
func (c *Cloud) Connect(endpoint, username, password string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.ids() {
		d := c.dbs[id]
		if d.Endpoint == endpoint && c.refresh(d) && d.Status == "available" {
			return d.Username == username && d.password == password
		}
	}
	return false
}

// List returns every database, outside the API's consistency (for audits).
func (c *Cloud) List() []Database {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Database
	for _, id := range c.ids() {
		if d := c.dbs[id]; c.refresh(d) {
			out = append(out, d.Database)
		}
	}
	return out
}
