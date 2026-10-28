/*
   serialize.go

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
//     to another integer, in the non-negative int32 domain
//   - Serialize fixed point values with a Q format and [min,max] bounds in whole units, writing
//     only the required bits, with exact round trips — including wide formats like Q112.16 via
//     the 128 bit pair
//   - Serialize 128 bit integers: Uint128 raw at a full 128 bits, Int128 ranged in only the
//     bits its range needs
//   - Alignment support so you can align your bitstream to a byte boundary whenever you want
//   - Unified serialization through the Stream interface, so you can write one function that
//     handles read, write and measure
//
// The bit stream is written to memory in little endian order, which is considered network
// byte order for this library.
//
// The wire format is specified in STANDARD.md, vendored verbatim from the C++ repository
// along with the shared conformance corpus in conformance/. This package implements format
// version 2.0: the wire carries only integers and bytes, and every value has exactly one
// encoding.
//
// Writes are the caller's responsibility. Write and measure misuse — a value outside its
// range or bit width, a non-finite float, double or compressed float value, a bad
// declaration, string text that is ill-formed, holds a NUL or is too long, a write that
// does not fit the buffer — panics at the call (IMPLEMENTING.md U9, the trap; Go has no
// unchecked build, so it traps in every run). A write or measure stream records
// nothing: its Err is always nil. Use CopyString to bound a string to a buffer size.
//
// Values read from a stream are untrusted network data: every read is bounds checked and
// range validated, and the first failure latches an error on the stream, a Refusal that
// names the rule that refused it. A read stream never panics. A packet reader reads the top level object and then
// calls SerializeEnd, which refuses trailing bytes and set padding in the final byte. The failure is
// terminal, as the format requires, and only ReadStream.Reset clears it. Serialize methods
// return that error and become no-ops once it is set, so you can either check each call or
// serialize an entire object and check Stream.Err once at the end — with one rule: a value
// that controls a loop must have its error checked before the loop uses it, because after
// an error values are never updated again and a loop waiting for one spins forever on a
// truncated or malicious packet. Use Continue for sentinel-driven loops, and check the
// error on serialized loop counts.
package serialize

import (
	"errors"
	"math/bits"
	"strings"
)

// Refusal names the rule that refused a read, from the closed list in STANDARD.md,
// "Refusal reasons". It is the error every stream latches: a stream's Err is a Refusal
// whenever the library itself refused, so errors.Is(err, serialize.ErrNonFiniteFloat)
// and a type switch both work, and comparing the sentinels allocates nothing.
//
// String returns the reason exactly as STANDARD.md spells it, which is how every
// implementation in the family names it; Error prefixes the package name.
//
// A failed stream reports its FIRST refusal: the refusals of later reads on that stream
// are echoes of it and keep its reason.
type Refusal uint8

const (
	// ErrPastEnd is a read that needs more bits than the stream holds. (A write that
	// would go past the end of the buffer is write misuse and panics: the refusal
	// reasons are a reader's, IMPLEMENTING.md U9.)
	ErrPastEnd Refusal = iota + 1

	// ErrValueOutOfRange is a decoded value outside its declared range or domain: a
	// ranged offset past its span, a string length past its buffer, a compressed
	// integer past its maximum, an int_relative outside its domain or not increasing,
	// or a bad declaration on read (IMPLEMENTING.md U7, U16).
	ErrValueOutOfRange

	// ErrNonzeroPadding is a set bit in an align's padding, or in the unused bits of
	// the final byte at the end. It typically means the read and write serialize
	// functions don't match.
	ErrNonzeroPadding

	// ErrTrailingData is a whole byte or more after the end of the object.
	ErrTrailingData

	// ErrNonFiniteFloat is a float or double that is NaN or an infinity, or a
	// compressed float decode that is not finite. The format carries finite values
	// only; a write of one is write misuse and panics.
	ErrNonFiniteFloat

	// ErrInvalidUTF8 is a string payload that is not well-formed UTF-8.
	ErrInvalidUTF8

	// ErrEmbeddedNul is a zero byte among a string's transmitted bytes.
	ErrEmbeddedNul

	// ErrNonCanonical is an encoding a conforming writer never produces for the value
	// it carries: the int_relative absolute tier carrying a bounded difference.
	ErrNonCanonical

	// ErrRejected is the caller's own refusal of a value whose bytes were well formed
	// (STANDARD.md 2.1): ReadStream.Reject, the package level Reject, or a nested
	// object whose Serialize method returns an error when nothing inside it refused.
	// No wire rule produces it.
	ErrRejected
)

// refusalNames are the reasons as STANDARD.md spells them, indexed by Refusal.
var refusalNames = [...]string{
	0:                  "none",
	ErrPastEnd:         "past_end",
	ErrValueOutOfRange: "value_out_of_range",
	ErrNonzeroPadding:  "nonzero_padding",
	ErrTrailingData:    "trailing_data",
	ErrNonFiniteFloat:  "non_finite_float",
	ErrInvalidUTF8:     "invalid_utf8",
	ErrEmbeddedNul:     "embedded_nul",
	ErrNonCanonical:    "non_canonical",
	ErrRejected:        "rejected",
}

// String returns the reason exactly as STANDARD.md spells it, for example
// "non_finite_float". The zero value, no refusal, returns "none", the family spelling
// (IMPLEMENTING.md U14). A value outside the closed list returns "".
func (r Refusal) String() string {
	if int(r) >= len(refusalNames) {
		return ""
	}
	return refusalNames[r]
}

// Error implements error: "serialize: " and the reason.
func (r Refusal) Error() string {
	return "serialize: " + r.String()
}

// rejectedError is the latch a read stream records when a nested object's Serialize
// method refuses with an error of its own: it is ErrRejected to errors.Is and errors.As,
// and it unwraps to the caller's error, so neither the reason nor the caller's error is
// lost. It is built only on that refusal path.
type rejectedError struct {
	cause error
}

func (e *rejectedError) Error() string { return "serialize: rejected: " + e.cause.Error() }

func (e *rejectedError) Unwrap() error { return e.cause }

func (e *rejectedError) Is(target error) bool { return target == ErrRejected }

func (e *rejectedError) As(target any) bool {
	if refusal, ok := target.(*Refusal); ok {
		*refusal = ErrRejected
		return true
	}
	return false
}

// rejection is what a read stream latches for an error a nested object's Serialize method
// returned with nothing latched: an error that is a Refusal from the closed list is the
// caller refusing with that reason and latches as it, a Refusal outside the list is the
// caller refusing without saying why, and any other error is the caller's own refusal,
// rejected, with the error kept.
func rejection(err error) error {
	var refusal Refusal
	if errors.As(err, &refusal) {
		if refusal >= ErrPastEnd && refusal <= ErrRejected {
			return refusal
		}
		return ErrRejected
	}
	return &rejectedError{cause: err}
}

// RefusalName returns the family name of a stream's error (IMPLEMENTING.md U14): "none"
// for nil, a stream that has not refused, and the STANDARD.md spelling for a Refusal,
// including the rejected wrapper a read object latches. It returns "" for any other
// error, which only a caller's own Serialize returns.
func RefusalName(err error) string {
	if err == nil {
		return refusalNames[0]
	}
	var refusal Refusal
	if errors.As(err, &refusal) {
		return refusal.String()
	}
	return ""
}

// closedRefusal maps a reason outside the closed list, none included, to ErrRejected:
// the caller refusing without saying why (IMPLEMENTING.md U14).
func closedRefusal(reason Refusal) Refusal {
	if reason < ErrPastEnd || reason > ErrRejected {
		return ErrRejected
	}
	return reason
}

// Fail refuses a read with a reason of the caller's choosing, from a serialize function
// written once for every stream (IMPLEMENTING.md U14): on a ReadStream it is
// ReadStream.Fail. On a WriteStream or MeasureStream there is nothing to refuse, so it
// records nothing, never panics (IMPLEMENTING.md U9), and returns its false: the reason,
// ErrRejected for one outside the closed list, which a serialize function returns as its
// own error.
func Fail(stream Stream, reason Refusal) error {
	if reader, ok := stream.(*ReadStream); ok {
		return reader.Fail(reason)
	}
	return closedRefusal(reason)
}

// Reject refuses a read as the caller's own decision, from a serialize function written
// once for every stream: on a ReadStream it is ReadStream.Reject, latching ErrRejected.
// Reject is read only: on a WriteStream or MeasureStream there is nothing to refuse, so
// calling it there is caller misuse and panics (IMPLEMENTING.md U9), as the reference's
// serialize_reject asserts. A serialize function that may refuse on every stream
// guards it with stream.IsReading(), or calls Fail, which never panics.
func Reject(stream Stream) error {
	if reader, ok := stream.(*ReadStream); ok {
		return reader.Reject()
	}
	panic(panicRejectOnWrite)
}

// Panic messages for write and measure misuse. These indicate programmer error, never
// bad packet data, so they panic like the debug asserts in the C++ library rather than
// returning an error: this is the port's trap (IMPLEMENTING.md U9), and since Go has no
// unchecked build it traps in every run. A read stream never panics (U7).
const (
	panicBitsRange             = "serialize: bits must be in [1,32]"
	panicBitsRange64           = "serialize: bits must be in [1,64]"
	panicMinMax                = "serialize: min must not be greater than max"
	panicIntRelativeDomain     = "serialize: int relative previous must be in [0,2147483647]"
	panicFixedParams           = "serialize: fixed point requires integer bits >= 1, fraction bits >= 0, a Q format that fits the storage width, and min <= max"
	panicFixedBounds           = "serialize: fixed point bounds in whole units do not fit the Q format"
	panicBufferSize            = "serialize: string buffer size must be in [1,1<<31)"
	panicFloatParams           = "serialize: compressed float requires min < max and resolution > 0"
	panicPrecomputedFloat      = "serialize: precomputed compressed float constants must be exactly what CompressedFloatParams derives"
	panicCompressedFloatDecode = "serialize: compressed float declaration decodes its top integer to a value that is not finite"
	panicWriteOverflow         = "serialize: bit writer overflow"
	panicReadOverflow          = "serialize: bit reader would read past the end of the buffer"
	panicNotAligned            = "serialize: byte array serialization requires byte alignment"
	panicBufferBytes           = "serialize: bit writer buffer size must be a multiple of 8 bytes"
	panicCopyStringDestSize    = "serialize: copy string destination size must be at least 1"
	panicValueOutOfRange       = "serialize: write of a value outside its declared range"
	panicValueTooWide          = "serialize: write of a value wider than its bit width"
	panicNonFiniteFloat        = "serialize: write of a float value that is NaN or an infinity"
	panicStringTooLong         = "serialize: write of a string longer than buffer size - 1"
	panicInvalidUTF8           = "serialize: write of a string that is not well-formed UTF-8"
	panicEmbeddedNul           = "serialize: write of a string holding a NUL"
	panicIntRelativeOrder      = "serialize: int relative current must be greater than previous"
	panicRejectOnWrite         = "serialize: reject on a write or measure stream is caller misuse"
)

// CopyString is the reference's serialize_copy_string (IMPLEMENTING.md U15): the
// longest prefix of source that fits a string buffer of destSize bytes, terminator
// included, without ending inside a character. It takes the bytes before the first
// NUL; if there are more than destSize - 1 of them it keeps the first destSize - 1 and
// then drops bytes from the end while the first byte left out is a UTF-8 continuation
// byte (10xxxxxx). For well-formed UTF-8 the result is text SerializeString with
// bufferSize = destSize accepts. A Go string is a value and assignment copies it but
// does not bound it, so the helper returns the prefix; the rule is on bytes, so it
// applies to any input, ill-formed text included. destSize below 1 is write misuse
// and panics (U9). The result shares source's memory: it allocates nothing.
func CopyString(source string, destSize int) string {
	if destSize < 1 {
		panic(panicCopyStringDestSize)
	}
	if nul := strings.IndexByte(source, 0); nul >= 0 {
		source = source[:nul]
	}
	if len(source) <= destSize-1 {
		return source
	}
	n := destSize - 1
	for n > 0 && source[n]&0xC0 == 0x80 {
		n--
	}
	return source[:n]
}

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

// BitsRequired128 returns the number of bits required to serialize a 128 bit integer
// in range [min,max]. The result is in [0,128]. The subtraction is performed in the
// unsigned domain, so ranges wider than 2^127 are exact rather than overflowing:
// signed bounds must be converted with Int128.Uint128, which preserves the sign
// extended bit pattern.
func BitsRequired128(min, max Uint128) int {
	if min == max {
		return 0
	}
	return max.Sub(min).Len()
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
