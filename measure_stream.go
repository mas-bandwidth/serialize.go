package serialize

import "unicode/utf8"

// MeasureStream counts how many bits it would take to serialize something, without
// writing any data. It acts like a write stream (IsWriting is true), so a unified
// serialize function measures the exact same fields it would write.
//
// When the serialization includes alignment to byte boundaries, the measurement is an
// estimate rather than exact, because the true pad depends on where the object lands in
// the final bit stream. The estimate is guaranteed to be conservative: every align is
// counted as the worst case 7 bits.
type MeasureStream struct {
	bitsWritten int64
	context     any
	err         error
}

// NewMeasureStream creates a measure stream. The zero value is also ready to use.
func NewMeasureStream() *MeasureStream {
	return &MeasureStream{}
}

// Reset clears the measured bit count and any latched error. The context is kept.
func (s *MeasureStream) Reset() {
	s.bitsWritten = 0
	s.err = nil
}

// IsWriting returns true: a measure stream behaves like a write stream so that unified
// serialize functions measure exactly what they would write.
func (s *MeasureStream) IsWriting() bool { return true }

// IsReading returns false.
func (s *MeasureStream) IsReading() bool { return false }

// fail latches the first error on the stream and returns the latched error.
func (s *MeasureStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// measure adds bits to the measured count.
func (s *MeasureStream) measure(bits int) error {
	if s.err != nil {
		return s.err
	}
	s.bitsWritten += int64(bits)
	return nil
}

// SerializeBits measures bits, which must be in [1,32].
func (s *MeasureStream) SerializeBits(value *uint32, bits int) error {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	return s.measure(bits)
}

// SerializeBits64 measures bits, which must be in [1,64].
func (s *MeasureStream) SerializeBits64(value *uint64, bits int) error {
	if bits < 1 || bits > 64 {
		panic(panicBitsRange64)
	}
	return s.measure(bits)
}

// SerializeInt measures the bits required for the range [min,max]. Like a write, the
// value must be in range or ErrValueOutOfRange is returned.
func (s *MeasureStream) SerializeInt(value *int32, min, max int32) error {
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	if *value < min || *value > max {
		return s.fail(ErrValueOutOfRange)
	}
	return s.measure(BitsRequired(uint32(min), uint32(max)))
}

// SerializeInt64 measures the bits required for the range [min,max]. Like a write, the
// value must be in range or ErrValueOutOfRange is returned.
func (s *MeasureStream) SerializeInt64(value *int64, min, max int64) error {
	if min >= max {
		panic(panicMinMax)
	}
	if s.err != nil {
		return s.err
	}
	if *value < min || *value > max {
		return s.fail(ErrValueOutOfRange)
	}
	return s.measure(BitsRequired64(uint64(min), uint64(max)))
}

// SerializeUint8 measures 8 bits.
func (s *MeasureStream) SerializeUint8(value *uint8) error { return s.measure(8) }

// SerializeUint16 measures 16 bits.
func (s *MeasureStream) SerializeUint16(value *uint16) error { return s.measure(16) }

// SerializeUint32 measures 32 bits.
func (s *MeasureStream) SerializeUint32(value *uint32) error { return s.measure(32) }

// SerializeUint64 measures 64 bits.
func (s *MeasureStream) SerializeUint64(value *uint64) error { return s.measure(64) }

// SerializeBool measures 1 bit.
func (s *MeasureStream) SerializeBool(value *bool) error { return s.measure(1) }

// SerializeFloat32 measures 32 bits.
func (s *MeasureStream) SerializeFloat32(value *float32) error { return s.measure(32) }

// SerializeFloat64 measures 64 bits.
func (s *MeasureStream) SerializeFloat64(value *float64) error { return s.measure(64) }

// SerializeCompressedFloat32 measures the bits required for the quantized range.
func (s *MeasureStream) SerializeCompressedFloat32(value *float32, min, max, resolution float32) error {
	_, bits, _ := compressedFloatParams(min, max, resolution)
	return s.measure(bits)
}

// SerializeBytes measures a worst case align plus the data bytes.
func (s *MeasureStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	return s.measure(len(data) * 8)
}

// SerializeString measures the length prefix, a worst case align, and the string bytes.
// Like a write, the string must fit in bufferSize-1 bytes or ErrValueOutOfRange is
// returned.
func (s *MeasureStream) SerializeString(value *string, bufferSize int) error {
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
	return s.measure(int(length) * 8)
}

// SerializeWideString measures the length prefix plus 32 bits per code point. Like a
// write, the string must fit in bufferSize-1 code points or ErrValueOutOfRange is
// returned.
func (s *MeasureStream) SerializeWideString(value *string, bufferSize int) error {
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
	return s.measure(int(length) * 32)
}

// SerializeAlign measures the conservative worst case align of 7 bits.
func (s *MeasureStream) SerializeAlign() error {
	return s.measure(s.AlignBits())
}

// SerializeObject measures an object that implements Serializable.
func (s *MeasureStream) SerializeObject(object Serializable) error {
	if s.err != nil {
		return s.err
	}
	if err := object.Serialize(s); err != nil {
		return s.fail(err)
	}
	return s.err
}

// SerializeIntRelative measures the encoding of *current relative to previous, exactly
// as SerializeIntRelative on a write stream would encode it. previous must be less than
// *current or ErrValueOutOfRange is returned.
func (s *MeasureStream) SerializeIntRelative(previous int32, current *int32) error {
	if s.err != nil {
		return s.err
	}
	if previous >= *current {
		return s.fail(ErrValueOutOfRange)
	}
	difference := uint32(*current) - uint32(previous)
	bits := 1
	if difference != 1 {
		matched := false
		for _, bucket := range intRelativeBuckets {
			bits++
			if difference <= bucket.max {
				bits += BitsRequired(bucket.min, bucket.max)
				matched = true
				break
			}
		}
		if !matched {
			bits += 32
		}
	}
	return s.measure(bits)
}

// AlignBits returns the worst case align of 7 bits. The number of bits required for
// alignment depends on where an object lands in the final bit stream, so the
// measurement is conservative.
func (s *MeasureStream) AlignBits() int {
	return 7
}

// BitsProcessed returns the number of bits measured so far.
func (s *MeasureStream) BitsProcessed() int64 {
	return s.bitsWritten
}

// BytesProcessed returns the number of bits measured so far, rounded up to the next byte.
func (s *MeasureStream) BytesProcessed() int64 {
	return (s.bitsWritten + 7) / 8
}

// Error returns the first error latched on the stream, or nil.
func (s *MeasureStream) Error() error {
	return s.err
}

// SetContext sets a context value that serialize functions can retrieve with Context.
func (s *MeasureStream) SetContext(context any) {
	s.context = context
}

// Context returns the context value set on the stream. It may be nil.
func (s *MeasureStream) Context() any {
	return s.context
}
