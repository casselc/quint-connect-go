// Package app marks its load roles with qrole's annotations only.
package app

import (
	"context"
	"errors"
)

// ErrBusy is the server's refusal of a first attempt.
var ErrBusy = errors.New("busy")

// Server refuses every other request.
type Server struct {
	n int
	// Seen is called while a request is served, to sample the roles.
	Seen func(ctx context.Context)
}

// Handle serves one request.
//
//quint:serve name:server
func (s *Server) Handle(ctx context.Context) error {
	s.n++
	s.Seen(ctx)
	if s.n%2 == 1 {
		return ErrBusy
	}
	return nil
}

// Client retries once.
type Client struct{ Srv *Server }

// Do makes one operation of up to two attempts.
//
//quint:operation service:server
func (c *Client) Do(ctx context.Context) (err error) {
	for range 2 {
		if err = c.attempt(ctx); err == nil {
			return nil
		}
	}
	return err
}

//quint:call service:server
func (c *Client) attempt(ctx context.Context) error { return c.Srv.Handle(ctx) }
