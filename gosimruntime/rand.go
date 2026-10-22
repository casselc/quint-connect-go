// Copyright 2023 The Go Authors. All rights reserved.  Use of this source code
// is governed by a BSD-style license that can be found at
// https://go.googlesource.com/go/+/refs/heads/master/LICENSE.

package gosimruntime

import (
	"math/bits"
)

func Fastrand64() uint64 {
	return fastrand64()
}

func Fastrandn(n uint32) uint32 {
	return gs.get().choose("rand", n)
}

type fastrander struct {
	state uint64
}

func (f *fastrander) fastrandn(n uint32) uint32 {
	// This is similar to fastrand() % n, but faster.
	// See https://lemire.me/blog/2016/06/27/a-fast-alternative-to-the-modulo-reduction/
	return uint32(uint64(f.fastrand32()) * uint64(n) >> 32)
}

func (f *fastrander) fastrand32() uint32 {
	return uint32(f.fastrand64())
}

// norace to allow modifying state
//
//go:norace
func (f *fastrander) fastrand64() uint64 {
	if inControlledNondeterminism() {
		// TODO: return an actually random value?
		return 0
	}
	f.state += 0xa0761d6478bd642f
	hi, lo := bits.Mul64(f.state, f.state^0xe7037ed1a0b428db)
	return hi ^ lo
}

func fastrandn(n uint32) uint32 {
	return gs.get().fastrander.fastrandn(n)
}

func fastrand64() uint64 {
	return gs.get().fastrander.fastrand64()
}
