package qweave

import (
	"encoding/json"
	"os"
	"testing"
)

// The directive table binding's lint runs too (../binding/testdata): what
// weaving makes of a //quint:action is what lint reads from it.
func TestSplitArgsTable(t *testing.T) {
	b, err := os.ReadFile("../binding/testdata/directives.json")
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
		got := splitArgs(c.In)
		ok := len(got) == len(c.Out)
		for i := 0; ok && i < len(got); i++ {
			ok = got[i].Key == c.Out[i][0] && got[i].Value == c.Out[i][1]
		}
		if !ok {
			t.Errorf("%q: got %v, want %q", c.In, got, c.Out)
		}
	}
}
