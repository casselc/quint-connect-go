package app

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/qrole"
)

// Woven, the annotations count: each operation is two attempts, the first
// refused, and inside the handler one request is served and no operation
// waits to retry.
func TestRolesWoven(t *testing.T) {
	roles := qrole.New()
	ctx := qrole.WithRoles(context.Background(), roles)
	srv := &Server{Seen: func(ctx context.Context) {
		if u, v := roles.Queue("server"), roles.Orbit("server"); u != 1 || v != 0 {
			t.Errorf("inside the handler: u=%d v=%d, want 1 and 0", u, v)
		}
	}}
	c := &Client{Srv: srv}
	for range 3 {
		if err := c.Do(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if ok, failed := roles.Served("server"); ok != 3 || failed != 3 {
		t.Errorf("served %d ok and %d failed, want 3 and 3", ok, failed)
	}
	if ok, failed := roles.Attempts("server"); ok != 3 || failed != 3 {
		t.Errorf("attempts %d ok and %d failed, want 3 and 3", ok, failed)
	}
	if u, v := roles.Queue("server"), roles.Orbit("server"); u != 0 || v != 0 {
		t.Errorf("after: u=%d v=%d", u, v)
	}
}
