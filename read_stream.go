package serialize

import "math"

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
// Any slice length is supported. For the fastest reads, keep at least 7 bytes of slack
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
	if bits <= 32 {
		*value = uint64(s.reader.readBits(bits))
		return nil
	}
	lo := s.reader.readBits(32)
	hi := s.reader.readBits(bits - 32)
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
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	bits := BitsRequired64(uint64(min), uint64(max))
	if s.reader.bitsRead+int64(bits) > s.reader.numBits {
		return s.fail(ErrOverflow)
	}
	var unsigned uint64
	if bits <= 32 {
		unsigned = uint64(s.reader.readBits(bits))
	} else {
		// low dword first, then the high remainder: same convention as SerializeBits64
		lo := s.reader.readBits(32)
		hi := s.reader.readBits(bits - 32)
		unsigned = uint64(hi)<<32 | uint64(lo)
	}
	// compare and add in the unsigned domain: the range may be wider than 2^63
	if unsigned > uint64(max)-uint64(min) {
		return s.fail(ErrValueOutOfRange)
	}
	*value = int64(unsigned + uint64(min))
	return nil
}

// SerializeUint8 reads an unsigned 8 bit integer.
func (s *ReadStream) SerializeUint8(value *uint8) error {
	var v uint32
	if err := s.readBits(&v, 8); err != nil {
		return err
	}
	*value = uint8(v)
	return nil
}

// SerializeUint16 reads an unsigned 16 bit integer.
func (s *ReadStream) SerializeUint16(value *uint16) error {
	var v uint32
	if err := s.readBits(&v, 16); err != nil {
		return err
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
	lo := s.reader.readBits(32)
	hi := s.reader.readBits(32)
	*value = uint64(hi)<<32 | uint64(lo)
	return nil
}

// SerializeBool reads a boolean value from one bit.
func (s *ReadStream) SerializeBool(value *bool) error {
	var v uint32
	if err := s.readBits(&v, 1); err != nil {
		return err
	}
	*value = v != 0
	return nil
}

// SerializeFloat32 reads an uncompressed 32 bit floating point value.
func (s *ReadStream) SerializeFloat32(value *float32) error {
	var v uint32
	if err := s.readBits(&v, 32); err != nil {
		return err
	}
	*value = math.Float32frombits(v)
	return nil
}

// SerializeFloat64 reads an uncompressed 64 bit floating point value.
func (s *ReadStream) SerializeFloat64(value *float64) error {
	var v uint64
	if err := s.SerializeUint64(&v); err != nil {
		return err
	}
	*value = math.Float64frombits(v)
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
	if int64(len(data)) > s.reader.BitsRemaining()/8 {
		return s.fail(ErrOverflow)
	}
	copy(data, s.reader.readSlice(len(data)))
	return nil
}

// SerializeString reads a string of fewer than bufferSize bytes into *value with a
// single allocation. On failure *value is left unmodified.
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
	*value = string(s.reader.readSlice(int(length)))
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
	if int64(length)*32 > s.reader.BitsRemaining() {
		return s.fail(ErrOverflow)
	}
	runes := make([]rune, length)
	for i := range runes {
		codePoint := s.reader.readBits(32)
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
func (s *ReadStream) SerializeAlign() error {
	if s.err != nil {
		return s.err
	}
	alignBits := s.reader.AlignBits()
	if alignBits == 0 {
		return nil
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
