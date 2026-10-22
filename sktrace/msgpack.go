package sktrace

import (
	"encoding/binary"
	"fmt"
	"math"
)

// decode reads one msgpack value: maps with string keys become
// map[string]any (other keys are formatted as strings: the index keys
// timestamps by integer), arrays []any, integers int64, floats float64,
// strings string, binary and extension data []byte. It is the subset
// simkube's traces use, kept here so the core module needs no msgpack
// library.
func decode(b []byte) (any, []byte, error) {
	if len(b) == 0 {
		return nil, nil, fmt.Errorf("sktrace: unexpected end of data")
	}
	c, b := b[0], b[1:]
	switch {
	case c <= 0x7f:
		return int64(c), b, nil
	case c >= 0xe0:
		return int64(int8(c)), b, nil
	case c&0xf0 == 0x80:
		return decodeMap(b, int(c&0x0f))
	case c&0xf0 == 0x90:
		return decodeArray(b, int(c&0x0f))
	case c&0xe0 == 0xa0:
		return decodeStr(b, int(c&0x1f))
	}
	need := func(n int) ([]byte, []byte, error) {
		if len(b) < n {
			return nil, nil, fmt.Errorf("sktrace: unexpected end of data")
		}
		return b[:n], b[n:], nil
	}
	length := func(n int) (int, []byte, error) {
		p, rest, err := need(n)
		if err != nil {
			return 0, nil, err
		}
		switch n {
		case 1:
			return int(p[0]), rest, nil
		case 2:
			return int(binary.BigEndian.Uint16(p)), rest, nil
		}
		return int(binary.BigEndian.Uint32(p)), rest, nil
	}
	switch c {
	case 0xc0:
		return nil, b, nil
	case 0xc2:
		return false, b, nil
	case 0xc3:
		return true, b, nil
	case 0xc4, 0xc5, 0xc6: // bin
		n, rest, err := length(1 << (c - 0xc4))
		if err != nil {
			return nil, nil, err
		}
		b = rest
		p, rest, err := need(n)
		return append([]byte(nil), p...), rest, err
	case 0xc7, 0xc8, 0xc9: // ext: length, type, data
		n, rest, err := length(1 << (c - 0xc7))
		if err != nil {
			return nil, nil, err
		}
		b = rest
		p, rest, err := need(n + 1)
		if err != nil {
			return nil, nil, err
		}
		return append([]byte(nil), p[1:]...), rest, nil
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8: // fixext
		p, rest, err := need(1 + 1<<(c-0xd4))
		if err != nil {
			return nil, nil, err
		}
		return append([]byte(nil), p[1:]...), rest, nil
	case 0xca:
		p, rest, err := need(4)
		if err != nil {
			return nil, nil, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(p))), rest, nil
	case 0xcb:
		p, rest, err := need(8)
		if err != nil {
			return nil, nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(p)), rest, nil
	case 0xcc, 0xcd, 0xce, 0xcf: // uint
		n := 1 << (c - 0xcc)
		p, rest, err := need(n)
		if err != nil {
			return nil, nil, err
		}
		var u uint64
		for _, x := range p {
			u = u<<8 | uint64(x)
		}
		return int64(u), rest, nil
	case 0xd0, 0xd1, 0xd2, 0xd3: // int
		n := 1 << (c - 0xd0)
		p, rest, err := need(n)
		if err != nil {
			return nil, nil, err
		}
		var u uint64
		for _, x := range p {
			u = u<<8 | uint64(x)
		}
		shift := 64 - 8*n
		return int64(u<<shift) >> shift, rest, nil
	case 0xd9, 0xda, 0xdb:
		n, rest, err := length(1 << (c - 0xd9))
		if err != nil {
			return nil, nil, err
		}
		return decodeStr(rest, n)
	case 0xdc, 0xdd:
		n, rest, err := length(2 << (c - 0xdc))
		if err != nil {
			return nil, nil, err
		}
		return decodeArray(rest, n)
	case 0xde, 0xdf:
		n, rest, err := length(2 << (c - 0xde))
		if err != nil {
			return nil, nil, err
		}
		return decodeMap(rest, n)
	}
	return nil, nil, fmt.Errorf("sktrace: unsupported msgpack type 0x%02x", c)
}

func decodeStr(b []byte, n int) (any, []byte, error) {
	if len(b) < n {
		return nil, nil, fmt.Errorf("sktrace: unexpected end of data")
	}
	return string(b[:n]), b[n:], nil
}

func decodeArray(b []byte, n int) (any, []byte, error) {
	out := make([]any, 0, n)
	for range n {
		v, rest, err := decode(b)
		if err != nil {
			return nil, nil, err
		}
		out, b = append(out, v), rest
	}
	return out, b, nil
}

func decodeMap(b []byte, n int) (any, []byte, error) {
	out := make(map[string]any, n)
	for range n {
		k, rest, err := decode(b)
		if err != nil {
			return nil, nil, err
		}
		v, rest, err := decode(rest)
		if err != nil {
			return nil, nil, err
		}
		key, ok := k.(string)
		if !ok {
			key = fmt.Sprint(k)
		}
		out[key], b = v, rest
	}
	return out, b, nil
}
