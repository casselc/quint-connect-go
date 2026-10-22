// Copyright 2009 The Go Authors. All rights reserved.  Use of this source code
// is governed by a BSD-style license that can be found at
// https://go.googlesource.com/go/+/refs/heads/master/LICENSE.

// Based on https://go.googlesource.com/go/+/refs/heads/master/src/reflect/type.go.

package reflect

import (
	"reflect" //gosim:notranslate
	"sync"    //gosim:notranslate

	"github.com/jellevandenhooff/gosim/gosimruntime"
)

func makeMapType() reflect.Type {
	// TODO: this is in a function instead of inlined because translate does not
	// handle the reflect notranslate well otherwise.
	return reflect.TypeFor[gosimruntime.ReflectMap]()
}

var mapInterfaceType reflect.Type = makeMapType()

type Type interface {
	Align() int
	FieldAlign() int
	Method(int) Method
	MethodByName(string) (Method, bool)
	NumMethod() int
	Name() string
	PkgPath() string
	Size() uintptr
	String() string
	Kind() Kind
	Implements(u Type) bool
	AssignableTo(u Type) bool
	ConvertibleTo(u Type) bool
	Comparable() bool
	Bits() int
	ChanDir() ChanDir
	IsVariadic() bool
	Elem() Type
	Field(i int) StructField
	FieldByIndex(index []int) StructField
	FieldByName(name string) (StructField, bool)
	FieldByNameFunc(match func(string) bool) (StructField, bool)
	In(i int) Type
	Key() Type
	Len() int
	NumField() int
	NumIn() int
	NumOut() int
	Out(i int) Type
	OverflowComplex(x complex128) bool
	OverflowFloat(x float64) bool
	OverflowInt(x int64) bool
	OverflowUint(x uint64) bool
	CanSeq() bool
	CanSeq2() bool
	isType()
	// common() *abi.Type
	// uncommon() *uncommonType
}

func unwrapTypes(x []Type) []reflect.Type {
	inner := make([]reflect.Type, len(x))
	for i, v := range x {
		inner[i] = v.(*typeImpl).inner
	}
	return inner
}

type typeImpl struct {
	inner reflect.Type
	kind  wrapKind
}

var jankHashMap sync.Map

func wrapType(typ reflect.Type) Type {
	gosimruntime.BeginControlledNondeterminism()
	defer gosimruntime.EndControlledNondeterminism()

	// XXX: prevent allocations here, cache?
	if typ == nil {
		return nil
	}

	known, ok := jankHashMap.Load(typ)
	if ok {
		return known.(Type)
	}

	if typ.Kind() == reflect.Struct && typ.NumField() == 1 {
		field := typ.Field(0)
		if field.Name == "Impl" && field.Type.Implements(mapInterfaceType) {
			impl := &typeImpl{inner: typ, kind: wrappedMap}
			jankHashMap.Store(typ, impl)
			return impl
		}
	}
	impl := &typeImpl{inner: typ}
	jankHashMap.Store(typ, impl)
	return impl
}

var _ Type = &typeImpl{}

func (t *typeImpl) Align() int {
	return t.inner.Align()
}

func (t *typeImpl) FieldAlign() int {
	return t.inner.FieldAlign()
}

func (t *typeImpl) Method(idx int) Method {
	return wrapMethod(t.inner.Method(idx))
}

func (t *typeImpl) MethodByName(name string) (Method, bool) {
	method, ok := t.inner.MethodByName(name)
	return wrapMethod(method), ok
}

func (t *typeImpl) NumMethod() int {
	return t.inner.NumMethod()
}

func (t *typeImpl) Name() string {
	return t.inner.Name()
}

func (t *typeImpl) PkgPath() string {
	return t.inner.PkgPath()
}

func (t *typeImpl) Size() uintptr {
	return t.inner.Size()
}

func (t *typeImpl) String() string {
	return t.inner.String()
}

func (t *typeImpl) Kind() Kind {
	switch t.kind {
	case normal:
		return t.inner.Kind()
	case wrappedMap:
		return Map
	default:
		panic("help")
	}
}

func (t *typeImpl) Implements(u Type) bool {
	return t.inner.Implements(u.(*typeImpl).inner)
}

func (t *typeImpl) AssignableTo(u Type) bool {
	return t.inner.AssignableTo(u.(*typeImpl).inner)
}

func (t *typeImpl) ConvertibleTo(u Type) bool {
	return t.inner.ConvertibleTo(u.(*typeImpl).inner)
}

func (t *typeImpl) Comparable() bool {
	return t.inner.Comparable()
}

func (t *typeImpl) Bits() int {
	return t.inner.Bits()
}

func (t *typeImpl) ChanDir() ChanDir {
	return t.inner.ChanDir()
}

func (t *typeImpl) IsVariadic() bool {
	return t.inner.IsVariadic()
}

func (t *typeImpl) Elem() Type {
	switch t.kind {
	case wrappedMap:
		descriptor := getDecriptor(t)
		return wrapType(descriptor.ElemType())
	default:
		return wrapType(t.inner.Elem())
	}
}

func (t *typeImpl) Field(i int) StructField {
	return wrapStructField(t.inner.Field(i))
}

func (t *typeImpl) FieldByIndex(index []int) StructField {
	return wrapStructField(t.inner.FieldByIndex(index))
}

func (t *typeImpl) FieldByName(name string) (StructField, bool) {
	field, ok := t.inner.FieldByName(name)
	return wrapStructField(field), ok
}

func (t *typeImpl) FieldByNameFunc(match func(string) bool) (StructField, bool) {
	field, ok := t.inner.FieldByNameFunc(match)
	return wrapStructField(field), ok
}

func (t *typeImpl) In(i int) Type {
	return wrapType(t.inner.In(i))
}

func (t *typeImpl) Key() Type {
	switch t.kind {
	case wrappedMap:
		descriptor := getDecriptor(t)
		return wrapType(descriptor.KeyType())
	default:
		return wrapType(t.inner.Key())
	}
}

func (t *typeImpl) Len() int {
	return t.inner.Len()
}

func (t *typeImpl) NumField() int {
	return t.inner.NumField()
}

func (t *typeImpl) NumIn() int {
	return t.inner.NumIn()
}

func (t *typeImpl) NumOut() int {
	return t.inner.NumOut()
}

func (t *typeImpl) Out(i int) Type {
	return wrapType(t.inner.Out(i))
}

func (t *typeImpl) OverflowComplex(x complex128) bool {
	return t.inner.OverflowComplex(x)
}

func (t *typeImpl) OverflowFloat(x float64) bool {
	return t.inner.OverflowFloat(x)
}

func (t *typeImpl) OverflowInt(x int64) bool {
	return t.inner.OverflowInt(x)
}

func (t *typeImpl) OverflowUint(x uint64) bool {
	return t.inner.OverflowUint(x)
}

func (t *typeImpl) isType() {}

type Method struct {
	Name    string
	PkgPath string
	Type    Type
	Func    Value
	Index   int
}

func wrapMethod(inner reflect.Method) Method {
	return Method{
		Name:    inner.Name,
		PkgPath: inner.PkgPath,
		Type:    wrapType(inner.Type),
		Func:    wrapValue(inner.Func),
		Index:   inner.Index,
	}
}

func (m Method) IsExported() bool {
	return m.PkgPath == ""
}

type StructField struct {
	Name      string
	PkgPath   string
	Type      Type
	Tag       StructTag
	Offset    uintptr
	Index     []int
	Anonymous bool
}

func wrapStructField(inner reflect.StructField) StructField {
	return StructField{
		Name: inner.Name,

		PkgPath: inner.PkgPath,

		Type:      wrapType(inner.Type),
		Tag:       inner.Tag,
		Offset:    inner.Offset,
		Index:     inner.Index,
		Anonymous: inner.Anonymous,
	}
}

func wrapStructFields(inner []reflect.StructField) []StructField {
	outer := make([]StructField, len(inner))
	for i, v := range inner {
		outer[i] = wrapStructField(v)
	}
	return outer
}

func (f StructField) IsExported() bool {
	return f.PkgPath == ""
}

type StructTag = reflect.StructTag

type ChanDir = reflect.ChanDir

const (
	RecvDir ChanDir = 1 << iota
	SendDir
	BothDir = RecvDir | SendDir
)

type Kind = reflect.Kind

type ValueError = reflect.ValueError

const (
	Invalid Kind = iota
	Bool
	Int
	Int8
	Int16
	Int32
	Int64
	Uint
	Uint8
	Uint16
	Uint32
	Uint64
	Uintptr
	Float32
	Float64
	Complex64
	Complex128
	Array
	Chan
	Func
	Interface
	Map
	Pointer
	Slice
	String
	Struct
	UnsafePointer
)

const Ptr = Pointer

func TypeOf(i any) Type {
	return wrapType(reflect.TypeOf(i))
}

func PtrTo(t Type) Type { return PointerTo(t) }

func PointerTo(t Type) Type {
	return wrapType(reflect.PointerTo(t.(*typeImpl).inner))
}

func ChanOf(dir ChanDir, t Type) Type {
	panic("missing")
	// return wrapType(reflect.ChanOf(dir, t.(*typeImpl).inner))
}

func MapOf(key, elem Type) Type {
	panic("missing")
}

func FuncOf(in, out []Type, variadic bool) Type {
	return wrapType(reflect.FuncOf(unwrapTypes(in), unwrapTypes(out), variadic))
}

func SliceOf(t Type) Type {
	return wrapType(reflect.SliceOf(t.(*typeImpl).inner))
}

func StructOf(fields []StructField) Type {
	panic("missing")
}

func ArrayOf(length int, elem Type) Type {
	return wrapType(reflect.ArrayOf(length, elem.(*typeImpl).inner))
}

func TypeFor[T any]() Type {
	var v T
	if t := TypeOf(v); t != nil {
		return t // optimize for T being a non-interface kind
	}
	return TypeOf((*T)(nil)).Elem() // only for an interface kind
}

func (t *typeImpl) CanSeq() bool {
	if t.kind == wrappedMap {
		return true
	}
	return t.inner.CanSeq()
}

func (t *typeImpl) CanSeq2() bool {
	if t.kind == wrappedMap {
		return true
	}
	return t.inner.CanSeq2()
}
