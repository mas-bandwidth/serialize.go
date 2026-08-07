package serialize

import (
	"encoding/binary"
	"math"
)

// ReadStream reads bitpacked data from a buffer, with bounds and range checking on
// every read, so maliciously crafted packets fail with errors instead of panicking or
// smuggling out of range values, and implements the Stream interface so unified
// serialize functions can read with it.
//
// The reader state is deliberately flat (BitReader's fields directly on the stream,
// not a wrapped BitReader) so the fused per-field read path fits the compiler's
// inlining budget — the read twin of WriteStream's flat writer state. The bits read
// are exactly BitReader's: each read loads a 64 bit little endian window at the
// current byte position and shifts by the bit remainder; buffers without 7 bytes of
// backing slack past the data read their final bytes from a zero padded tail window
// (see BitReader for the full discussion).
//
// The zero value is an exhausted stream: create a ReadStream with NewReadStream, or
// Reset one onto a buffer before use.
type ReadStream struct {
	data     []byte
	padded   []byte
	numBits  int64 // len(data)*8, or -1 after a latched error (see fail)
	bitsRead int64
	tailBase int      // byte index the tail window is based at
	tail     [16]byte // zero padded copy of the final data bytes (no-slack buffers only)
	context  any
	err      error
}

// NewReadStream creates a read stream that reads the bitpacked data in the given slice.
// Any slice length is supported. For the fastest reads, keep at least 7 bytes of slack
// in the backing array beyond the data: see BitReader for details.
func NewReadStream(data []byte) *ReadStream {
	s := &ReadStream{}
	s.resetReader(data)
	return s
}

// Reset points the stream at a data slice and clears all read state including any
// latched error, allowing a single stream to be reused without allocation. The context
// is kept.
func (s *ReadStream) Reset(data []byte) {
	s.resetReader(data)
	s.err = nil
}

// IsWriting returns false.
func (s *ReadStream) IsWriting() bool { return false }

// IsReading returns true.
func (s *ReadStream) IsReading() bool { return true }

// fail latches the first error on the stream and returns the latched error. It also
// poisons the reader's bit budget, so the fused TryReadBits bounds check refuses
// every read after a latched error without a separate error load on the hot path:
// reads after a latched error return the latched error and read nothing, exactly as
// before. Reset restores the budget. The stream's own methods still consult s.err
// first, so their behavior is unchanged.
func (s *ReadStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	s.numBits = -1
	return s.err
}

// Fail latches err as the stream's error (the first latched error wins) and returns
// the latched error. It exists so generated code pairing with TryReadBits can latch
// overflow and validation errors exactly as the Serialize methods would; custom
// Serializer implementations may use it for the same purpose.
func (s *ReadStream) Fail(err error) error {
	return s.fail(err)
}

// TryReadBits reads bits that have already been validated to [1,32], reporting false
// if the read would go past the end of the buffer — or if the stream carries a
// latched error, because fail poisons the bit budget and the same comparison covers
// both. It exists so generated code can fuse the bounds check and the window load
// into its own body: both this wrapper and the underlying read stay within the
// compiler's inlining budget, so a generated call site pays no call at all. On
// false, latch the failure with Fail(ErrOverflow).
//
// bits MUST be in [1,32] — the schema generator guarantees this for generated call
// sites; misuse produces a corrupt decode, never memory unsafety.
//
// The flat reader state is load-bearing here, like the flat writer state of
// tryWriteBits (serialize.go#19): the same body through a wrapped BitReader costs
// more than the budget allows.
func (s *ReadStream) TryReadBits(bits int) (uint32, bool) {
	if s.bitsRead+int64(bits) > s.numBits {
		return 0, false
	}
	return s.rawReadBits(bits), true
}

// resetReader points the flat reader state at a data slice: BitReader.Reset on the
// stream's own fields. When the backing array has no slack past the data, the final
// bytes are copied into the zero padded tail window so that every read is a single
// 64 bit window load: see BitReader.
func (s *ReadStream) resetReader(data []byte) {
	s.data = data
	s.padded = data[:cap(data)]
	s.numBits = int64(len(data)) * 8
	s.bitsRead = 0
	if cap(data)-len(data) < 7 {
		s.fillTail()
	}
}

// fillTail copies the final bytes of the data into the tail window, zero padded:
// BitReader.fillTail on the stream's own fields.
func (s *ReadStream) fillTail() {
	s.tailBase = max(len(s.data)-8, 0)
	n := copy(s.tail[:], s.data[s.tailBase:])
	clear(s.tail[n:])
}

// rawReadBits is BitReader.readBits on the stream's own fields: the unchecked hot
// path. bits must be in [1,32] and must not read past the end of the buffer.
func (s *ReadStream) rawReadBits(bits int) uint32 {
	byteIndex := int(s.bitsRead >> 3)

	src := s.padded
	if byteIndex+8 > len(src) {
		// no slack past the data: the final bytes live in the zero padded tail window
		src = s.tail[:]
		byteIndex -= s.tailBase
	}
	window := binary.LittleEndian.Uint64(src[byteIndex:])

	output := uint32(window>>(s.bitsRead&7)) & uint32(uint64(1)<<bits-1)

	s.bitsRead += int64(bits)

	return output
}

// readSlice is BitReader.readSlice on the stream's own fields: the next n bytes of
// the underlying data without copying. The stream must be byte aligned and the
// caller must have bounds checked the read.
func (s *ReadStream) readSlice(n int) []byte {
	offset := int(s.bitsRead >> 3)
	s.bitsRead += int64(n) * 8
	return s.data[offset : offset+n]
}

// bitsRemaining is the number of bits still available to read.
func (s *ReadStream) bitsRemaining() int64 {
	return s.numBits - s.bitsRead
}

// readBits bounds checks and reads bits that have already been validated to [1,32].
func (s *ReadStream) readBits(value *uint32, bits int) error {
	if s.err != nil {
		return s.err
	}
	if s.bitsRead+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	*value = s.rawReadBits(bits)
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
	if s.bitsRead+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	if bits <= 32 {
		*value = uint64(s.rawReadBits(bits))
		return nil
	}
	lo := s.rawReadBits(32)
	hi := s.rawReadBits(bits - 32)
	*value = uint64(hi)<<32 | uint64(lo)
	return nil
}

// SerializeInt reads a signed integer into *value. On success *value is guaranteed to be
// in [min,max]; values smuggled into the bit headroom of the range fail with
// ErrValueOutOfRange.
func (s *ReadStream) SerializeInt(value *int32, min, max int32) error {
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	bits := BitsRequired(uint32(min), uint32(max))
	if s.bitsRead+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	unsigned := s.rawReadBits(bits)
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
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	bits := BitsRequired64(uint64(min), uint64(max))
	if s.bitsRead+int64(bits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	var unsigned uint64
	if bits <= 32 {
		unsigned = uint64(s.rawReadBits(bits))
	} else {
		// low dword first, then the high remainder: same convention as SerializeBits64
		lo := s.rawReadBits(32)
		hi := s.rawReadBits(bits - 32)
		unsigned = uint64(hi)<<32 | uint64(lo)
	}
	// compare and add in the unsigned domain: the range may be wider than 2^63
	if unsigned > uint64(max)-uint64(min) {
		return s.fail(ErrValueOutOfRange)
	}
	*value = int64(unsigned + uint64(min))
	return nil
}

// readGroups128 reads a 128 bit offset written in 32 bit groups, least significant
// group first, with the final group carrying the remainder: the same splitting
// convention as SerializeBits64 and SerializeInt64. numBits must be in [1,128] and
// already bounds checked against the buffer.
func (s *ReadStream) readGroups128(numBits int) Uint128 {
	var offset Uint128
	switch {
	case numBits <= 32:
		offset.Lo = uint64(s.rawReadBits(numBits))
	case numBits <= 64:
		offset.Lo = uint64(s.rawReadBits(32))
		offset.Lo |= uint64(s.rawReadBits(numBits-32)) << 32
	case numBits <= 96:
		offset.Lo = uint64(s.rawReadBits(32))
		offset.Lo |= uint64(s.rawReadBits(32)) << 32
		offset.Hi = uint64(s.rawReadBits(numBits - 64))
	default:
		offset.Lo = uint64(s.rawReadBits(32))
		offset.Lo |= uint64(s.rawReadBits(32)) << 32
		offset.Hi = uint64(s.rawReadBits(32))
		offset.Hi |= uint64(s.rawReadBits(numBits-96)) << 32
	}
	return offset
}

// SerializeInt128 reads a signed 128 bit integer into *value. On success *value is
// guaranteed to be in [min,max]; values smuggled into the bit headroom of the range
// fail with ErrValueOutOfRange.
func (s *ReadStream) SerializeInt128(value *Int128, min, max Int128) error {
	if min.Cmp(max) >= 0 {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	numBits := BitsRequired128(min.Uint128(), max.Uint128())
	if s.bitsRead+int64(numBits) > s.numBits {
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
	if s.bitsRead+128 > s.numBits {
		return s.fail(ErrOverflow)
	}
	var v Uint128
	v.Lo = uint64(s.rawReadBits(32))
	v.Lo |= uint64(s.rawReadBits(32)) << 32
	v.Hi = uint64(s.rawReadBits(32))
	v.Hi |= uint64(s.rawReadBits(32)) << 32
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
	if s.bitsRead+int64(numBits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	var offset uint64
	if numBits <= 32 {
		offset = uint64(s.rawReadBits(numBits))
	} else {
		// low dword first, then the high remainder: same convention as SerializeInt64
		lo := s.rawReadBits(32)
		hi := s.rawReadBits(numBits - 32)
		offset = uint64(hi)<<32 | uint64(lo)
	}
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
	if s.bitsRead+int64(numBits) > s.numBits {
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
	v, ok := s.TryReadBits(8)
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
	v, ok := s.TryReadBits(16)
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
	if s.bitsRead+64 > s.numBits {
		return s.fail(ErrOverflow)
	}
	lo := s.rawReadBits(32)
	hi := s.rawReadBits(32)
	*value = uint64(hi)<<32 | uint64(lo)
	return nil
}

// SerializeBool reads a boolean value from one bit.
func (s *ReadStream) SerializeBool(value *bool) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.TryReadBits(1)
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
	v, ok := s.TryReadBits(32)
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
	if s.bitsRead+64 > s.numBits {
		return s.fail(ErrOverflow)
	}
	lo := s.rawReadBits(32)
	hi := s.rawReadBits(32)
	*value = math.Float64frombits(uint64(hi)<<32 | uint64(lo))
	return nil
}

// SerializeCompressedFloat32 reads a quantized floating point value. On success *value
// is guaranteed to be in [min,max]; quantized values smuggled into the bit headroom fail
// with ErrValueOutOfRange.
func (s *ReadStream) SerializeCompressedFloat32(value *float32, min, max, resolution float32) error {
	maxIntegerValue, bits, delta := compressedFloatParams(min, max, resolution)
	var integerValue uint32
	if err := s.readBits(&integerValue, bits); err != nil {
		return err
	}
	if integerValue > maxIntegerValue {
		return s.fail(ErrValueOutOfRange)
	}
	normalizedValue := float32(integerValue) / float32(maxIntegerValue)
	*value = normalizedValue*delta + min
	return nil
}

// SerializeBytes aligns the stream to a byte boundary and block copies len(data) bytes
// into data.
func (s *ReadStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	// compare in bytes rather than bits, consistent with the 64 bit bookkeeping
	if int64(len(data)) > s.bitsRemaining()/8 {
		return s.fail(ErrOverflow)
	}
	copy(data, s.readSlice(len(data)))
	return nil
}

// SerializeString reads a string of fewer than bufferSize bytes into *value with at
// most one allocation: when the incoming bytes equal the current contents of *value the
// string is kept as is, so re-reading stable strings into the same value is allocation
// free (the comparison itself does not allocate). When the content differs, *value
// becomes a fresh copy — a string returned by a read never aliases the stream's buffer
// and is never modified by a later read. On failure *value is left unmodified.
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
	if int64(length) > s.bitsRemaining()/8 {
		return s.fail(ErrOverflow)
	}
	data := s.readSlice(int(length))
	if *value != string(data) { // the compiler compares without converting: no allocation
		*value = string(data) // one allocation, only when the content actually changed
	}
	return nil
}

// SerializeWideString reads a string stored as 32 bits per code point into *value.
// Code points that are not valid (surrogates or values above 0x10FFFF) fail with
// ErrValueOutOfRange. On failure *value is left unmodified.
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
	if int64(length)*32 > s.bitsRemaining() {
		return s.fail(ErrOverflow)
	}
	runes := make([]rune, length)
	for i := range runes {
		codePoint := s.rawReadBits(32)
		if codePoint > 0x10FFFF || (codePoint >= 0xD800 && codePoint <= 0xDFFF) {
			return s.fail(ErrValueOutOfRange)
		}
		runes[i] = rune(codePoint)
	}
	*value = string(runes)
	return nil
}

// SerializeAlign skips ahead to the next byte boundary, verifying that the padding bits
// are zero. Nonzero padding fails with ErrAlign, which typically means the read and
// write serialize functions don't match.
//
// The body is shaped to fit the compiler's inlining budget so an already-aligned
// stream pays no call at all — generated code aligns wherever the schema says align,
// and the position is frequently already a byte boundary. The aligned arm returns
// s.err (nil on a healthy stream) and the padding arm carries the checks, both
// exactly the previous behavior.
func (s *ReadStream) SerializeAlign() error {
	alignBits := int(-s.bitsRead) & 7 // (8 - bits%8) % 8, in two's complement
	if alignBits == 0 {
		return s.err
	}
	return s.readAlign(alignBits)
}

// readAlign reads and verifies alignBits of zero padding: SerializeAlign's unaligned
// arm, outlined.
func (s *ReadStream) readAlign(alignBits int) error {
	if s.err != nil {
		return s.err
	}
	if s.bitsRead+int64(alignBits) > s.numBits {
		return s.fail(ErrOverflow)
	}
	if s.rawReadBits(alignBits) != 0 {
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
	return int((8 - s.bitsRead%8) % 8)
}

// BitsProcessed returns the number of bits read so far.
func (s *ReadStream) BitsProcessed() int64 {
	return s.bitsRead
}

// BytesProcessed returns the number of bits read so far, rounded up to the next byte.
func (s *ReadStream) BytesProcessed() int64 {
	return (s.bitsRead + 7) / 8
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
