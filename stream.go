package serialize

import "math"

// Stream is the unified serialization interface implemented by WriteStream, ReadStream
// and MeasureStream. It is the Go equivalent of the templated stream parameter in the
// C++ serialize library: write one serialize function against Stream and it handles
// write, read and measure.
//
// All serialize methods take pointers so the same call reads or writes the value
// depending on the stream. Use IsWriting and IsReading inside serialize functions for
// logic that differs between the two, such as initializing fields that are not sent.
//
// Errors are sticky: the first failure latches on the stream and every later serialize
// call returns it without touching the stream, so you can either check every call or
// serialize a whole object and check Err once at the end. The one rule: a value that
// controls a loop must have its error checked before the loop runs, because after an
// error the value is never updated again. See Continue and the Serializer documentation.
type Stream interface {
	// IsWriting returns true if the stream writes or measures values (WriteStream and
	// MeasureStream), and false if it reads them.
	IsWriting() bool

	// IsReading returns true if the stream reads values (ReadStream).
	IsReading() bool

	// SerializeBits serializes the low order bits of an unsigned integer. bits must be
	// in [1,32]. A value in [0,31] can be serialized with just 5 bits and so on.
	SerializeBits(value *uint32, bits int) error

	// SerializeBits64 serializes the low order bits of a 64 bit unsigned integer.
	// bits must be in [1,64].
	SerializeBits64(value *uint64, bits int) error

	// SerializeInt serializes a signed integer in [min,max], writing only the number of
	// bits required to represent the range. On read the value is guaranteed to be in
	// [min,max] if the call succeeds.
	SerializeInt(value *int32, min, max int32) error

	// SerializeInt64 serializes a signed 64 bit integer in [min,max], writing only the
	// number of bits required to represent the range. The full 64 bit range is supported.
	SerializeInt64(value *int64, min, max int64) error

	// SerializeUint8 serializes an unsigned 8 bit integer.
	SerializeUint8(value *uint8) error

	// SerializeUint16 serializes an unsigned 16 bit integer.
	SerializeUint16(value *uint16) error

	// SerializeUint32 serializes an unsigned 32 bit integer.
	SerializeUint32(value *uint32) error

	// SerializeUint64 serializes an unsigned 64 bit integer.
	SerializeUint64(value *uint64) error

	// SerializeBool serializes a boolean value with one bit.
	SerializeBool(value *bool) error

	// SerializeFloat32 serializes an uncompressed 32 bit floating point value.
	SerializeFloat32(value *float32) error

	// SerializeFloat64 serializes an uncompressed 64 bit floating point value.
	SerializeFloat64(value *float64) error

	// SerializeCompressedFloat32 serializes a floating point value in [min,max] with the
	// given resolution, writing only the bits required for the quantized range. On write
	// the value is clamped into [min,max]; on read it is guaranteed to be in [min,max]
	// if the call succeeds.
	SerializeCompressedFloat32(value *float32, min, max, resolution float32) error

	// SerializeBytes serializes an array of bytes. The stream aligns to a byte boundary
	// first, then block copies the data. Both sides must know the length: it is not sent.
	SerializeBytes(data []byte) error

	// SerializeString serializes a string of fewer than bufferSize bytes: the length is
	// serialized in [0,bufferSize-1], the stream aligns to a byte boundary, then the
	// string bytes are block copied. bufferSize mirrors the C++ API, where a string with
	// its terminating null character must fit into the buffer, keeping streams
	// compatible between the two languages.
	SerializeString(value *string, bufferSize int) error

	// SerializeWideString serializes a string as 32 bits per code point, wire compatible
	// with serialize_wstring in the C++ library. The length is serialized in
	// [0,bufferSize-1] code points. On read, code points that are not valid (surrogates
	// or values above 0x10FFFF) fail with ErrValueOutOfRange.
	SerializeWideString(value *string, bufferSize int) error

	// SerializeAlign pads the stream with zero bits to the next byte boundary. On read
	// the padding is verified to be zero.
	SerializeAlign() error

	// SerializeObject serializes an object that implements Serializer.
	SerializeObject(object Serializer) error

	// SerializeIntRelative serializes an integer relative to a previous integer, using
	// fewer bits the closer the two values are. previous must be less than current.
	SerializeIntRelative(previous int32, current *int32) error

	// AlignBits returns the number of bits required to align the stream to the next byte
	// boundary, in [0,7]. MeasureStream always returns the conservative worst case 7.
	AlignBits() int

	// BitsProcessed returns the number of bits written to, read from or measured on
	// the stream.
	BitsProcessed() int64

	// BytesProcessed returns the number of bits processed rounded up to the next byte.
	// After writing, this is effectively the packet size.
	BytesProcessed() int64

	// Err returns the first error latched on the stream, or nil.
	Err() error

	// SetContext sets a context value on the stream. The context lets you pass data
	// through to your serialize functions, for example lookup tables or min/max ranges
	// needed to read and write values. It mirrors the context pointer in the C++
	// library and is unrelated to context.Context.
	SetContext(context any)

	// Context returns the context value set on the stream. It may be nil.
	Context() any
}

// Serializer is the interface implemented by objects that serialize themselves to a
// stream. Write one Serialize method per type and it works for write, read and measure.
//
// Return an error to abort serialization: the standard pattern is to call serialize
// methods for each field and return stream.Err() at the end, adding your own
// validation errors where needed.
//
// IMPORTANT: A value that controls how much more work your serialize function does —
// a loop count or a continuation bit — must have its error checked before you use it.
// Once an error latches, serialize calls are no-ops that leave values unmodified, so a
// loop waiting for a serialized value to change spins forever on a truncated or
// malicious packet. Use Continue or Until for sentinel-driven loops.
type Serializer interface {
	Serialize(stream Stream) error
}

// Continue serializes *more as a single continuation bit and reports whether a
// sentinel-driven loop should proceed, folding the stream error state into the loop
// condition in the style of bufio.Scanner: it returns false as soon as the stream has
// an error, so loops of this form always terminate on truncated or malicious data,
// bounded by the size of the packet.
//
//	hasNext := ... // when writing: true if there is a first element
//	for serialize.Continue(stream, &hasNext) {
//	    // serialize one element
//	    // when writing: set hasNext = true if there is another element
//	}
//	if err := stream.Err(); err != nil {
//	    return err
//	}
//
// Never write the loop as `for hasNext { stream.SerializeBool(&hasNext); ... }`: once
// the stream has an error the failed read leaves hasNext unmodified and the loop never
// exits. For wire formats with the opposite bit polarity — a termination bit rather
// than a continuation bit — use Until. See the README section on reading untrusted data.
func Continue(stream Stream, more *bool) bool {
	if stream.SerializeBool(more) != nil {
		return false
	}
	return *more
}

// Until serializes *done as a single termination bit and reports whether a
// sentinel-driven loop should proceed. It is the inverse of Continue, for wire formats
// that mark the end of a sequence with a true bit instead of marking each element with
// a continuation bit — the polarity cannot be flipped without changing the wire format,
// so both helpers exist. Like Continue, it returns false as soon as the stream has an
// error, so loops of this form always terminate on truncated or malicious data, bounded
// by the size of the packet.
//
//	done := ... // when writing: true if the sequence is empty
//	for serialize.Until(stream, &done) {
//	    // serialize one element
//	    // when writing: set done = true after the last element
//	}
//	if err := stream.Err(); err != nil {
//	    return err
//	}
//
// Never write the loop as `for !done { stream.SerializeBool(&done); ... }`: once the
// stream has an error the failed read leaves done unmodified and the loop never exits.
func Until(stream Stream, done *bool) bool {
	if stream.SerializeBool(done) != nil {
		return false
	}
	return !*done
}

// Interface conformance.
var (
	_ Stream = (*WriteStream)(nil)
	_ Stream = (*ReadStream)(nil)
	_ Stream = (*MeasureStream)(nil)
)

// intRelativeBuckets are the difference buckets used by SerializeIntRelative. Each bucket
// costs one signal bit plus the bits required for its [min,max] range; differences past
// the last bucket fall back to an absolute 32 bit value.
var intRelativeBuckets = [...]struct{ min, max uint32 }{
	{2, 6},
	{7, 23},
	{24, 280},
	{281, 4377},
	{4378, 69914},
}

// compressedFloatParams computes the quantization parameters shared by the write, read
// and measure implementations of SerializeCompressedFloat32. The quantized range is
// clamped so it always fits in a uint32, even for pathological delta / resolution
// ratios; the !>= form of the clamp also catches NaN.
func compressedFloatParams(min, max, resolution float32) (maxIntegerValue uint32, bits int, delta float32) {
	if !(min < max) || !(resolution > 0) {
		panic(panicFloatParams)
	}

	delta = max - min

	values := delta / resolution

	if !(values >= 1.0) {
		values = 1.0
	} else if values > 4294967040.0 { // largest float32 below 2^32
		values = 4294967040.0
	}

	maxIntegerValue = uint32(math.Ceil(float64(values)))

	bits = BitsRequired(0, maxIntegerValue)

	return maxIntegerValue, bits, delta
}

// validateBufferSize panics if a string buffer size cannot express a valid length range.
func validateBufferSize(bufferSize int) {
	if bufferSize < 2 || int64(bufferSize) > math.MaxInt32 {
		panic(panicBufferSize)
	}
}
