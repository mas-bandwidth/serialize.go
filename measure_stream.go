package serialize

import "math"

// MeasureStream counts how many bits it would take to serialize something, without
// writing any data. It acts like a write stream (IsWriting is true), so a unified
// serialize function measures the exact same fields it would write.
//
// When the serialization includes alignment to byte boundaries, the measurement is an
// estimate rather than exact, because the true pad depends on where the object lands in
// the final bit stream. The estimate is guaranteed to be conservative: every align is
// counted as the worst case 7 bits.
//
// A measure refuses nothing (IMPLEMENTING.md U6): it makes every check a write makes,
// except the room left in a buffer it does not have, and catches misuse by the same
// trap, a panic at the call (U9). It records nothing: Err is always nil.
type MeasureStream struct {
	bitsWritten int64
	context     any
}

// NewMeasureStream creates a measure stream. The zero value is also ready to use.
func NewMeasureStream() *MeasureStream {
	return &MeasureStream{}
}

// Reset clears the measured bit count. The context is kept.
func (s *MeasureStream) Reset() {
	s.bitsWritten = 0
}

// IsWriting returns true: a measure stream behaves like a write stream so that unified
// serialize functions measure exactly what they would write.
func (s *MeasureStream) IsWriting() bool { return true }

// IsReading returns false.
func (s *MeasureStream) IsReading() bool { return false }

// measure adds bits to the measured count. The count is int64 like the other streams,
// so byte based measurements must multiply in the 64 bit domain: len(data)*8 overflows
// int on 32 bit platforms for buffers of 256MB or more.
func (s *MeasureStream) measure(bits int64) error {
	s.bitsWritten += bits
	return nil
}

// SerializeBits measures bits, which must be in [1,32].
func (s *MeasureStream) SerializeBits(value *uint32, bits int) error {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	if *value > ^uint32(0)>>(32-bits) {
		panic(panicValueTooWide) // U8, as the writer
	}
	return s.measure(int64(bits))
}

// SerializeBits64 measures bits, which must be in [1,64].
func (s *MeasureStream) SerializeBits64(value *uint64, bits int) error {
	if bits < 1 || bits > 64 {
		panic(panicBitsRange64)
	}
	if *value > ^uint64(0)>>(64-bits) {
		panic(panicValueTooWide) // U8, as the writer
	}
	return s.measure(int64(bits))
}

// SerializeInt measures the bits required for the range [min,max]. Like a write, the
// value must be in range or the call panics (U9).
func (s *MeasureStream) SerializeInt(value *int32, min, max int32) error {
	if min > max {
		panic(panicMinMax)
	}
	if *value < min || *value > max {
		panic(panicValueOutOfRange)
	}
	return s.measure(int64(BitsRequired(uint32(min), uint32(max))))
}

// SerializeInt64 measures the bits required for the range [min,max]. Like a write, the
// value must be in range or the call panics (U9).
func (s *MeasureStream) SerializeInt64(value *int64, min, max int64) error {
	if min > max {
		panic(panicMinMax)
	}
	if *value < min || *value > max {
		panic(panicValueOutOfRange)
	}
	return s.measure(int64(BitsRequired64(uint64(min), uint64(max))))
}

// SerializeInt128 measures the bits required for the range [min,max]. Like a write,
// the value must be in range or the call panics (U9).
func (s *MeasureStream) SerializeInt128(value *Int128, min, max Int128) error {
	if min.Cmp(max) > 0 {
		panic(panicMinMax)
	}
	if value.Cmp(min) < 0 || value.Cmp(max) > 0 {
		panic(panicValueOutOfRange)
	}
	return s.measure(int64(BitsRequired128(min.Uint128(), max.Uint128())))
}

// SerializeUint128 measures 128 bits.
func (s *MeasureStream) SerializeUint128(value *Uint128) error { return s.measure(128) }

// SerializeFixed64 measures the bits required for the raw (scaled) range. Like a
// write, the raw value must be within [min,max] whole units or the call panics (U9).
func (s *MeasureStream) SerializeFixed64(value *int64, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams64(integerBits, fractionBits, min, max)
	checkFixedPointParams(numBits)
	if uint64(*value)-rawMin > rawRange {
		panic(panicValueOutOfRange)
	}
	return s.measure(int64(numBits))
}

// SerializeFixed128 measures the bits required for the raw (scaled) range. Like a
// write, the raw value must be within [min,max] whole units or the call panics (U9). integerBits plus fractionBits must equal 128.
func (s *MeasureStream) SerializeFixed128(value *Int128, integerBits, fractionBits int, min, max int64) error {
	rawMin, rawRange, numBits := fixedPointParams128(integerBits, fractionBits, min, max)
	checkFixedPointParams(numBits)
	if value.Uint128().Sub(rawMin).Cmp(rawRange) > 0 {
		panic(panicValueOutOfRange)
	}
	return s.measure(int64(numBits))
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

// SerializeFloat32 measures 32 bits. Like a write, the value must be finite or the
// call panics (U9).
func (s *MeasureStream) SerializeFloat32(value *float32) error {
	if !float32BitsAreFinite(math.Float32bits(*value)) {
		panic(panicNonFiniteFloat)
	}
	return s.measure(32)
}

// SerializeFloat64 measures 64 bits. Like a write, the value must be finite or the
// call panics (U9).
func (s *MeasureStream) SerializeFloat64(value *float64) error {
	if !float64BitsAreFinite(math.Float64bits(*value)) {
		panic(panicNonFiniteFloat)
	}
	return s.measure(64)
}

// SerializeCompressedFloat32 measures the bits required for the quantized range. It
// derives the wire constants with CompressedFloatParams, exactly like the write and
// read streams; the wire width is the whole of the measure-side arithmetic, so there
// is no separate audited home to reach here.
func (s *MeasureStream) SerializeCompressedFloat32(value *float32, min, max, resolution float32) error {
	// no validation on this path, exactly as on the write and read streams: the
	// constants come straight out of CompressedFloatParams and are correct by
	// construction
	_, bits, _ := CompressedFloatParams(min, max, resolution)
	// the writer's declaration check, so a measure panics exactly where the write
	// would (IMPLEMENTING.md, "The check model", U9)
	checkCompressedFloatDeclarationFinite(min, max, resolution)
	if !float32BitsAreFinite(math.Float32bits(*value)) {
		panic(panicNonFiniteFloat) // the writer's value check (U6, U9)
	}
	return s.measure(int64(bits))
}

// SerializeCompressedFloat32Precomputed measures the precomputed wire width. The
// constants are validated exactly as on the write and read streams: constants that are
// not what CompressedFloatParams derives are API misuse and panic.
func (s *MeasureStream) SerializeCompressedFloat32Precomputed(value *float32, maxIntegerValue uint32, bits int, delta, min float32) error {
	validateCompressedFloatConstants(maxIntegerValue, bits, delta)
	if !float32BitsAreFinite(math.Float32bits(min + delta)) {
		panic(panicCompressedFloatDecode) // the writer's top decode check (U9)
	}
	if !float32BitsAreFinite(math.Float32bits(*value)) {
		panic(panicNonFiniteFloat) // the writer's value check (U6, U9)
	}
	return s.measure(int64(bits))
}

// SerializeBytes measures a worst case align plus the data bytes.
func (s *MeasureStream) SerializeBytes(data []byte) error {
	return s.measure(int64(s.AlignBits()) + int64(len(data))*8)
}

// SerializeString measures the length prefix, a worst case align, and the string bytes.
// Like a write, text longer than bufferSize-1 bytes, holding a NUL, or not well-formed
// UTF-8 panics (U6, U9).
func (s *MeasureStream) SerializeString(value *string, bufferSize int) error {
	validateBufferSize(bufferSize)
	if len(*value) >= bufferSize {
		panic(panicStringTooLong)
	}
	checkStringText(*value)
	length := int32(len(*value))
	s.bitsWritten += int64(BitsRequired(0, uint32(bufferSize-1)) + s.AlignBits())
	return s.measure(int64(length) * 8)
}

// SerializeAlign measures the conservative worst case align of 7 bits.
func (s *MeasureStream) SerializeAlign() error {
	return s.measure(int64(s.AlignBits()))
}

// SerializeEnd measures the end of the object, which costs nothing.
func (s *MeasureStream) SerializeEnd() error {
	return nil
}

// SerializeObject measures an object that implements Serializer. An error the object's
// Serialize method returns is returned as it is and recorded nowhere (U6, U9).
func (s *MeasureStream) SerializeObject(object Serializer) error {
	return object.Serialize(s)
}

// SerializeIntRelative measures the encoding of *current relative to previous, exactly
// as SerializeIntRelative on a write stream would encode it, and refuses the same
// parameters: previous outside the int_relative domain, or not less than *current,
// panics (U6, U9).
func (s *MeasureStream) SerializeIntRelative(previous int32, current *int32) error {
	if previous < intRelativeMin {
		panic(panicIntRelativeDomain)
	}
	if previous >= *current {
		panic(panicIntRelativeOrder)
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
	return s.measure(int64(bits))
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

// Err returns nil: a measure stream records nothing (IMPLEMENTING.md U6, U9). It
// exists so a unified serialize function can return stream.Err() whatever the stream.
func (s *MeasureStream) Err() error {
	return nil
}

// SetContext sets a context value that serialize functions can retrieve with Context.
func (s *MeasureStream) SetContext(context any) {
	s.context = context
}

// Context returns the context value set on the stream. It may be nil.
func (s *MeasureStream) Context() any {
	return s.context
}
