package binding

import "testing"

func TestJSONCollections(t *testing.T) {
	for _, c := range []struct{ in, kind, want string }{
		{`"[3,1]"`, "Set", "Set(3, 1)"},
		{`"[[0,5],[1,7]]"`, "List", "[(0, 5), (1, 7)]"},
		{`"[]"`, "List", "[]"},
		{`"{\"n2\":4,\"n1\":3}"`, "Map", `Map("n1" -> 3, "n2" -> 4)`},
		{`"[\"a\",true]"`, "Set", `Set("a", true)`},
	} {
		got, err := jsonQuint(qlit(c.in), c.kind)
		if err != nil || got != c.want {
			t.Errorf("%s as %s: %q %v, want %q", c.in, c.kind, got, err, c.want)
		}
	}
	if _, err := jsonQuint(qlit(`"nope"`), "Set"); err == nil {
		t.Error("non-JSON accepted")
	}
}
