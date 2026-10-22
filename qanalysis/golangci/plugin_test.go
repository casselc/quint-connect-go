package golangci

import (
	"testing"

	"github.com/golangci/plugin-module-register/register"
)

// The plugin registers, picks analyzers by name and rejects unknown ones.
func TestPlugin(t *testing.T) {
	newPlugin, err := register.GetPlugin("quintgo")
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPlugin(map[string]any{"analyzers": []string{"quintsim"}})
	if err != nil {
		t.Fatal(err)
	}
	as, err := p.BuildAnalyzers()
	if err != nil || len(as) != 1 || as[0].Name != "quintsim" {
		t.Fatalf("analyzers %v, %v; want quintsim", as, err)
	}
	p, _ = newPlugin(map[string]any{"analyzers": []string{"nope"}})
	if _, err := p.BuildAnalyzers(); err == nil {
		t.Error("an unknown analyzer was accepted")
	}
	if _, err := newPlugin(map[string]any{"bogus": 1}); err == nil {
		t.Error("an unknown setting was accepted")
	}
}
