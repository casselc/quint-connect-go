package binding

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The directive table qweave's tests run too: what lint reads from a
// //quint:action is what weaving makes of it. (Lint once split
// b.Balance(ctx, acct) into two arguments.)
func TestDirectiveArgs(t *testing.T) {
	b, err := os.ReadFile("testdata/directives.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		In  string      `json:"in"`
		Out [][2]string `json:"out"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := directiveArgs(c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("%q: got %q, want %q", c.In, got, c.Out)
		}
	}
}
