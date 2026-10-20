package itf

import (
	"encoding"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

// Unmarshaler is implemented by types that decode themselves from an ITF
// value, e.g. a sum type with payloads that does not fit the default rules.
type Unmarshaler interface {
	UnmarshalITF(v Value) error
}

// Marshaler is implemented by types that encode themselves as an ITF value.
type Marshaler interface {
	MarshalITF() (Value, error)
}

var (
	valueType       = reflect.TypeOf(Value{})
	bigIntType      = reflect.TypeOf(big.Int{})
	unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
	marshalerType   = reflect.TypeOf((*Marshaler)(nil)).Elem()
	textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// Decode stores the ITF value v in the Go value pointed to by out, the way
// encoding/json does for JSON. It is the Go counterpart of deserialising a
// quint-connect state or nondet pick with serde. The rules:
//
//	Quint int          any Go integer kind (overflow is an error), *big.Int, big.Int, float
//	Quint bool / str   bool / string (a string also fills an encoding.TextUnmarshaler)
//	List, Set, Tuple   slice or array; a Set is decoded in the canonical order of its elements
//	Map                Go map (keys decoded like any value); a slice of [2]-arrays or 2-field structs
//	Record             struct (fields matched by `itf:"name"` tag, else by name, ignoring
//	                   case and underscores; unknown record fields are ignored, as serde does),
//	                   or map[string]T
//	Variant            a pointer: None -> nil, Some(x) -> &x (Quint's Option);
//	                   a string kind: the tag of a nullary variant (enums: type Phase string);
//	                   a struct with fields tagged `itf:",tag"` and `itf:",value"` (any sum type);
//	                   a struct with the field named like the tag (e.g. Occupied *Player) and
//	                   a Tag field, also allowed.
//	Tuple              a struct whose fields are filled in order (the `itf:",tuple"` tag on any
//	                   field, or a struct of exported fields only and a tuple value).
//	any / Value        the Value itself (into Value), or plain Go values (into any).
//
// A type implementing Unmarshaler decodes itself. Errors name the path of the
// failing value, e.g. `.writer.pushes[2].phase`.
func Decode(v Value, out any) error {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("itf.Decode: need a non-nil pointer, got %T", out)
	}
	return decodeInto(v, rv.Elem(), "")
}

// DecodeAs decodes v into a new T.
func DecodeAs[T any](v Value) (T, error) {
	var t T
	err := Decode(v, &t)
	return t, err
}

type decodeError struct {
	path string
	msg  string
}

func (e *decodeError) Error() string {
	if e.path == "" {
		return "itf: " + e.msg
	}
	return fmt.Sprintf("itf: at %s: %s", e.path, e.msg)
}

func derr(path, f string, a ...any) error { return &decodeError{path, fmt.Sprintf(f, a...)} }

func kindName(k Kind) string {
	return [...]string{"invalid", "int", "bool", "str", "list", "set", "map", "tuple", "record", "variant"}[k]
}

func decodeInto(v Value, rv reflect.Value, path string) error {
	t := rv.Type()
	if rv.CanAddr() && reflect.PointerTo(t).Implements(unmarshalerType) {
		return rv.Addr().Interface().(Unmarshaler).UnmarshalITF(v)
	}
	if t == valueType {
		rv.Set(reflect.ValueOf(v))
		return nil
	}
	if t == bigIntType {
		if v.Kind != Int {
			return derr(path, "want int, have %s %s", kindName(v.Kind), v)
		}
		rv.Set(reflect.ValueOf(*new(big.Int).Set(v.I)))
		return nil
	}
	switch t.Kind() {
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return derr(path, "cannot decode into non-empty interface %s", t)
		}
		rv.Set(reflect.ValueOf(v.Go()))
		return nil
	case reflect.Pointer:
		// Quint's Option: None -> nil, Some(x) -> &x. Anything else decodes into a new value.
		if v.Kind == Variant && (v.Tag == "None" || v.Tag == "Some") {
			if v.Tag == "None" {
				rv.Set(reflect.Zero(t))
				return nil
			}
			p := reflect.New(t.Elem())
			if err := decodeInto(v.inner(), p.Elem(), path); err != nil {
				return err
			}
			rv.Set(p)
			return nil
		}
		p := reflect.New(t.Elem())
		if err := decodeInto(v, p.Elem(), path); err != nil {
			return err
		}
		rv.Set(p)
		return nil
	case reflect.Bool:
		if v.Kind != Bool {
			return derr(path, "want bool, have %s %s", kindName(v.Kind), v)
		}
		rv.SetBool(v.B)
		return nil
	case reflect.String:
		switch v.Kind {
		case Str:
			rv.SetString(v.S)
			return nil
		case Variant:
			if !v.nullary() {
				return derr(path, "variant %s carries a value; decode it into a struct or an Unmarshaler, not %s", v, t)
			}
			rv.SetString(v.Tag)
			return nil
		}
		return derr(path, "want str or enum variant, have %s %s", kindName(v.Kind), v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Kind != Int {
			return derr(path, "want int, have %s %s", kindName(v.Kind), v)
		}
		if !v.I.IsInt64() || rv.OverflowInt(v.I.Int64()) {
			return derr(path, "%s overflows %s", v.I, t)
		}
		rv.SetInt(v.I.Int64())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if v.Kind != Int {
			return derr(path, "want int, have %s %s", kindName(v.Kind), v)
		}
		if v.I.Sign() < 0 || !v.I.IsUint64() || rv.OverflowUint(v.I.Uint64()) {
			return derr(path, "%s overflows %s", v.I, t)
		}
		rv.SetUint(v.I.Uint64())
		return nil
	case reflect.Float32, reflect.Float64:
		if v.Kind != Int {
			return derr(path, "want int, have %s %s", kindName(v.Kind), v)
		}
		f, _ := new(big.Float).SetInt(v.I).Float64()
		rv.SetFloat(f)
		return nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && v.Kind == Str {
			rv.SetBytes([]byte(v.S))
			return nil
		}
		if v.Kind == Map {
			return decodePairs(v, rv, path)
		}
		elems, err := seqElems(v, path)
		if err != nil {
			return err
		}
		s := reflect.MakeSlice(t, len(elems), len(elems))
		for i, e := range elems {
			if err := decodeInto(e, s.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		rv.Set(s)
		return nil
	case reflect.Array:
		elems, err := seqElems(v, path)
		if err != nil {
			return err
		}
		if len(elems) != t.Len() {
			return derr(path, "want %d elements for %s, have %d", t.Len(), t, len(elems))
		}
		for i, e := range elems {
			if err := decodeInto(e, rv.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		m := reflect.MakeMap(t)
		switch v.Kind {
		case Map:
			for i, p := range v.Pairs {
				k := reflect.New(t.Key()).Elem()
				if err := decodeInto(p[0], k, fmt.Sprintf("%s{key %d}", path, i)); err != nil {
					return err
				}
				x := reflect.New(t.Elem()).Elem()
				if err := decodeInto(p[1], x, fmt.Sprintf("%s[%s]", path, p[0])); err != nil {
					return err
				}
				m.SetMapIndex(k, x)
			}
		case Record:
			if t.Key().Kind() != reflect.String {
				return derr(path, "a record decodes into a map with string keys, not %s", t)
			}
			for name, f := range v.Fields {
				x := reflect.New(t.Elem()).Elem()
				if err := decodeInto(f, x, path+"."+name); err != nil {
					return err
				}
				m.SetMapIndex(reflect.ValueOf(name).Convert(t.Key()), x)
			}
		case Set:
			// Set[T] into map[T]bool or map[T]struct{}: membership.
			ek := t.Elem().Kind()
			if ek != reflect.Bool && !(ek == reflect.Struct && t.Elem().NumField() == 0) {
				return derr(path, "a set decodes into map[T]bool or map[T]struct{}, not %s", t)
			}
			for i, e := range v.Elems {
				k := reflect.New(t.Key()).Elem()
				if err := decodeInto(e, k, fmt.Sprintf("%s{%d}", path, i)); err != nil {
					return err
				}
				x := reflect.New(t.Elem()).Elem()
				if ek == reflect.Bool {
					x.SetBool(true)
				}
				m.SetMapIndex(k, x)
			}
		default:
			return derr(path, "want map, record or set for %s, have %s %s", t, kindName(v.Kind), v)
		}
		rv.Set(m)
		return nil
	case reflect.Struct:
		return decodeStruct(v, rv, path)
	}
	if rv.CanAddr() && reflect.PointerTo(t).Implements(textUnmarshaler) && v.Kind == Str {
		return rv.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(v.S))
	}
	return derr(path, "cannot decode %s into %s", kindName(v.Kind), t)
}

func seqElems(v Value, path string) ([]Value, error) {
	switch v.Kind {
	case List, Tuple:
		return v.Elems, nil
	case Set:
		return v.sortedElems(), nil
	}
	return nil, derr(path, "want list, set or tuple, have %s %s", kindName(v.Kind), v)
}

// decodePairs decodes a Quint map into a slice of pairs (sorted by key).
func decodePairs(v Value, rv reflect.Value, path string) error {
	t := rv.Type()
	pairs := append([][2]Value(nil), v.Pairs...)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0].String() < pairs[j][0].String() })
	s := reflect.MakeSlice(t, len(pairs), len(pairs))
	for i, p := range pairs {
		tup := Value{Kind: Tuple, Elems: []Value{p[0], p[1]}}
		if err := decodeInto(tup, s.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	rv.Set(s)
	return nil
}

type fieldInfo struct {
	index       int
	name        string
	tag, value  bool // `itf:",tag"`, `itf:",value"`
	tuple, skip bool
}

func structFields(t reflect.Type) []fieldInfo {
	var out []fieldInfo
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fi := fieldInfo{index: i, name: f.Name}
		tag, ok := f.Tag.Lookup("itf")
		if !ok {
			if jt, ok := f.Tag.Lookup("json"); ok {
				tag = strings.Split(jt, ",")[0]
			}
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			fi.skip = true
		} else if name != "" {
			fi.name = name
		}
		for _, o := range strings.Split(opts, ",") {
			switch o {
			case "tag":
				fi.tag = true
			case "value":
				fi.value = true
			case "tuple":
				fi.tuple = true
			}
		}
		out = append(out, fi)
	}
	return out
}

func norm(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

func decodeStruct(v Value, rv reflect.Value, path string) error {
	t := rv.Type()
	fields := structFields(t)
	isTuple := false
	for _, f := range fields {
		if f.tuple {
			isTuple = true
		}
	}
	switch {
	case v.Kind == Tuple || (isTuple && v.Kind == List):
		var live []fieldInfo
		for _, f := range fields {
			if !f.skip {
				live = append(live, f)
			}
		}
		if len(live) != len(v.Elems) {
			return derr(path, "tuple of %d elements into %s with %d fields", len(v.Elems), t, len(live))
		}
		for i, f := range live {
			if err := decodeInto(v.Elems[i], rv.Field(f.index), fmt.Sprintf("%s._%d", path, i+1)); err != nil {
				return err
			}
		}
		return nil
	case v.Kind == Record:
		byName := map[string]string{}
		for name := range v.Fields {
			byName[norm(name)] = name
		}
		for _, f := range fields {
			if f.skip || f.tag || f.value {
				continue
			}
			name, ok := v.Fields[f.name]
			_ = name
			key := f.name
			if !ok {
				if k, ok2 := byName[norm(f.name)]; ok2 {
					key, ok = k, true
				}
			}
			if !ok {
				continue // absent: leave the zero value, as serde does with defaults
			}
			if err := decodeInto(v.Fields[key], rv.Field(f.index), path+"."+key); err != nil {
				return err
			}
		}
		return nil
	case v.Kind == Variant:
		// 1. explicit `itf:",tag"` / `itf:",value"` fields
		var tagF, valF *fieldInfo
		for i := range fields {
			if fields[i].tag {
				tagF = &fields[i]
			}
			if fields[i].value {
				valF = &fields[i]
			}
		}
		if tagF == nil {
			for i := range fields {
				if fields[i].name == "Tag" || fields[i].name == "tag" {
					tagF = &fields[i]
				}
			}
		}
		if tagF != nil {
			if err := decodeInto(Value{Kind: Str, S: v.Tag}, rv.Field(tagF.index), path+".tag"); err != nil {
				return err
			}
		}
		if valF != nil {
			if v.nullary() {
				return nil // no payload: the value field keeps its zero value
			}
			return decodeInto(v.inner(), rv.Field(valF.index), path+"."+v.Tag)
		}
		// 2. a field named like the variant holds its payload
		for _, f := range fields {
			if f.skip || f.tag {
				continue
			}
			if f.name == v.Tag || norm(f.name) == norm(v.Tag) {
				fv := rv.Field(f.index)
				if v.nullary() && fv.Kind() == reflect.Bool {
					fv.SetBool(true)
					return nil
				}
				return decodeInto(v.inner(), fv, path+"."+v.Tag)
			}
		}
		if tagF != nil && v.nullary() {
			return nil
		}
		return derr(path, "variant %s has no field in %s (give it a field named %s, or fields tagged `itf:\",tag\"` and `itf:\",value\"`)", v.Tag, t, v.Tag)
	}
	return derr(path, "want record, tuple or variant for %s, have %s %s", t, kindName(v.Kind), v)
}

func (v Value) inner() Value {
	if v.Inner == nil {
		return Value{Kind: Tuple}
	}
	return *v.Inner
}

func (v Value) nullary() bool {
	return v.Inner == nil || (v.Inner.Kind == Tuple && len(v.Inner.Elems) == 0)
}

func (v Value) sortedElems() []Value {
	es := append([]Value(nil), v.Elems...)
	sort.Slice(es, func(i, j int) bool { return es[i].String() < es[j].String() })
	return es
}

// Go converts the value to plain Go values: int64 (or *big.Int when it does
// not fit), bool, string, []any (list, set, tuple), map[string]any (record),
// [][2]any (map), and map[string]any{"tag": ..., "value": ...} (variant).
func (v Value) Go() any {
	switch v.Kind {
	case Int:
		if v.I.IsInt64() {
			return v.I.Int64()
		}
		return new(big.Int).Set(v.I)
	case Bool:
		return v.B
	case Str:
		return v.S
	case List, Tuple, Set:
		es := v.Elems
		if v.Kind == Set {
			es = v.sortedElems()
		}
		out := make([]any, len(es))
		for i, e := range es {
			out[i] = e.Go()
		}
		return out
	case Map:
		out := make([][2]any, len(v.Pairs))
		for i, p := range v.Pairs {
			out[i] = [2]any{p[0].Go(), p[1].Go()}
		}
		return out
	case Record:
		out := map[string]any{}
		for k, f := range v.Fields {
			out[k] = f.Go()
		}
		return out
	case Variant:
		if v.nullary() {
			return map[string]any{"tag": v.Tag}
		}
		return map[string]any{"tag": v.Tag, "value": v.inner().Go()}
	}
	return nil
}

// Equal reports whether two values are the same Quint value (sets and maps
// compare regardless of order).
func (v Value) Equal(w Value) bool { return v.String() == w.String() }

// Encode converts a Go value to an ITF value, the inverse of Decode:
// integers -> int, bool, string, slices/arrays -> List (Set when the struct
// field is tagged `itf:",set"`, or for map[T]bool/map[T]struct{}), maps ->
// Map (Record for map[string]T is not assumed: use a struct), structs ->
// Record (or Variant, for structs with `itf:",tag"` fields), nil pointer ->
// None, non-nil pointer -> Some(x) when the field is tagged `itf:",option"`
// (otherwise the pointed-to value). A string type with an Enum() marker
// method (or a field tagged `itf:",enum"`) becomes a nullary variant.
// A Marshaler encodes itself.
func Encode(x any) (Value, error) {
	if x == nil {
		return Value{}, fmt.Errorf("itf.Encode: nil")
	}
	return encode(reflect.ValueOf(x), "", "")
}

// MustEncode is Encode, panicking on error (for tests and literals).
func MustEncode(x any) Value {
	v, err := Encode(x)
	if err != nil {
		panic(err)
	}
	return v
}

// Enum is a convenience for nullary variants: Enum("Ok") is Quint's Ok.
func Enum(tag string) Value { return Value{Kind: Variant, Tag: tag, Inner: &Value{Kind: Tuple}} }

// VariantOf builds the variant Tag(v).
func VariantOf(tag string, v Value) Value { return Value{Kind: Variant, Tag: tag, Inner: &v} }

// None is Quint's None.
func None() Value { return Enum("None") }

// IntOf, StrOf, BoolOf build scalars.
func IntOf(n int64) Value       { return Value{Kind: Int, I: big.NewInt(n)} }
func StrOf(s string) Value      { return Value{Kind: Str, S: s} }
func BoolOf(b bool) Value       { return Value{Kind: Bool, B: b} }
func SetOf(es ...Value) Value   { return Value{Kind: Set, Elems: es} }
func ListOf(es ...Value) Value  { return Value{Kind: List, Elems: es} }
func TupleOf(es ...Value) Value { return Value{Kind: Tuple, Elems: es} }

// RecordOf builds a record from alternating names and values.
func RecordOf(kv ...any) Value {
	r := Value{Kind: Record, Fields: map[string]Value{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Fields[kv[i].(string)] = MustEncode(kv[i+1])
	}
	return r
}

type enumer interface{ Enum() }

var enumerType = reflect.TypeOf((*enumer)(nil)).Elem()

func encode(rv reflect.Value, opts, path string) (Value, error) {
	if !rv.IsValid() {
		return Value{}, derr(path, "invalid value")
	}
	t := rv.Type()
	if t.Implements(marshalerType) && !(rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return rv.Interface().(Marshaler).MarshalITF()
	}
	if t == valueType {
		return rv.Interface().(Value), nil
	}
	if t == bigIntType {
		b := rv.Interface().(big.Int)
		return Value{Kind: Int, I: new(big.Int).Set(&b)}, nil
	}
	has := func(o string) bool {
		for _, x := range strings.Split(opts, ",") {
			if x == o {
				return true
			}
		}
		return false
	}
	switch t.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return None(), nil
		}
		return encode(rv.Elem(), opts, path)
	case reflect.Pointer:
		if rv.IsNil() {
			return None(), nil
		}
		if t.Elem() == bigIntType {
			return Value{Kind: Int, I: new(big.Int).Set(rv.Interface().(*big.Int))}, nil
		}
		inner, err := encode(rv.Elem(), "", path)
		if err != nil {
			return Value{}, err
		}
		if has("option") {
			return Some(inner), nil
		}
		return inner, nil
	case reflect.Bool:
		return BoolOf(rv.Bool()), nil
	case reflect.String:
		if has("enum") || t.Implements(enumerType) {
			return Enum(rv.String()), nil
		}
		return StrOf(rv.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return IntOf(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return Value{Kind: Int, I: new(big.Int).SetUint64(rv.Uint())}, nil
	case reflect.Slice, reflect.Array:
		k := List
		if has("set") {
			k = Set
		} else if has("tuple") {
			k = Tuple
		}
		out := Value{Kind: k, Elems: []Value{}}
		for i := 0; i < rv.Len(); i++ {
			e, err := encode(rv.Index(i), "", fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return Value{}, err
			}
			out.Elems = append(out.Elems, e)
		}
		return out, nil
	case reflect.Map:
		ek := t.Elem().Kind()
		isSet := ek == reflect.Bool || (ek == reflect.Struct && t.Elem().NumField() == 0)
		if isSet && !has("map") {
			out := Value{Kind: Set, Elems: []Value{}}
			for _, k := range rv.MapKeys() {
				if ek == reflect.Bool && !rv.MapIndex(k).Bool() {
					continue
				}
				kv, err := encode(k, "", path)
				if err != nil {
					return Value{}, err
				}
				out.Elems = append(out.Elems, kv)
			}
			return out, nil
		}
		if has("record") && t.Key().Kind() == reflect.String {
			out := Value{Kind: Record, Fields: map[string]Value{}}
			for _, k := range rv.MapKeys() {
				fv, err := encode(rv.MapIndex(k), "", path+"."+k.String())
				if err != nil {
					return Value{}, err
				}
				out.Fields[k.String()] = fv
			}
			return out, nil
		}
		out := Value{Kind: Map}
		for _, k := range rv.MapKeys() {
			kv, err := encode(k, "", path)
			if err != nil {
				return Value{}, err
			}
			vv, err := encode(rv.MapIndex(k), "", fmt.Sprintf("%s[%s]", path, kv))
			if err != nil {
				return Value{}, err
			}
			out.Pairs = append(out.Pairs, [2]Value{kv, vv})
		}
		sort.Slice(out.Pairs, func(i, j int) bool { return out.Pairs[i][0].String() < out.Pairs[j][0].String() })
		return out, nil
	case reflect.Struct:
		fields := structFields(t)
		var tagF, valF *fieldInfo
		tuple := has("tuple")
		for i := range fields {
			switch {
			case fields[i].tag:
				tagF = &fields[i]
			case fields[i].value:
				valF = &fields[i]
			case fields[i].tuple:
				tuple = true
			}
		}
		if tagF != nil {
			tag := fmt.Sprint(rv.Field(tagF.index).Interface())
			if valF == nil {
				return Enum(tag), nil
			}
			if fv := rv.Field(valF.index); (fv.Kind() == reflect.Pointer || fv.Kind() == reflect.Interface) && fv.IsNil() {
				return Enum(tag), nil // no payload
			}
			inner, err := encode(rv.Field(valF.index), "", path+"."+tag)
			if err != nil {
				return Value{}, err
			}
			return VariantOf(tag, inner), nil
		}
		if tuple {
			out := Value{Kind: Tuple}
			for _, f := range fields {
				if f.skip {
					continue
				}
				e, err := encode(rv.Field(f.index), "", path)
				if err != nil {
					return Value{}, err
				}
				out.Elems = append(out.Elems, e)
			}
			return out, nil
		}
		out := Value{Kind: Record, Fields: map[string]Value{}}
		for _, f := range fields {
			if f.skip {
				continue
			}
			sf := t.Field(f.index)
			fopts := ""
			if tag, ok := sf.Tag.Lookup("itf"); ok {
				_, fopts, _ = strings.Cut(tag, ",")
			}
			name := f.name
			if _, ok := sf.Tag.Lookup("itf"); !ok {
				if _, ok := sf.Tag.Lookup("json"); !ok {
					name = lowerFirst(name)
				}
			}
			fv, err := encode(rv.Field(f.index), fopts, path+"."+name)
			if err != nil {
				return Value{}, err
			}
			out.Fields[name] = fv
		}
		return out, nil
	}
	return Value{}, derr(path, "cannot encode %s", t)
}

// lowerFirst maps Go's exported field names to Quint's camelCase:
// NextTurn -> nextTurn, ID -> id.
func lowerFirst(s string) string {
	rs := []rune(s)
	i := 0
	for i < len(rs) && unicode.IsUpper(rs[i]) {
		i++
	}
	switch {
	case i == 0:
		return s
	case i == len(rs):
		return strings.ToLower(s)
	case i > 1:
		i-- // ACMEThing -> acmeThing
	}
	return strings.ToLower(string(rs[:i])) + string(rs[i:])
}
