package gosimruntime

import (
	"iter"
	"reflect"
	"unsafe"

	"github.com/jellevandenhooff/gosim/internal/race"
)

const checkMap = false

// Map implements go's builtin map API with deterministic yet varying iteration
// order.
//
// The Map maintains an ordering of values based on their insertion time.
// Iterators start at a random index and then visit all items.
//
// Map is a value-type that wraps a pointer to an implementation like go's
// builtin map.
type Map[K comparable, V any] struct {
	Impl *mapImpl[K, V]
}

type mapElem[K comparable, V any] struct {
	key     K
	value   V
	present bool
}

// MapImpl names the implementation of Map, for the constraint
// ~struct{ Impl *MapImpl[K, V] } that ~map[K]V translates to.
type MapImpl[K comparable, V any] = mapImpl[K, V]

type mapImpl[K comparable, V any] struct {
	// underlying maps keys to a position in small concatenated with values
	underlying map[K]int32

	// small and values together form the backing array for the map.
	// values might be nil for small maps.
	small  [4]mapElem[K, V]
	values []mapElem[K, V]

	// free holds free indices in the backing array of the map. Only
	// allocated and meaningful when values is non-nil.
	free []int32

	// TODO: use underlying equal etc from map?
	// TODO: use raw bytes like map?
	// TODO: somehow support reflect.MapOf?
}

func NewMap[K comparable, V any]() Map[K, V] {
	return Map[K, V]{
		Impl: &mapImpl[K, V]{},
	}
}

func NilMap[K comparable, V any]() Map[K, V] {
	return Map[K, V]{}
}

// ExtractMap extracts a Map from named Map types.
//
// TODO: Consider instead having many generic package-local funcs for MapRange,
// MapSet, etc.? That would sidestep all the ExtractMap calls.
func ExtractMap[M ~struct{ Impl *mapImpl[K, V] }, K comparable, V any](m M) Map[K, V] {
	return Map[K, V](m)
}

type KV[K comparable, V any] struct {
	K K
	V V
}

// MapLiteral builds a Map from a slice of key-value pairs.
func MapLiteral[K comparable, V any](pairs []KV[K, V]) Map[K, V] {
	m := NewMap[K, V]()
	for _, pair := range pairs {
		m.Set(pair.K, pair.V)
	}
	return m
}

func (m Map[K, V]) Clear() {
	clear(m.Impl.underlying)
	for i := range m.Impl.small {
		m.Impl.small[i] = mapElem[K, V]{}
	}
	for i := range m.Impl.values {
		m.Impl.values[i] = mapElem[K, V]{}
	}
	if m.Impl.free != nil {
		n := len(m.Impl.values) + len(m.Impl.small)
		m.Impl.free = m.Impl.free[:n]
		for i := range n {
			m.Impl.free[i] = int32(i)
		}
	}
}

func (m Map[K, V]) elem(idx int) *mapElem[K, V] {
	if idx < len(m.Impl.small) {
		return &m.Impl.small[idx]
	}
	return &m.Impl.values[idx-len(m.Impl.small)]
}

func (m Map[K, V]) GetOk(k K) (V, bool) {
	if m.Impl == nil {
		var v V
		return v, false
	}

	if m.Impl.underlying == nil {
		for i := range m.Impl.small {
			elem := &m.Impl.small[i]
			if elem.present && elem.key == k {
				return elem.value, true
			}
		}
		var v V
		return v, false
	}

	idx, ok := m.Impl.underlying[k]
	if !ok {
		var v V
		return v, false
	}

	elem := m.elem(int(idx))
	if checkMap {
		if elem.key != k {
			panic("bad elem.key")
		}
	}

	return elem.value, true
}

func (m Map[K, V]) Get(k K) V {
	v, _ := m.GetOk(k)
	return v
}

func (m Map[K, V]) resizeInitial() {
	oldN := len(m.Impl.small)
	newN := oldN * 2

	newValues := make([]mapElem[K, V], newN-len(m.Impl.small))
	newFree := make([]int32, oldN, newN)

	m.Impl.underlying = make(map[K]int32, newN)
	for i := range m.Impl.small {
		elem := &m.Impl.small[i]
		if checkMap {
			if !elem.present {
				panic("bad elem.present")
			}
		}
		m.Impl.underlying[elem.key] = int32(i * 2)
	}

	for i := len(m.Impl.small) / 2; i < len(m.Impl.small); i++ {
		newValues[2*i-len(m.Impl.small)] = m.Impl.small[i]
	}
	for i := len(m.Impl.small)/2 - 1; i >= 0; i-- {
		m.Impl.small[i*2+1] = mapElem[K, V]{}
		m.Impl.small[i*2] = m.Impl.small[i]
	}

	for i := 0; i < oldN; i++ {
		newFree[i] = int32(i*2 + 1)
	}

	m.Impl.values = newValues
	m.Impl.free = newFree
}

func (m Map[K, V]) resizeLater() {
	oldN := len(m.Impl.values) + len(m.Impl.small)
	newN := oldN * 2

	newValues := make([]mapElem[K, V], newN-len(m.Impl.small))
	newFree := make([]int32, oldN, newN)

	for k, idx := range m.Impl.underlying {
		m.Impl.underlying[k] = idx * 2
	}

	for i := len(m.Impl.small) / 2; i < len(m.Impl.small); i++ {
		newValues[2*i-len(m.Impl.small)] = m.Impl.small[i]
	}
	for i := len(m.Impl.small)/2 - 1; i >= 0; i-- {
		m.Impl.small[i*2+1] = mapElem[K, V]{}
		m.Impl.small[i*2] = m.Impl.small[i]
	}
	for i := len(m.Impl.small); i < oldN; i++ {
		newValues[2*i-len(m.Impl.small)] = m.Impl.values[i-len(m.Impl.small)]
	}

	for i := 0; i < oldN; i++ {
		newFree[i] = int32(2*i + 1)
	}

	m.Impl.values = newValues
	m.Impl.free = newFree
}

func (m Map[K, V]) Set(k K, v V) {
	if m.Impl.underlying == nil {
		freeIdx := -1
		for i := range m.Impl.small {
			elem := &m.Impl.small[i]
			if elem.present && elem.key == k {
				elem.value = v
				return
			}
			if !elem.present {
				freeIdx = i
			}
		}

		if freeIdx != -1 {
			m.Impl.small[freeIdx] = mapElem[K, V]{
				present: true,
				key:     k,
				value:   v,
			}
			return
		}

		m.resizeInitial()
	} else {
		idx, ok := m.Impl.underlying[k]
		if ok {
			elem := m.elem(int(idx))
			if checkMap {
				if elem.key != k {
					panic("bad elem.key")
				}
			}
			elem.value = v
			return
		}
	}

	if len(m.Impl.free) == 0 {
		m.resizeLater()
	}

	freeIdx := int(gs.get().choose("map.free", uint32(len(m.Impl.free))))
	idx := m.Impl.free[freeIdx]
	m.Impl.free[freeIdx] = m.Impl.free[len(m.Impl.free)-1]
	m.Impl.free = m.Impl.free[:len(m.Impl.free)-1]

	m.Impl.underlying[k] = idx
	*m.elem(int(idx)) = mapElem[K, V]{key: k, value: v, present: true}
}

func (m Map[K, V]) Len() int {
	if m.Impl == nil {
		return 0
	}

	if race.Enabled {
		// for TestRaceMapLenDelete
		race.Read(unsafe.Pointer(m.Impl))
	}

	if m.Impl.underlying == nil {
		count := 0
		for i := range m.Impl.small {
			elem := &m.Impl.small[i]
			if elem.present {
				count++
			}
		}
		return count
	}

	return len(m.Impl.underlying)
}

func (m Map[K, V]) Delete(k K) {
	if m.Impl == nil {
		// XXX: wtf
		return
	}

	if race.Enabled {
		// for TestRaceMapLenDelete
		race.Write(unsafe.Pointer(m.Impl))
	}

	if m.Impl.underlying == nil {
		for i := range m.Impl.small {
			elem := &m.Impl.small[i]
			if elem.present && elem.key == k {
				elem.present = false
				return
			}
		}
	} else {
		idx, ok := m.Impl.underlying[k]
		if !ok {
			return
		}

		delete(m.Impl.underlying, k)
		*m.elem(int(idx)) = mapElem[K, V]{}
		m.Impl.free = append(m.Impl.free, idx)
	}
}

type MapIter[K comparable, V any] struct {
	impl   *mapImpl[K, V]
	size   int
	start  int
	offset int
}

func (i *MapIter[K, V]) Next() (K, V, bool) {
	if i.impl == nil {
		var k K
		var v V
		return k, v, false
	}

	for i.size != len(i.impl.values)+len(i.impl.small) {
		i.size *= 2
		i.offset *= 2
		i.start *= 2
	}

	for i.offset < i.size {
		idx := i.start + i.offset
		if idx >= i.size {
			idx -= i.size
		}
		elem := i.impl.outer().elem(idx)
		i.offset++

		if elem.present {
			return elem.key, elem.value, true
		}
	}

	var k K
	var v V
	return k, v, false
}

func (m Map[K, V]) Iter() MapIter[K, V] {
	if m.Impl == nil {
		return MapIter[K, V]{}
	}
	size := len(m.Impl.values) + len(m.Impl.small)
	start := int(gs.get().choose("map", uint32(size)))
	return MapIter[K, V]{
		impl:   m.Impl,
		size:   size,
		start:  start,
		offset: 0,
	}
}

func (m Map[K, V]) Range() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		iter := m.Iter()
		for {
			k, v, ok := iter.Next()
			if !ok {
				break
			}
			if !yield(k, v) {
				break
			}
		}
	}
}

// CloneMap clones a map, exposed by the runtime for maps.Clone.
func CloneMap(m any) any {
	return m.(mapsCloner).mapsClone()
}

type mapsCloner interface {
	mapsClone() any
}

func (m Map[K, V]) mapsClone() any {
	result := NewMap[K, V]()
	iter := m.Iter()
	for k, v, ok := iter.Next(); ok; k, v, ok = iter.Next() {
		result.Set(k, v)
	}
	return result
}

// ReflectMap provides a reflection API to access a Map, used by gosim's
// internal/reflect package.
type ReflectMap interface {
	GetIndex(reflect.Value) (reflect.Value, bool)
	SetIndex(reflect.Value, reflect.Value)
	Iter() ReflectMapIter
	Len() int
	UnsafePointer() unsafe.Pointer
	Type() ReflectMapType
}

type ReflectMapIter interface {
	Key() reflect.Value
	Value() reflect.Value
	Next() bool
}

type ReflectMapType interface {
	Make() any
	KeyType() reflect.Type
	ElemType() reflect.Type
}

var _ ReflectMap = &mapImpl[any, any]{}

func (m *mapImpl[K, V]) Type() ReflectMapType {
	return reflectMapType[K, V]{}
}

func (m *mapImpl[K, V]) Len() int {
	return m.outer().Len()
}

func (m *mapImpl[K, V]) UnsafePointer() unsafe.Pointer {
	return unsafe.Pointer(m)
}

func (m *mapImpl[K, V]) SetIndex(key, value reflect.Value) {
	m.outer().Set(key.Interface().(K), value.Interface().(V))
}

func (m *mapImpl[K, V]) outer() Map[K, V] {
	return Map[K, V]{Impl: m}
}

func (m *mapImpl[K, V]) Iter() ReflectMapIter {
	return &reflectMapIter[K, V]{inner: m.outer().Iter()}
}

func (m *mapImpl[K, V]) GetIndex(key reflect.Value) (reflect.Value, bool) {
	v, ok := m.outer().GetOk(key.Interface().(K))
	return reflect.ValueOf(&v).Elem(), ok
}

type reflectMapIter[K comparable, V any] struct {
	key   K
	value V
	inner MapIter[K, V]
}

var _ ReflectMapIter = &reflectMapIter[any, any]{}

func (i *reflectMapIter[K, V]) Next() bool {
	k, v, ok := i.inner.Next()
	i.key = k
	i.value = v
	return ok
}

func (i *reflectMapIter[K, V]) Key() reflect.Value {
	k := i.key
	return reflect.ValueOf(&k).Elem()
}

func (i *reflectMapIter[K, V]) Value() reflect.Value {
	v := i.value
	return reflect.ValueOf(&v).Elem()
}

type reflectMapType[K comparable, V any] struct{}

var _ ReflectMapType = reflectMapType[any, any]{}

func (reflectMapType[K, V]) Make() any {
	return NewMap[K, V]()
}

func (reflectMapType[K, V]) KeyType() reflect.Type {
	var k [0]K
	return reflect.TypeOf(k).Elem()
}

func (reflectMapType[K, V]) ElemType() reflect.Type {
	var v [0]V
	return reflect.TypeOf(v).Elem()
}
