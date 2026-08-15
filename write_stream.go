package serialize

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// WriteStream writes bitpacked data to a buffer. It carries the bit writer state in
// four flat fields with overflow and range checking that returns errors instead of
// panicking, and implements the Stream interface so unified serialize functions can
// write with it.
//
// The writer state is deliberately flat (not a wrapped BitWriter) and minimal, so the
// per-field write path fits the compiler's inlining budget: the pending bit count is
// bitsWritten&63 (every spill removes exactly 64 bits, so the two never drift), and
// the current qword's store offset is (bitsWritten>>3)&^7. The bit stream produced is
// bit-for-bit identical to BitWriter's and to the C++ serialize library.
//
// The zero value is not usable: create a WriteStream with NewWriteStream, or Reset one
// onto a buffer before use.
type WriteStream struct {
	data        []byte
	scratch     uint64 // pending bits: the low bitsWritten&63 bits, above bits zero
	numBits     int64  // len(data)*8, or -1 after a latched error (see fail)
	bitsWritten int64
	context     any
	err         error
}

// NewWriteStream creates a write stream that writes to the given buffer. The buffer size
// must be a multiple of 8 bytes, because the writer stores qwords to memory.
func NewWriteStream(buffer []byte) *WriteStream {
	s := &WriteStream{}
	s.Reset(buffer)
	return s
}

// Reset points the stream at a buffer and clears all write state including any latched
// error, allowing a single stream to be reused without allocation. The context is kept.
// The buffer size must be a multiple of 8 bytes.
func (s *WriteStream) Reset(buffer []byte) {
	if len(buffer)%8 != 0 {
		panic(panicBufferBytes)
	}
	s.data = buffer
	s.scratch = 0
	s.numBits = int64(len(buffer)) * 8
	s.bitsWritten = 0
	s.err = nil
}

// IsWriting returns true.
func (s *WriteStream) IsWriting() bool { return true }

// IsReading returns false.
func (s *WriteStream) IsReading() bool { return false }

// fail latches the first error on the stream and returns the latched error. It also
// poisons the bit budget, so the per-write bounds check refuses every write after an
// error without a separate error load on the hot path: writes after a latched error
// return the latched error and write nothing, exactly as before. Reset restores the
// budget.
func (s *WriteStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	s.numBits = -1
	return s.err
}

// tryWriteBits packs bits that have already been validated to [1,32], with value
// already masked to that width, reporting false if the write would go past the end of
// the buffer — or if the stream carries a latched error, because fail poisons numBits
// and the same comparison covers both. It exists so per-field serialize calls can fuse
// the bounds check and the bit packing into their own bodies: the body stays within
// the compiler's inlining budget (the write twin of BitReader.readBits).
//
// The value MUST be masked to bits by the caller: unmasked high bits would corrupt the
// scratch above the field and leak into the spill carry.
func (s *WriteStream) tryWriteBits(value uint32, bits int) bool {
	bw := s.bitsWritten
	if bw+int64(bits) > s.numBits {
		return false
	}
	sb := int(bw) & 63
	s.scratch |= uint64(value) << sb
	s.bitsWritten = bw + int64(bits)
	if sb+bits >= 64 {
		binary.LittleEndian.PutUint64(s.data[(bw>>3)&^7:], s.scratch)
		s.scratch = uint64(value) >> (64 - sb)
	}
	return true
}

// writeBits bounds checks and writes bits that have already been validated to [1,32]
// and masked to that width. One outlined call per field with no calls inside it: the
// packing fast path and the error latch both fuse into this body.
func (s *WriteStream) writeBits(value uint32, bits int) error {
	if !s.tryWriteBits(value, bits) {
		return s.fail(ErrOverflow)
	}
	return nil
}

// writeBool writes a boolean value as one bit.
func (s *WriteStream) writeBool(value bool) error {
	v := uint32(0)
	if value {
		v = 1
	}
	return s.writeBits(v, 1)
}

// SerializeBits writes the low order bits of *value. bits must be in [1,32].
func (s *WriteStream) SerializeBits(value *uint32, bits int) error {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	return s.writeBits(*value&(^uint32(0)>>(32-bits)), bits)
}

// SerializeBits64 writes the low order bits of *value. bits must be in [1,64].
// Values wider than 32 bits are written as the low dword first, then the high remainder.
func (s *WriteStream) SerializeBits64(value *uint64, bits int) error {
	if bits < 1 || bits > 64 {
		panic(panicBitsRange64)
	}
	v := *value
	if bits <= 32 {
		return s.writeBits(uint32(v)&(^uint32(0)>>(32-bits)), bits)
	}
	if s.bitsWritten+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	s.tryWriteBits(uint32(v), 32)
	s.tryWriteBits(uint32(v>>32)&(^uint32(0)>>(64-bits)), bits-32)
	return nil
}

// SerializeInt writes *value, which must be in [min,max], using only the bits required
// to represent the range. Returns ErrValueOutOfRange if it is not.
func (s *WriteStream) SerializeInt(value *int32, min, max int32) error {
	if min > max {
		panic(panicMinMax)
	}
	v := *value
	if v < min || v > max {
		return s.fail(ErrValueOutOfRange)
	}
	bits := BitsRequired(uint32(min), uint32(max))
	// subtract in the unsigned domain: the range may be wider than 2^31. The offset
	// is within the range, so it is already masked to bits.
	if !s.tryWriteBits(uint32(v)-uint32(min), bits) {
		return s.fail(ErrOverflow)
	}
	return nil
}

// SerializeInt64 writes *value, which must be in [min,max], using only the bits required
// to represent the range. Returns ErrValueOutOfRange if it is not.
func (s *WriteStream) SerializeInt64(value *int64, min, max int64) error {
	if min > max {
		panic(panicMinMax)
	}
	v := *value
	if v < min || v > max {
		return s.fail(ErrValueOutOfRange)
	}
	bits := BitsRequired64(uint64(min), uint64(max))
	// subtract in the unsigned domain: the range may be wider than 2^63; the offset
	// is within the range, so both dwords are already masked to their widths
	unsigned := uint64(v) - uint64(min)
	if s.bitsWritten+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	if bits <= 32 {
		s.tryWriteBits(uint32(unsigned), bits)
		return nil
	}
	// low dword first, then the high remainder: same convention as SerializeBits64
	s.tryWriteBits(uint32(unsigned), 32)
	s.tryWriteBits(uint32(unsigned>>32), bits-32)
	return nil
}

// writeGroups128 writes a 128 bit offset in 32 bit groups, least significant group
// first, with the final group carrying the remainder: the same splitting convention
// as SerializeBits64 and SerializeInt64. numBits must be in [1,128] and already
// bounds checked against the buffer, and the offset must be within its numBits-wide
// range, so every group is already masked to its width.
func (s *WriteStream) writeGroups128(offset Uint128, numBits int) {
	switch {
	case numBits <= 32:
		s.tryWriteBits(uint32(offset.Lo), numBits)
	case numBits <= 64:
		s.tryWriteBits(uint32(offset.Lo), 32)
		s.tryWriteBits(uint32(offset.Lo>>32), numBits-32)
	case numBits <= 96:
		s.tryWriteBits(uint32(offset.Lo), 32)
		s.tryWriteBits(uint32(offset.Lo>>32), 32)
		s.tryWriteBits(uint32(offset.Hi), numBits-64)
	default:
		s.tryWriteBits(uint32(offset.Lo), 32)
		s.tryWriteBits(uint32(offset.Lo>>32), 32)
		s.tryWriteBits(uint32(offset.Hi), 32)
		s.tryWriteBits(uint32(offset.Hi>>32), numBits-96)
	}
}

// SerializeInt128 writes *value, which must be in [min,max], using only the bits
// required to represent the range. Returns ErrValueOutOfRange if it is not. The
// offset from min is computed in the unsigned domain, so ranges wider than 2^127
// are exact. Where the range fits 64 bits or fewer the bytes are identical to
// SerializeInt64 over the same bounds.
func (s *WriteStream) SerializeInt128(value *Int128, min, max Int128) error {
	if min.Cmp(max) > 0 {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	v := *value
	if v.Cmp(min) < 0 || v.Cmp(max) > 0 {
		return s.fail(ErrValueOutOfRange)
	}
	numBits := BitsRequired128(min.Uint128(), max.Uint128())
	if s.bitsWritten+int64(numBits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	s.writeGroups128(v.Uint128().Sub(min.Uint128()), numBits)
	return nil
}

// SerializeUint128 writes an unsigned 128 bit integer: the low 64 bit half first,
// then the high half, each half as the low dword then the high dword. When the
// stream is byte aligned the result is the 16 bytes of the value in little endian
// order.
func (s *WriteStream) SerializeUint128(value *Uint128) error {
	if s.bitsWritten+128 > s.numBits {
		return s.fail(ErrOverflow)
	}
	s.tryWriteBits(uint32(value.Lo), 32)
	s.tryWriteBits(uint32(value.Lo>>32), 32)
	s.tryWriteBits(uint32(value.Hi), 32)
	s.tryWriteBits(uint32(value.Hi>>32), 32)
	return nil
}

// SerializeFixed64 writes a raw fixed point value in *value, which must be within
// [min,max] whole units, as an offset from the raw (scaled) minimum in the bits
// required for the raw range. Returns ErrValueOutOfRange if it is not. The wire
// format is byte identical to SerializeInt64 of the raw value over the raw bounds.
func (s *WriteStream) SerializeFixed64(value *int64, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams64(integerBits, fractionBits, min, max)
	// subtract in the unsigned domain: the raw range may be wider than 2^63
	offset := uint64(*value) - rawMin
	if offset > rawRange {
		return s.fail(ErrValueOutOfRange)
	}
	if s.bitsWritten+int64(numBits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	// the offset is within the raw range, so both dwords are already masked
	if numBits <= 32 {
		s.tryWriteBits(uint32(offset), numBits)
		return nil
	}
	// low dword first, then the high remainder: same convention as SerializeInt64
	s.tryWriteBits(uint32(offset), 32)
	s.tryWriteBits(uint32(offset>>32), numBits-32)
	return nil
}

// SerializeFixed128 writes a raw wide fixed point value in *value, which must be
// within [min,max] whole units, as an offset from the raw (scaled) minimum in the
// bits required for the raw range. Returns ErrValueOutOfRange if it is not.
// integerBits plus fractionBits must equal 128.
func (s *WriteStream) SerializeFixed128(value *Int128, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams128(integerBits, fractionBits, min, max)
	if s.err != nil {
		return s.err
	}
	// subtract in the unsigned domain: the raw range may be wider than 2^127
	offset := value.Uint128().Sub(rawMin)
	if offset.Cmp(rawRange) > 0 {
		return s.fail(ErrValueOutOfRange)
	}
	if s.bitsWritten+int64(numBits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	s.writeGroups128(offset, numBits)
	return nil
}

// SerializeUint8 writes an unsigned 8 bit integer.
func (s *WriteStream) SerializeUint8(value *uint8) error {
	return s.writeBits(uint32(*value), 8)
}

// SerializeUint16 writes an unsigned 16 bit integer.
func (s *WriteStream) SerializeUint16(value *uint16) error {
	return s.writeBits(uint32(*value), 16)
}

// SerializeUint32 writes an unsigned 32 bit integer.
func (s *WriteStream) SerializeUint32(value *uint32) error {
	return s.writeBits(*value, 32)
}

// SerializeUint64 writes an unsigned 64 bit integer as the low dword then the high dword.
func (s *WriteStream) SerializeUint64(value *uint64) error {
	if s.bitsWritten+64 > s.numBits {
		return s.fail(ErrOverflow)
	}
	v := *value
	s.tryWriteBits(uint32(v), 32)
	s.tryWriteBits(uint32(v>>32), 32)
	return nil
}

// SerializeBool writes a boolean value with one bit.
func (s *WriteStream) SerializeBool(value *bool) error {
	return s.writeBool(*value)
}

// SerializeFloat32 writes an uncompressed 32 bit floating point value.
func (s *WriteStream) SerializeFloat32(value *float32) error {
	return s.writeBits(math.Float32bits(*value), 32)
}

// SerializeFloat64 writes an uncompressed 64 bit floating point value.
func (s *WriteStream) SerializeFloat64(value *float64) error {
	bits := math.Float64bits(*value)
	return s.SerializeUint64(&bits)
}

// SerializeCompressedFloat32 quantizes *value into [min,max] at the given resolution and
// writes only the bits required for the quantized range. The value is clamped into
// [min,max] before quantization; the !>= / !<= clamp form forces NaN into range too.
func (s *WriteStream) SerializeCompressedFloat32(value *float32, min, max, resolution float32) error {
	maxIntegerValue, bits, delta := compressedFloatParams(min, max, resolution)
	normalizedValue := (*value - min) / delta
	if !(normalizedValue >= 0) {
		normalizedValue = 0
	} else if !(normalizedValue <= 1) {
		normalizedValue = 1
	}
	// the clamp bounds the integer value to [0, maxIntegerValue], so it is already
	// masked to bits
	//
	// The inner float32() around the product is LOAD BEARING. STANDARD.md
	// requires float32 arithmetic with TWO roundings — the product rounds
	// before 0.5 is added — and Go permits fusing a multiply and an add into a
	// single FMA unless a conversion forces the intermediate rounding. arm64
	// takes that permission: without this conversion the line compiles to one
	// FMADDS, rounds once, and writes 0 where every other runtime writes 1 for
	// value 0.005 over [0, 10] at resolution 0.01. amd64 emits MULSS+ADDSS and
	// agrees, so CI on x86 stays green while Apple Silicon emits different
	// bytes. Do not "simplify" this.
	integerValue := uint32(math.Floor(float64(float32(normalizedValue*float32(maxIntegerValue)) + 0.5)))
	return s.writeBits(integerValue, bits)
}

// writeBytesStream copies a run of bytes into the bit stream from either a byte slice
// or a string, without bitpacking the aligned middle section. The stream must be
// aligned to a byte boundary and the write already bounds checked. Head bytes are
// bitpacked until the stream reaches a qword boundary, the middle is a straight copy,
// and the tail is bitpacked again. The bytes are identical to BitWriter's writeBytes.
func writeBytesStream[T ~[]byte | ~string](s *WriteStream, data T) {
	if s.bitsWritten%8 != 0 {
		panic(panicNotAligned)
	}

	n := int64(len(data))

	headBytes := min((8-(s.bitsWritten%64)/8)%8, n)
	for i := range headBytes {
		s.tryWriteBits(uint32(data[i]), 8)
	}
	if headBytes == n {
		return
	}

	// the head bytes spilled the scratch exactly at the qword boundary, so the
	// scratch is zero here and the aligned middle is a straight copy
	numWords := (n - headBytes) / 8
	if numWords > 0 {
		copy(s.data[s.bitsWritten>>3:], data[headBytes:headBytes+numWords*8])
		s.bitsWritten += numWords * 64
	}

	for i := headBytes + numWords*8; i < n; i++ {
		s.tryWriteBits(uint32(data[i]), 8)
	}
}

// SerializeBytes aligns the stream to a byte boundary and block copies data into it.
func (s *WriteStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	if s.bitsWritten+int64(len(data))*8 > s.numBits {
		return s.fail(ErrOverflow)
	}
	writeBytesStream(s, data)
	return nil
}

// SerializeString writes the length of *value in [0,bufferSize-1], aligns the stream to
// a byte boundary, then block copies the string bytes. Returns ErrValueOutOfRange if the
// string does not fit in bufferSize-1 bytes.
func (s *WriteStream) SerializeString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if s.err != nil {
		return s.err
	}
	if len(*value) >= bufferSize {
		return s.fail(ErrValueOutOfRange)
	}
	length := int32(len(*value))
	if err := s.SerializeInt(&length, 0, int32(bufferSize-1)); err != nil {
		return err
	}
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	if s.bitsWritten+int64(length)*8 > s.numBits {
		return s.fail(ErrOverflow)
	}
	writeBytesStream(s, *value)
	return nil
}

// SerializeWideString writes the length of *value in UTF-16 code units in
// [0,bufferSize-1], then each code unit as 32 bits. Wire compatible with
// serialize_wstring in the C++ library: STANDARD.md pins each 32 bit group as one
// UTF-16 code unit, so astral code points are split into surrogate pairs — two
// groups — and every platform produces identical bytes. The payload is well formed
// by construction: ranging over a Go string can only yield valid code points
// (invalid UTF-8 decodes as U+FFFD, the writer contract surfacing Go's way), never
// an unpaired surrogate. Returns ErrValueOutOfRange if the string does not fit in
// bufferSize-1 code units.
func (s *WriteStream) SerializeWideString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if s.err != nil {
		return s.err
	}
	length := int32(utf16Length(*value))
	if length >= int32(bufferSize) {
		return s.fail(ErrValueOutOfRange)
	}
	if err := s.SerializeInt(&length, 0, int32(bufferSize-1)); err != nil {
		return err
	}
	for _, r := range *value {
		if r >= 0x10000 {
			hi, lo := utf16.EncodeRune(r)
			if err := s.writeBits(uint32(hi), 32); err != nil {
				return err
			}
			if err := s.writeBits(uint32(lo), 32); err != nil {
				return err
			}
			continue
		}
		if err := s.writeBits(uint32(r), 32); err != nil {
			return err
		}
	}
	return nil
}

// SerializeAlign pads the stream with zero bits to the next byte boundary.
func (s *WriteStream) SerializeAlign() error {
	if s.err != nil {
		return s.err
	}
	alignBits := int(8-s.bitsWritten%8) % 8
	if alignBits == 0 {
		return nil
	}
	return s.writeBits(0, alignBits)
}

// SerializeObject writes an object that implements Serializer.
func (s *WriteStream) SerializeObject(object Serializer) error {
	if s.err != nil {
		return s.err
	}
	if err := object.Serialize(s); err != nil {
		return s.fail(err)
	}
	return s.err
}

// SerializeIntRelative writes *current relative to previous, using fewer bits the closer
// the two values are. previous must be less than *current or ErrValueOutOfRange is
// returned. The difference is computed in the unsigned domain, so gaps wider than 2^31
// wrap and fall through to the absolute 32 bit encoding.
func (s *WriteStream) SerializeIntRelative(previous int32, current *int32) error {
	if s.err != nil {
		return s.err
	}
	if previous >= *current {
		return s.fail(ErrValueOutOfRange)
	}
	difference := uint32(*current) - uint32(previous)
	if err := s.writeBool(difference == 1); err != nil {
		return err
	}
	if difference == 1 {
		return nil
	}
	for _, bucket := range intRelativeBuckets {
		inBucket := difference <= bucket.max
		if err := s.writeBool(inBucket); err != nil {
			return err
		}
		if inBucket {
			v := int32(difference)
			return s.SerializeInt(&v, int32(bucket.min), int32(bucket.max))
		}
	}
	return s.writeBits(uint32(*current), 32)
}

// Flush flushes the last word of bits to memory. Always call this after you finish
// writing and before you call Data, or you risk truncating the last word of data.
// The flush ends the write: do not serialize more values after it.
func (s *WriteStream) Flush() {
	if s.bitsWritten&63 != 0 {
		// the store is a full qword: the buffer size is a multiple of 8 so this
		// stays in bounds, and bytes past the written data are only ever written
		// as zeros
		binary.LittleEndian.PutUint64(s.data[(s.bitsWritten>>3)&^7:], s.scratch)
	}
}

// Data returns the written portion of the buffer: the packet you should send.
//
// IMPORTANT: Call Flush first.
func (s *WriteStream) Data() []byte {
	return s.data[:s.BytesProcessed()]
}

// AlignBits returns the number of bits required to align the stream to the next byte
// boundary, in [0,7].
func (s *WriteStream) AlignBits() int {
	return int(8-s.bitsWritten%8) % 8
}

// BitsProcessed returns the number of bits written so far.
func (s *WriteStream) BitsProcessed() int64 {
	return s.bitsWritten
}

// BytesProcessed returns the number of bits written so far, rounded up to the next byte.
// This is effectively the packet size.
func (s *WriteStream) BytesProcessed() int64 {
	return (s.bitsWritten + 7) / 8
}

// Err returns the first error latched on the stream, or nil.
func (s *WriteStream) Err() error {
	return s.err
}

// SetContext sets a context value that serialize functions can retrieve with Context.
func (s *WriteStream) SetContext(context any) {
	s.context = context
}

// Context returns the context value set on the stream. It may be nil.
func (s *WriteStream) Context() any {
	return s.context
}
