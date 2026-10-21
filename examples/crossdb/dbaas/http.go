package dbaas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Faults of the HTTP server, the real environment's fault injection.
type Faults struct {
	// DropResponse is the probability that a mutating request is applied
	// and its connection then closed without a response.
	DropResponse float64
	// ThrottleRPS limits requests per second (0: no limit); over it, 429.
	ThrottleRPS float64
	Seed        uint64
}

// Server serves a Cloud over HTTP/JSON.
type Server struct {
	Cloud  *Cloud
	Faults Faults

	mu     sync.Mutex
	rnd    *rand.Rand
	tokens float64
	last   time.Time
}

// Handler returns the server's routes:
//
//	POST   /databases               create
//	GET    /databases/{id}           get
//	GET    /databases?clientToken=t  find by token
//	PUT    /databases/{id}/password  set password
//	DELETE /databases/{id}           delete
//	POST   /connect                  check credentials (for audits)
func (s *Server) Handler() http.Handler {
	s.rnd = rand.New(rand.NewPCG(s.Faults.Seed, 7))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /databases", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if !s.admit(w) || !decode(w, r, &in) {
			return
		}
		d, err := s.Cloud.Create(r.Context(), in)
		s.reply(w, d, err, true)
	})
	mux.HandleFunc("GET /databases/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.admit(w) {
			return
		}
		d, err := s.Cloud.Get(r.Context(), r.PathValue("id"))
		s.reply(w, d, err, false)
	})
	mux.HandleFunc("GET /databases", func(w http.ResponseWriter, r *http.Request) {
		if !s.admit(w) {
			return
		}
		d, err := s.Cloud.FindByToken(r.Context(), r.URL.Query().Get("clientToken"))
		s.reply(w, d, err, false)
	})
	mux.HandleFunc("PUT /databases/{id}/password", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Password string }
		if !s.admit(w) || !decode(w, r, &in) {
			return
		}
		err := s.Cloud.SetPassword(r.Context(), r.PathValue("id"), in.Password)
		s.reply(w, struct{}{}, err, true)
	})
	mux.HandleFunc("DELETE /databases/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.admit(w) {
			return
		}
		err := s.Cloud.Delete(r.Context(), r.PathValue("id"))
		s.reply(w, struct{}{}, err, true)
	})
	mux.HandleFunc("POST /connect", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Endpoint, Username, Password string }
		if !decode(w, r, &in) {
			return
		}
		s.reply(w, map[string]bool{"ok": s.Cloud.Connect(in.Endpoint, in.Username, in.Password)}, nil, false)
	})
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, s.Cloud.List(), nil, false)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// admit applies the throttle: a token bucket of ThrottleRPS, burst 1s.
func (s *Server) admit(w http.ResponseWriter) bool {
	if s.Faults.ThrottleRPS <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.tokens = min(s.Faults.ThrottleRPS, s.tokens+now.Sub(s.last).Seconds()*s.Faults.ThrottleRPS)
	s.last = now
	if s.tokens < 1 {
		http.Error(w, ErrThrottled.Error(), http.StatusTooManyRequests)
		return false
	}
	s.tokens--
	return true
}

func (s *Server) reply(w http.ResponseWriter, v any, err error, mutating bool) {
	if mutating && s.Faults.DropResponse > 0 {
		s.mu.Lock()
		drop := s.rnd.Float64() < s.Faults.DropResponse
		s.mu.Unlock()
		if drop {
			// applied; the client never hears back
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
		}
	}
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Client is the API over HTTP.
type Client struct {
	URL  string
	HTTP *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, &body)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("dbaas: %s %s: %w (outcome unknown)", method, path, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrThrottled
	}
	return fmt.Errorf("dbaas: %s %s: %s", method, path, resp.Status)
}

// Create implements API.
func (c *Client) Create(ctx context.Context, in CreateInput) (Database, error) {
	var d Database
	return d, c.do(ctx, http.MethodPost, "/databases", in, &d)
}

// Get implements API.
func (c *Client) Get(ctx context.Context, id string) (Database, error) {
	var d Database
	return d, c.do(ctx, http.MethodGet, "/databases/"+id, nil, &d)
}

// FindByToken implements API.
func (c *Client) FindByToken(ctx context.Context, token string) (Database, error) {
	var d Database
	return d, c.do(ctx, http.MethodGet, "/databases?clientToken="+token, nil, &d)
}

// SetPassword implements API.
func (c *Client) SetPassword(ctx context.Context, id, password string) error {
	return c.do(ctx, http.MethodPut, "/databases/"+id+"/password", map[string]string{"Password": password}, nil)
}

// Delete implements API.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/databases/"+id, nil, nil)
}

// Connect asks the cloud whether the credentials work.
func (c *Client) Connect(ctx context.Context, endpoint, username, password string) (bool, error) {
	var out map[string]bool
	err := c.do(ctx, http.MethodPost, "/connect", map[string]string{"Endpoint": endpoint, "Username": username, "Password": password}, &out)
	return out["ok"], err
}

// Audit lists every database (for the final audit).
func (c *Client) Audit(ctx context.Context) ([]Database, error) {
	var out []Database
	return out, c.do(ctx, http.MethodGet, "/audit", nil, &out)
}
