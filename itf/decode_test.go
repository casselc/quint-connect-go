package itf

import (
	"math/big"
	"reflect"
	"strings"
	"testing"
)

type player string

func (player) Enum() {}

type square struct {
	Tag      string  `itf:",tag"`
	Occupied *player `itf:",value"`
}

type game struct {
	Board    map[int]map[int]square
	NextTurn player
	Moves    []int
	Winner   *player
	Big      *big.Int
	Pos      [2]int
	Seen     map[string]bool
}

func TestDecodeQuintState(t *testing.T) {
	src := `{ board: Map(1 -> Map(1 -> Occupied(X), 2 -> Empty)), nextTurn: O, moves: [3, 1], winner: None,
	          big: 123456789012345678901234567890, pos: (1, 2), seen: Set("a", "b"), extra: 7 }`
	v, err := ParseQuint(src)
	if err != nil {
		t.Fatal(err)
	}
	var g game
	if err := Decode(v, &g); err != nil {
		t.Fatal(err)
	}
	x := player("X")
	want := game{
		Board:    map[int]map[int]square{1: {1: {Tag: "Occupied", Occupied: &x}, 2: {Tag: "Empty"}}},
		NextTurn: "O", Moves: []int{3, 1}, Big: g.Big, Pos: [2]int{1, 2}, Seen: map[string]bool{"a": true, "b": true},
	}
	if g.Big == nil || g.Big.String() != "123456789012345678901234567890" {
		t.Fatalf("big: %v", g.Big)
	}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("got %+v\nwant %+v", g, want)
	}
	back, err := Encode(g)
	if err != nil {
		t.Fatal(err)
	}
	// extra is not in the struct; everything else must round-trip
	delete(v.Fields, "extra")
	// moves/pos: a Go slice/array encodes as a list unless tagged
	v.Fields["pos"] = ListOf(IntOf(1), IntOf(2))
	if !back.Equal(v) {
		t.Fatalf("round trip:\n got %s\nwant %s", back, v)
	}
}

func TestDecodeErrorsNamePath(t *testing.T) {
	v, _ := ParseQuint(`{ a: { b: [1, "x"] } }`)
	var out struct{ A struct{ B []int } }
	err := Decode(v, &out)
	if err == nil || !strings.Contains(err.Error(), ".a.b[1]") {
		t.Fatalf("error %v", err)
	}
	var small struct{ A int8 }
	v, _ = ParseQuint(`{ a: 300 }`)
	if err := Decode(v, &small); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("overflow: %v", err)
	}
}

func TestDecodeOptionAndAny(t *testing.T) {
	v, _ := ParseQuint(`Some(5)`)
	var p *int
	if err := Decode(v, &p); err != nil || p == nil || *p != 5 {
		t.Fatalf("%v %v", p, err)
	}
	var a any
	v, _ = ParseQuint(`{ x: Set(2, 1) }`)
	if err := Decode(v, &a); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, map[string]any{"x": []any{int64(1), int64(2)}}) {
		t.Fatalf("%#v", a)
	}
	n, err := DecodeAs[uint8](IntOf(-1))
	if err == nil {
		t.Fatalf("negative into uint8: %v", n)
	}
}

func TestEncodeNames(t *testing.T) {
	type rec struct {
		NextTurn int
		ID       string
		HTTPCode int
		Opt      *int  `itf:"opt,option"`
		Ids      []int `itf:"ids,set"`
	}
	v := MustEncode(rec{NextTurn: 1, ID: "a", HTTPCode: 2, Ids: []int{2, 1}})
	if got := v.String(); got != `{ httpCode: 2, id: "a", ids: Set(1, 2), nextTurn: 1, opt: None }` {
		t.Fatal(got)
	}
}
