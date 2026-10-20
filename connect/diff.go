package connect

import (
	"fmt"
	"math/big"
	"reflect"
	"sort"
)

// maxDiff bounds the number of differences reported.
const maxDiff = 20

// Diff lists the paths at which want (the model) and got (the
// implementation) differ, as "path: model X, implementation Y". Equal
// values give nil.
func Diff(want, got any) []string {
	var out []string
	diff(reflect.ValueOf(want), reflect.ValueOf(got), "", &out)
	if len(out) > maxDiff {
		out = append(out[:maxDiff], fmt.Sprintf("... and %d more", len(out)-maxDiff))
	}
	return out
}

func show(v reflect.Value) string {
	if !v.IsValid() {
		return "<absent>"
	}
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return "nil"
	}
	if b, ok := v.Interface().(*big.Int); ok {
		return b.String()
	}
	return fmt.Sprintf("%#v", v.Interface())
}

func diff(a, b reflect.Value, path string, out *[]string) {
	if len(*out) > maxDiff {
		return
	}
	add := func() {
		p := path
		if p == "" {
			p = "(state)"
		}
		*out = append(*out, fmt.Sprintf("%s: model %s, implementation %s", p, show(a), show(b)))
	}
	if !a.IsValid() || !b.IsValid() {
		if a.IsValid() != b.IsValid() {
			add()
		}
		return
	}
	if a.Type() != b.Type() {
		add()
		return
	}
	if a.CanInterface() {
		if x, ok := a.Interface().(big.Int); ok {
			y := b.Interface().(big.Int)
			if x.Cmp(&y) != 0 {
				add()
			}
			return
		}
		if x, ok := a.Interface().(*big.Int); ok {
			y := b.Interface().(*big.Int)
			if (x == nil) != (y == nil) || (x != nil && x.Cmp(y) != 0) {
				add()
			}
			return
		}
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				add()
			}
			return
		}
		diff(a.Elem(), b.Elem(), path, out)
	case reflect.Struct:
		t := a.Type()
		for i := 0; i < t.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			diff(a.Field(i), b.Field(i), path+"."+t.Field(i).Name, out)
		}
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && a.Len() != b.Len() {
			if !reflect.DeepEqual(a.Interface(), b.Interface()) {
				*out = append(*out, fmt.Sprintf("%s: model has %d elements, implementation %d: model %s, implementation %s",
					orState(path), a.Len(), b.Len(), show(a), show(b)))
			}
			return
		}
		for i := 0; i < a.Len(); i++ {
			diff(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		keys := map[string]reflect.Value{}
		for _, k := range a.MapKeys() {
			keys[fmt.Sprint(k.Interface())] = k
		}
		for _, k := range b.MapKeys() {
			keys[fmt.Sprint(k.Interface())] = k
		}
		names := make([]string, 0, len(keys))
		for n := range keys {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			k := keys[n]
			diff(a.MapIndex(k), b.MapIndex(k), fmt.Sprintf("%s[%v]", path, n), out)
		}
	default:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			add()
		}
	}
}

func orState(p string) string {
	if p == "" {
		return "(state)"
	}
	return p
}
