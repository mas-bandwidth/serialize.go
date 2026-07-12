/*
   goserialize

   Copyright © 2016 - 2026, Más Bandwidth LLC.

   Redistribution and use in source and binary forms, with or without modification, are permitted
   provided that the conditions of the BSD 3-Clause license are met (see the LICENSE file).
*/

// Package serialize implements fast bitpacked binary serialization for Go.
//
// It is a pure Go port of the C++ serialize library (github.com/mas-bandwidth/serialize)
// and produces bit-for-bit identical output, so streams written by one language can be
// read by the other.
//
// It has the following features:
//
//   - Serialize a bool with only one bit
//   - Serialize any integer value from [1,64] bits writing only that number of bits to the buffer
//   - Serialize signed integer values in [min,max] writing only the required bits to the buffer
//   - Serialize floats, doubles, compressed floats, strings, byte arrays, and integers relative
//     to another integer
//   - Alignment support so you can align your bitstream to a byte boundary whenever you want
//   - Unified serialization through the Stream interface, so you can write one function that
//     handles read, write and measure
//
// The bit stream is written to memory in little endian order, which is considered network
// byte order for this library.
//
// Values read from a stream are untrusted network data: every read is bounds checked and
// range validated, and the first failure latches an error on the stream. Serialize methods
// return that error and become no-ops once it is set, so you can either check each call or
// serialize an entire object and check Stream.Error once at the end.
package serialize

import (
	"errors"
	"math/bits"
)

// Version is the version of this library.
const Version = "1.0.0"

var (
	// ErrOverflow is returned when a read would go past the end of the buffer, or a write
	// would go past the end of the buffer.
	ErrOverflow = errors.New("serialize: stream overflow")

	// ErrValueOutOfRange is returned when a value is outside the range it is serialized with.
	// On read this typically means the packet is corrupt or maliciously crafted.
	ErrValueOutOfRange = errors.New("serialize: value out of range")

	// ErrAlign is returned when the zero pad bits read by an align are not zero.
	// This typically means the read and write serialize functions don't match.
	ErrAlign = errors.New("serialize: nonzero padding bits in align")
)

// Panic messages for API misuse. These indicate programmer error, never bad packet data,
// so they panic like the debug asserts in the C++ library rather than returning an error.
const (
	panicBitsRange     = "serialize: bits must be in [1,32]"
	panicBitsRange64   = "serialize: bits must be in [1,64]"
	panicMinMax        = "serialize: min must be less than max"
	panicBufferSize    = "serialize: string buffer size must be in [2,1<<31]"
	panicFloatParams   = "serialize: compressed float requires min < max and resolution > 0"
	panicWriteOverflow = "serialize: bit writer overflow"
	panicReadOverflow  = "serialize: bit reader would read past the end of the buffer"
	panicNotAligned    = "serialize: byte array serialization requires byte alignment"
	panicBufferBytes   = "serialize: bit writer buffer size must be a multiple of 8 bytes"
)

// BitsRequired returns the number of bits required to serialize an integer in range [min,max].
func BitsRequired(min, max uint32) int {
	if min == max {
		return 0
	}
	return bits.Len32(max - min)
}

// BitsRequired64 returns the number of bits required to serialize a 64 bit integer
// in range [min,max]. The result is in [0,64].
func BitsRequired64(min, max uint64) int {
	if min == max {
		return 0
	}
	// subtract in the unsigned domain: the range may be wider than 2^63
	return bits.Len64(max - min)
}

// SignedToUnsigned converts a signed integer to an unsigned integer with zig-zag encoding.
// 0,-1,+1,-2,+2... becomes 0,1,2,3,4...
func SignedToUnsigned(n int32) uint32 {
	return (uint32(n) << 1) ^ (0 - (uint32(n) >> 31))
}

// UnsignedToSigned converts an unsigned integer to a signed integer with zig-zag encoding.
// 0,1,2,3,4... becomes 0,-1,+1,-2,+2...
func UnsignedToSigned(n uint32) int32 {
	return int32((n >> 1) ^ (0 - (n & 1)))
}
