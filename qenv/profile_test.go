package qenv_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

func slowBackend(ctx context.Context) { qenv.Sleep(ctx, 900*time.Millisecond) }
func fastCache(ctx context.Context)   { qenv.Sleep(ctx, 100*time.Millisecond) }

// The profile weighs each wait by the virtual time it took, by stack:
// pprof reads it, and the slow call dominates.
func TestProfileWeighsVirtualTime(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	s.Profile = qenv.NewProfile()
	for i := 0; i < 3; i++ {
		s.Spawn("client", func(ctx context.Context) {
			fastCache(ctx)
			slowBackend(ctx)
		})
	}
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := s.Profile.Total(); got != 3*time.Second {
		t.Fatalf("total %v; want 3s of waits", got)
	}
	path := filepath.Join(t.TempDir(), "virtual.pb.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Profile.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command("go", "tool", "pprof", "-top", "-cum", path).CombinedOutput()
	if err != nil {
		t.Fatalf("pprof: %v\n%s", err, out)
	}
	text := string(out)
	slow, fast := strings.Index(text, "slowBackend"), strings.Index(text, "fastCache")
	if slow < 0 || fast < 0 || slow > fast {
		t.Fatalf("want slowBackend above fastCache:\n%s", text)
	}
	t.Logf("%s", text)
}
