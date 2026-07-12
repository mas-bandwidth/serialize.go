package serialize

import (
	"math"
	"unicode/utf8"
)

// WriteStream writes bitpacked data to a buffer. It wraps BitWriter with overflow and
// range checking that returns errors instead of panicking, and implements the Stream
// interface so unified serialize functions can write with it.
//
// The zero value is not usable: create a WriteStream with NewWriteStream, or Reset one
// onto a buffer before use.
type WriteStream struct {
	writer  BitWriter
	context any
	err     error
}

// NewWriteStream creates a write stream that writes to the given buffer. The buffer size
// must be a multiple of 8 bytes, because the bit writer stores qwords to memory.
func NewWriteStream(buffer []byte) *WriteStream {
	s := &WriteStream{}
	s.writer.Reset(buffer)
	return s
}

// Reset points the stream at a buffer and clears all write state including any latched
// error, allowing a single stream to be reused without allocation. The context is kept.
func (s *WriteStream) Reset(buffer []byte) {
	s.writer.Reset(buffer)
	s.err = nil
}

// IsWriting returns true.
func (s *WriteStream) IsWriting() bool { return true }

// IsReading returns false.
func (s *WriteStream) IsReading() bool { return false }

// fail latches the first error on the stream and returns the latched error.
func (s *WriteStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// writeBits bounds checks and writes bits that have already been validated to [1,32].
func (s *WriteStream) writeBits(value uint32, bits int) error {
	if s.err != nil {
		return s.err
	}
	if s.writer.bitsWritten+int64(bits) > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	s.writer.writeBits(value, bits)
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
	return s.writeBits(*value, bits)
}

// SerializeBits64 writes the low order bits of *value. bits must be in [1,64].
// Values wider than 32 bits are written as the low dword first, then the high remainder.
func (s *WriteStream) SerializeBits64(value *uint64, bits int) error {
	if bits < 1 || bits > 64 {
		panic(panicBitsRange64)
	}
	if bits <= 32 {
		return s.writeBits(uint32(*value), bits)
	}
	if s.err != nil {
		return s.err
	}
	if s.writer.bitsWritten+int64(bits) > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	s.writer.writeBits(uint32(*value), 32)
	s.writer.writeBits(uint32(*value>>32), bits-32)
	return nil
}

// SerializeInt writes *value, which must be in [min,max], using only the bits required
// to represent the range. Returns ErrValueOutOfRange if it is not.
func (s *WriteStream) SerializeInt(value *int32, min, max int32) error {
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	v := *value
	if v < min || v > max {
		return s.fail(ErrValueOutOfRange)
	}
	bits := BitsRequired(uint32(min), uint32(max))
	// subtract in the unsigned domain: the range may be wider than 2^31
	return s.writeBits(uint32(v)-uint32(min), bits)
}

// SerializeInt64 writes *value, which must be in [min,max], using only the bits required
// to represent the range. Returns ErrValueOutOfRange if it is not.
func (s *WriteStream) SerializeInt64(value *int64, min, max int64) error {
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	v := *value
	if v < min || v > max {
		return s.fail(ErrValueOutOfRange)
	}
	bits := BitsRequired64(uint64(min), uint64(max))
	// subtract in the unsigned domain: the range may be wider than 2^63
	unsigned := uint64(v) - uint64(min)
	if bits <= 32 {
		return s.writeBits(uint32(unsigned), bits)
	}
	if s.writer.bitsWritten+int64(bits) > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	// low dword first, then the high remainder: same convention as SerializeBits64
	s.writer.writeBits(uint32(unsigned), 32)
	s.writer.writeBits(uint32(unsigned>>32), bits-32)
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
	if s.err != nil {
		return s.err
	}
	if s.writer.bitsWritten+64 > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	s.writer.writeBits(uint32(*value), 32)
	s.writer.writeBits(uint32(*value>>32), 32)
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
	if s.err != nil {
		return s.err
	}
	normalizedValue := (*value - min) / delta
	if !(normalizedValue >= 0) {
		normalizedValue = 0
	} else if !(normalizedValue <= 1) {
		normalizedValue = 1
	}
	integerValue := uint32(math.Floor(float64(normalizedValue*float32(maxIntegerValue) + 0.5)))
	return s.writeBits(integerValue, bits)
}

// SerializeBytes aligns the stream to a byte boundary and block copies data into it.
func (s *WriteStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	if s.writer.bitsWritten+int64(len(data))*8 > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	writeBytes(&s.writer, data)
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
	if s.writer.bitsWritten+int64(length)*8 > s.writer.numBits {
		return s.fail(ErrOverflow)
	}
	writeBytes(&s.writer, *value)
	return nil
}

// SerializeWideString writes the length of *value in code points in [0,bufferSize-1],
// then each code point as 32 bits. Wire compatible with serialize_wstring in the C++
// library. Returns ErrValueOutOfRange if the string does not fit in bufferSize-1 code
// points.
func (s *WriteStream) SerializeWideString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if s.err != nil {
		return s.err
	}
	length := int32(utf8.RuneCountInString(*value))
	if length >= int32(bufferSize) {
		return s.fail(ErrValueOutOfRange)
	}
	if err := s.SerializeInt(&length, 0, int32(bufferSize-1)); err != nil {
		return err
	}
	for _, r := range *value {
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
	alignBits := s.writer.AlignBits()
	if alignBits == 0 {
		return nil
	}
	return s.writeBits(0, alignBits)
}

// SerializeObject writes an object that implements Serializable.
func (s *WriteStream) SerializeObject(object Serializable) error {
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
	s.writer.FlushBits()
}

// Data returns the written portion of the buffer: the packet you should send.
//
// IMPORTANT: Call Flush first.
func (s *WriteStream) Data() []byte {
	return s.writer.Data()
}

// AlignBits returns the number of bits required to align the stream to the next byte
// boundary, in [0,7].
func (s *WriteStream) AlignBits() int {
	return s.writer.AlignBits()
}

// BitsProcessed returns the number of bits written so far.
func (s *WriteStream) BitsProcessed() int64 {
	return s.writer.BitsWritten()
}

// BytesProcessed returns the number of bits written so far, rounded up to the next byte.
// This is effectively the packet size.
func (s *WriteStream) BytesProcessed() int64 {
	return s.writer.BytesWritten()
}

// Error returns the first error latched on the stream, or nil.
func (s *WriteStream) Error() error {
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
