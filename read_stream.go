package serialize

import (
	"bytes"
	"math"
	"unicode/utf16"
	"unicode/utf8"
)

// ReadStream reads bitpacked data from a buffer. It wraps BitReader with bounds and
// range checking on every read, so maliciously crafted packets fail with errors instead
// of panicking or smuggling out of range values, and implements the Stream interface so
// unified serialize functions can read with it.
//
// The zero value is an exhausted stream: create a ReadStream with NewReadStream, or
// Reset one onto a buffer before use.
type ReadStream struct {
	reader  BitReader
	context any
	err     error
}

// NewReadStream creates a read stream that reads the bitpacked data in the given slice.
// Any slice length is supported. For the fastest reads, keep at least 12 bytes of slack
// in the backing array beyond the data: see BitReader for details.
func NewReadStream(data []byte) *ReadStream {
	s := &ReadStream{}
	s.reader.Reset(data)
	return s
}

// Reset points the stream at a data slice and clears all read state including any
// latched error, allowing a single stream to be reused without allocation. The context
// is kept.
func (s *ReadStream) Reset(data []byte) {
	s.reader.Reset(data)
	s.err = nil
}

// IsWriting returns false.
func (s *ReadStream) IsWriting() bool { return false }

// IsReading returns true.
func (s *ReadStream) IsReading() bool { return true }

// fail latches the first error on the stream and returns the latched error.
func (s *ReadStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// readBits bounds checks and reads bits that have already been validated to [1,32].
func (s *ReadStream) readBits(value *uint32, bits int) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+int64(bits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	*value = s.reader.readBits(bits)
	return nil
}

// SerializeBits reads bits into *value. bits must be in [1,32]. On success *value is in
// [0,(1<<bits)-1]; on failure it is left unmodified.
func (s *ReadStream) SerializeBits(value *uint32, bits int) error {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	return s.readBits(value, bits)
}

// SerializeBits64 reads bits into *value. bits must be in [1,64]. Values wider than 32
// bits are read as the low dword first, then the high remainder.
func (s *ReadStream) SerializeBits64(value *uint64, bits int) error {
	if bits < 1 || bits > 64 {
		panic(panicBitsRange64)
	}
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+int64(bits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	*value = s.reader.readBits64(bits)
	return nil
}

// SerializeInt reads a signed integer into *value. On success *value is guaranteed to be
// in [min,max]; values smuggled into the bit headroom of the range fail with
// ErrValueOutOfRange.
func (s *ReadStream) SerializeInt(value *int32, min, max int32) error {
	if min > max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	bits := BitsRequired(uint32(min), uint32(max))
	if s.reader.bitsRead+int64(bits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	unsigned := s.reader.readBits(bits)
	// compare and add in the unsigned domain: the range may be wider than 2^31
	if unsigned > uint32(max)-uint32(min) {
		return s.fail(ErrValueOutOfRange)
	}
	*value = int32(unsigned + uint32(min))
	return nil
}

// SerializeInt64 reads a signed 64 bit integer into *value. On success *value is
// guaranteed to be in [min,max]; values smuggled into the bit headroom of the range fail
// with ErrValueOutOfRange.
func (s *ReadStream) SerializeInt64(value *int64, min, max int64) error {
	if min > max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	bits := BitsRequired64(uint64(min), uint64(max))
	if s.reader.bitsRead+int64(bits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	unsigned := s.reader.readBits64(bits)
	// compare and add in the unsigned domain: the range may be wider than 2^63
	if unsigned > uint64(max)-uint64(min) {
		return s.fail(ErrValueOutOfRange)
	}
	*value = int64(unsigned + uint64(min))
	return nil
}

// readGroups128 reads a 128 bit offset least significant half first, with the final
// half carrying the remainder: the same splitting convention as SerializeBits64 and
// SerializeInt64. numBits must be in [1,128] and already bounds checked against the
// buffer.
func (s *ReadStream) readGroups128(numBits int) Uint128 {
	var offset Uint128
	if numBits <= 64 {
		offset.Lo = s.reader.readBits64(numBits)
		return offset
	}
	offset.Lo = s.reader.readBits64(64)
	offset.Hi = s.reader.readBits64(numBits - 64)
	return offset
}

// SerializeInt128 reads a signed 128 bit integer into *value. On success *value is
// guaranteed to be in [min,max]; values smuggled into the bit headroom of the range
// fail with ErrValueOutOfRange.
func (s *ReadStream) SerializeInt128(value *Int128, min, max Int128) error {
	if min.Cmp(max) > 0 {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	numBits := BitsRequired128(min.Uint128(), max.Uint128())
	if s.reader.bitsRead+int64(numBits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	offset := s.readGroups128(numBits)
	// compare and add in the unsigned domain: the range may be wider than 2^127
	if offset.Cmp(max.Uint128().Sub(min.Uint128())) > 0 {
		return s.fail(ErrValueOutOfRange)
	}
	*value = offset.Add(min.Uint128()).Int128()
	return nil
}

// SerializeUint128 reads an unsigned 128 bit integer: the low 64 bit half first,
// then the high half, each half as the low dword then the high dword.
func (s *ReadStream) SerializeUint128(value *Uint128) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+128 > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	var v Uint128
	v.Lo = s.reader.readBits64(64)
	v.Hi = s.reader.readBits64(64)
	*value = v
	return nil
}

// SerializeFixed64 reads a raw fixed point value into *value. On success the raw
// value is guaranteed to be within [min,max] whole units; raw values smuggled into
// the bit headroom of the offset encoding fail with ErrValueOutOfRange — reject,
// never clamp. Round trips are exact.
func (s *ReadStream) SerializeFixed64(value *int64, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams64(integerBits, fractionBits, min, max)
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+int64(numBits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	offset := s.reader.readBits64(numBits)
	if offset > rawRange {
		return s.fail(ErrValueOutOfRange)
	}
	// reconstruct in the unsigned domain: wraps two's complement for negative raw values
	*value = int64(rawMin + offset)
	return nil
}

// SerializeFixed128 reads a raw wide fixed point value into *value. On success the
// raw value is guaranteed to be within [min,max] whole units; raw values smuggled
// into the bit headroom of the offset encoding fail with ErrValueOutOfRange —
// reject, never clamp. integerBits plus fractionBits must equal 128.
func (s *ReadStream) SerializeFixed128(value *Int128, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams128(integerBits, fractionBits, min, max)
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+int64(numBits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	offset := s.readGroups128(numBits)
	if offset.Cmp(rawRange) > 0 {
		return s.fail(ErrValueOutOfRange)
	}
	// reconstruct in the unsigned domain: wraps two's complement for negative raw values
	*value = rawMin.Add(offset).Int128()
	return nil
}

// SerializeUint8 reads an unsigned 8 bit integer.
func (s *ReadStream) SerializeUint8(value *uint8) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.reader.tryReadBits(8)
	if !ok {
		return s.fail(ErrOverflow)
	}
	*value = uint8(v)
	return nil
}

// SerializeUint16 reads an unsigned 16 bit integer.
func (s *ReadStream) SerializeUint16(value *uint16) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.reader.tryReadBits(16)
	if !ok {
		return s.fail(ErrOverflow)
	}
	*value = uint16(v)
	return nil
}

// SerializeUint32 reads an unsigned 32 bit integer.
func (s *ReadStream) SerializeUint32(value *uint32) error {
	return s.readBits(value, 32)
}

// SerializeUint64 reads an unsigned 64 bit integer as the low dword then the high dword.
func (s *ReadStream) SerializeUint64(value *uint64) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+64 > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	*value = s.reader.readBits64(64)
	return nil
}

// SerializeBool reads a boolean value from one bit.
func (s *ReadStream) SerializeBool(value *bool) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.reader.tryReadBits(1)
	if !ok {
		return s.fail(ErrOverflow)
	}
	*value = v != 0
	return nil
}

// SerializeFloat32 reads an uncompressed 32 bit floating point value.
func (s *ReadStream) SerializeFloat32(value *float32) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.reader.tryReadBits(32)
	if !ok {
		return s.fail(ErrOverflow)
	}
	*value = math.Float32frombits(v)
	return nil
}

// SerializeFloat64 reads an uncompressed 64 bit floating point value.
func (s *ReadStream) SerializeFloat64(value *float64) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+64 > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	*value = math.Float64frombits(s.reader.readBits64(64))
	return nil
}

// SerializeCompressedFloat32 reads a quantized floating point value. On success *value
// is guaranteed to be in [min,max]. It derives the wire constants with
// CompressedFloatParams, then runs the one audited home of the read-side quantization
// arithmetic: the two functions are the pre-split body, split at the line schema issue
// mas-bandwidth/schema#82 names — everything that depends only on the declaration
// lives in CompressedFloatParams, everything that touches the value or the wire lives
// in compressedFloat32Precomputed, statement for statement — the same home
// SerializeCompressedFloat32Precomputed reaches.
// TestCompressedFloatPrecomputedDifferential holds this composition to byte and bit
// identity against a frozen copy of the original unsplit implementation.
func (s *ReadStream) SerializeCompressedFloat32(value *float32, min, max, resolution float32) error {
	maxIntegerValue, bits, delta := CompressedFloatParams(min, max, resolution)
	// no validation on this path: the constants come straight out of
	// CompressedFloatParams, so they are correct by construction. Only constants
	// supplied by a caller can be wrong, and those enter through the exported
	// precomputed entry point, which validates them there.
	return s.compressedFloat32Precomputed(value, maxIntegerValue, bits, delta, min)
}

// SerializeCompressedFloat32Precomputed reads a quantized floating point value from
// precomputed wire constants — see CompressedFloatParams and the Stream interface
// documentation. Constants that are not exactly what CompressedFloatParams derives
// are API misuse and panic. This is the untrusted-caller boundary; the arithmetic
// itself lives in compressedFloat32Precomputed, which the derive-per-call path
// reaches too.
func (s *ReadStream) SerializeCompressedFloat32Precomputed(value *float32, maxIntegerValue uint32, bits int, delta, min float32) error {
	validateCompressedFloatConstants(maxIntegerValue, bits, delta)
	return s.compressedFloat32Precomputed(value, maxIntegerValue, bits, delta, min)
}

// compressedFloat32Precomputed is the one audited home of the read-side compressed
// float quantization arithmetic. Both entry points reach it: SerializeCompressedFloat32
// after deriving the constants, SerializeCompressedFloat32Precomputed after validating
// the caller's. Quantized values smuggled into the bit headroom fail with
// ErrValueOutOfRange — that is packet data, not API misuse, so it is an error and
// never a panic.
func (s *ReadStream) compressedFloat32Precomputed(value *float32, maxIntegerValue uint32, bits int, delta, min float32) error {
	var integerValue uint32
	if err := s.readBits(&integerValue, bits); err != nil {
		return err
	}
	if integerValue > maxIntegerValue {
		return s.fail(ErrValueOutOfRange)
	}
	normalizedValue := float32(integerValue) / float32(maxIntegerValue)
	// The float32() around the product is LOAD BEARING, for the same reason as
	// the writer's: STANDARD.md pins this arithmetic to float32, the product
	// rounds before min is added, and Go permits contracting the multiply and
	// the add into a single FMA unless a conversion forces the intermediate
	// rounding. arm64 takes that permission: fused, integer 384 over
	// [-100, 100] at resolution 0.01 decodes to -96.159996 where two roundings
	// give -96.160004, so an Apple Silicon reader disagrees with every other
	// runtime about what it just read. A zero min hides the difference (adding
	// zero is exact), which is why the writer's divergence was found first.
	// Do not "simplify" this.
	*value = float32(normalizedValue*delta) + min
	return nil
}

// SerializeBytes aligns the stream to a byte boundary and block copies len(data) bytes
// into data.
func (s *ReadStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	// compare in bytes rather than bits, consistent with the 64 bit bookkeeping
	if int64(len(data)) > s.reader.BitsRemaining()/8 {
		return s.fail(ErrOverflow)
	}
	copy(data, s.reader.readSlice(len(data)))
	return nil
}

// SerializeString reads a string of fewer than bufferSize bytes into *value with at
// most one allocation: when the incoming bytes equal the current contents of *value the
// string is kept as is, so re-reading stable strings into the same value is allocation
// free (the comparison itself does not allocate). When the content differs, *value
// becomes a fresh copy — a string returned by a read never aliases the stream's buffer
// and is never modified by a later read.
//
// The payload is validated: a NUL byte or malformed UTF-8 fails the read with
// ErrValueOutOfRange. Neither can come from a conforming writer — the length derives
// from the NUL-terminated form, so a transmitted NUL would give the same payload two
// disagreeing lengths in C interop, and well-formed UTF-8 is the writer's contract
// (STANDARD.md string). Readers face untrusted bytes, so both rules are enforced
// here, in every build. Arbitrary byte payloads belong to SerializeBytes. On failure
// *value is left unmodified.
func (s *ReadStream) SerializeString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if s.err != nil {
		return s.err
	}
	var length int32
	if err := s.SerializeInt(&length, 0, int32(bufferSize-1)); err != nil {
		return err
	}
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	if int64(length) > s.reader.BitsRemaining()/8 {
		return s.fail(ErrOverflow)
	}
	data := s.reader.readSlice(int(length))
	if bytes.IndexByte(data, 0) >= 0 {
		// an interior NUL: impossible from a conforming writer, and the
		// two-lengths smuggling primitive if accepted
		return s.fail(ErrValueOutOfRange)
	}
	if !utf8.Valid(data) {
		return s.fail(ErrValueOutOfRange)
	}
	if *value != string(data) { // the compiler compares without converting: no allocation
		*value = string(data) // one allocation, only when the content actually changed
	}
	return nil
}

// SerializeWideString reads a string stored as 32 bits per UTF-16 code unit into
// *value, recombining surrogate pairs into the astral code points they encode
// (STANDARD.md: each group carries one UTF-16 code unit, so every platform produces
// identical bytes). The payload is validated, ErrValueOutOfRange on refusal: values
// above 0xFFFF are not UTF-16 code units, an unpaired surrogate is malformed UTF-16,
// and a zero group is an interior NUL — impossible from a conforming writer, whose
// length derives from the NUL-terminated form, and the same two-lengths smuggling
// primitive the narrow path refuses. Readers face untrusted bytes, so these rules
// are enforced here, in every build. On failure *value is left unmodified.
func (s *ReadStream) SerializeWideString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if s.err != nil {
		return s.err
	}
	var length int32
	if err := s.SerializeInt(&length, 0, int32(bufferSize-1)); err != nil {
		return err
	}
	// bounds check the whole string before allocating
	if int64(length)*32 > s.reader.BitsRemaining() {
		return s.fail(ErrOverflow)
	}
	runes := make([]rune, 0, length)
	for i := int32(0); i < length; i++ {
		unit := s.reader.readBits(32)
		if unit == 0 {
			// an interior NUL: impossible from a conforming writer, and the
			// two-lengths smuggling primitive if accepted
			return s.fail(ErrValueOutOfRange)
		}
		if unit > 0xFFFF || (unit >= 0xDC00 && unit <= 0xDFFF) {
			// not a UTF-16 code unit, or a low surrogate with no high surrogate before it
			return s.fail(ErrValueOutOfRange)
		}
		if unit >= 0xD800 && unit <= 0xDBFF {
			// a high surrogate: the pair's low half must follow within the length
			i++
			if i == length {
				return s.fail(ErrValueOutOfRange)
			}
			low := s.reader.readBits(32)
			if low < 0xDC00 || low > 0xDFFF {
				return s.fail(ErrValueOutOfRange)
			}
			runes = append(runes, utf16.DecodeRune(rune(unit), rune(low)))
			continue
		}
		runes = append(runes, rune(unit))
	}
	*value = string(runes)
	return nil
}

// SerializeAlign skips ahead to the next byte boundary, verifying that the padding bits
// are zero. Nonzero padding fails with ErrAlign, which typically means the read and
// write serialize functions don't match.
// The body is shaped to fit the compiler's inlining budget, so an already aligned
// stream pays no call at all: generated code aligns wherever the schema says align,
// and the position is frequently already a byte boundary. The aligned arm returns
// s.err, which is nil on a healthy stream; the padding arm carries the error check
// and the bounds check, exactly as before.
func (s *ReadStream) SerializeAlign() error {
	alignBits := int(-s.reader.bitsRead) & 7 // (8 - bitsRead%8) % 8, in two's complement
	if alignBits == 0 {
		return s.err
	}
	return s.readAlign(alignBits)
}

// readAlign reads and verifies alignBits of zero padding: SerializeAlign's unaligned
// arm, outlined so the aligned arm stays inlinable.
func (s *ReadStream) readAlign(alignBits int) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+int64(alignBits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	if s.reader.readBits(alignBits) != 0 {
		return s.fail(ErrAlign)
	}
	return nil
}

// SerializeObject reads an object that implements Serializer.
func (s *ReadStream) SerializeObject(object Serializer) error {
	if s.err != nil {
		return s.err
	}
	if err := object.Serialize(s); err != nil {
		return s.fail(err)
	}
	return s.err
}

// SerializeIntRelative reads *current relative to previous. The value is reconstructed
// in the unsigned domain, so it wraps rather than overflowing when previous is near the
// top of the int32 range. The absolute fallback encoding validates that the decoded
// value is greater than previous.
func (s *ReadStream) SerializeIntRelative(previous int32, current *int32) error {
	if s.err != nil {
		return s.err
	}
	var flag bool
	if err := s.SerializeBool(&flag); err != nil {
		return err
	}
	if flag {
		*current = int32(uint32(previous) + 1)
		return nil
	}
	for _, bucket := range intRelativeBuckets {
		if err := s.SerializeBool(&flag); err != nil {
			return err
		}
		if flag {
			var difference int32
			if err := s.SerializeInt(&difference, int32(bucket.min), int32(bucket.max)); err != nil {
				return err
			}
			*current = int32(uint32(previous) + uint32(difference))
			return nil
		}
	}
	var v uint32
	if err := s.readBits(&v, 32); err != nil {
		return err
	}
	if int32(v) <= previous {
		return s.fail(ErrValueOutOfRange)
	}
	*current = int32(v)
	return nil
}

// AlignBits returns the number of bits required to align the stream to the next byte
// boundary, in [0,7].
func (s *ReadStream) AlignBits() int {
	return s.reader.AlignBits()
}

// BitsProcessed returns the number of bits read so far.
func (s *ReadStream) BitsProcessed() int64 {
	return s.reader.BitsRead()
}

// BytesProcessed returns the number of bits read so far, rounded up to the next byte.
func (s *ReadStream) BytesProcessed() int64 {
	return (s.reader.BitsRead() + 7) / 8
}

// Err returns the first error latched on the stream, or nil.
func (s *ReadStream) Err() error {
	return s.err
}

// SetContext sets a context value that serialize functions can retrieve with Context.
func (s *ReadStream) SetContext(context any) {
	s.context = context
}

// Context returns the context value set on the stream. It may be nil.
func (s *ReadStream) Context() any {
	return s.context
}
