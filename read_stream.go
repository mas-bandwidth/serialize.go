package serialize

import (
	"bytes"
	"math"
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
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
		return s.fail(ErrPastEnd)
	}
	*value = v != 0
	return nil
}

// SerializeFloat32 reads an uncompressed 32 bit floating point value. The format carries
// finite values only (STANDARD.md, "Finite values only, every one bit exact"): a pattern
// whose exponent field is all ones, NaN with any payload or either infinity, fails with
// ErrNonFiniteFloat and leaves *value unmodified. The test is on the raw bits, before
// any conversion. Negative zero and the subnormals are finite and read bit exact.
func (s *ReadStream) SerializeFloat32(value *float32) error {
	if s.err != nil {
		return s.err
	}
	v, ok := s.reader.tryReadBits(32)
	if !ok {
		return s.fail(ErrPastEnd)
	}
	if !float32BitsAreFinite(v) {
		return s.fail(ErrNonFiniteFloat)
	}
	*value = math.Float32frombits(v)
	return nil
}

// SerializeFloat64 reads an uncompressed 64 bit floating point value. Exactly as for
// SerializeFloat32, a non-finite pattern fails with ErrNonFiniteFloat and leaves *value
// unmodified, and every finite pattern reads bit exact.
func (s *ReadStream) SerializeFloat64(value *float64) error {
	if s.err != nil {
		return s.err
	}
	if s.reader.bitsRead+64 > s.reader.numBits {
		return s.fail(ErrPastEnd)
	}
	v := s.reader.readBits64(64)
	if !float64BitsAreFinite(v) {
		return s.fail(ErrNonFiniteFloat)
	}
	*value = math.Float64frombits(v)
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
// never a panic. A decode that is not finite fails with ErrNonFiniteFloat and leaves
// *value unmodified: every operand is finite and the integer is in range, and still the
// final add can round past the largest float32 (STANDARD.md, "The decode must be
// finite"), so no declaration can hand NaN or an infinity to the far side of the wire.
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
	decoded := float32(normalizedValue*delta) + min
	if !float32BitsAreFinite(math.Float32bits(decoded)) {
		return s.fail(ErrNonFiniteFloat)
	}
	*value = decoded
	return nil
}

// SerializeBytes aligns the stream to a byte boundary and block copies len(data) bytes
// into data.
//
// data is a caller-owned buffer, the one destination a refused read makes no promise
// about: treat its contents as unspecified after a failure (STANDARD.md, Reader
// Obligations). The scalar reads on this stream do promise non-mutation.
func (s *ReadStream) SerializeBytes(data []byte) error {
	if err := s.SerializeAlign(); err != nil {
		return err
	}
	// compare in bytes rather than bits, consistent with the 64 bit bookkeeping
	if int64(len(data)) > s.reader.BitsRemaining()/8 {
		return s.fail(ErrPastEnd)
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
// The payload is validated: a NUL byte fails the read with ErrEmbeddedNul, and
// malformed UTF-8 with ErrInvalidUTF8. Neither can come from a conforming writer — a
// transmitted NUL would give the same payload two disagreeing lengths, the wire's and
// the one every consumer that stops at a zero computes, and well-formed UTF-8 is the
// writer's contract (STANDARD.md string). The NUL scan runs over the whole payload
// first, so a payload that breaks both rules is ErrEmbeddedNul. Readers face untrusted
// bytes, so both rules are enforced here, in every build. A well-formed payload is
// handed back byte for byte: no normalization, no replacement, no stripped byte order
// mark. Arbitrary byte payloads belong to SerializeBytes. On failure *value is left
// unmodified.
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
		return s.fail(ErrPastEnd)
	}
	data := s.reader.readSlice(int(length))
	if bytes.IndexByte(data, 0) >= 0 {
		// an interior NUL: impossible from a conforming writer, and the
		// two-lengths smuggling primitive if accepted
		return s.fail(ErrEmbeddedNul)
	}
	if !utf8.Valid(data) {
		return s.fail(ErrInvalidUTF8)
	}
	if *value != string(data) { // the compiler compares without converting: no allocation
		*value = string(data) // one allocation, only when the content actually changed
	}
	return nil
}

// SerializeAlign skips ahead to the next byte boundary, verifying that the padding bits
// are zero. Nonzero padding fails with ErrNonzeroPadding, which typically means the read and
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
		return s.fail(ErrPastEnd)
	}
	if s.reader.readBits(alignBits) != 0 {
		return s.fail(ErrNonzeroPadding)
	}
	return nil
}

// SerializeEnd reads the end of the object: call it after the last field of the top
// level object a packet carries, with the stream holding exactly the packet. An object
// ends at the bit index after its last field, and the stream must end there too
// (STANDARD.md end). A whole byte or more left over fails with ErrTrailingData, even
// when it is zero, and a set bit among the final byte's unused bits fails with
// ErrNonzeroPadding; trailing data is checked first. The bits of the last field are
// never examined. An accepted end consumes the final byte's padding, so the stream is
// then exactly consumed and any further read of one bit or more is past the end.
//
// Without the end, trailing bytes and padding go unexamined, and one value no longer
// has exactly one byte sequence.
func (s *ReadStream) SerializeEnd() error {
	if s.err != nil {
		return s.err
	}
	remaining := s.reader.numBits - s.reader.bitsRead
	if remaining >= 8 {
		return s.fail(ErrTrailingData)
	}
	if remaining > 0 && s.reader.readBits(int(remaining)) != 0 {
		return s.fail(ErrNonzeroPadding)
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

// SerializeIntRelative reads *current relative to previous. previous is the caller's own
// state and never arrives off the wire, so a previous outside the int_relative domain is
// API misuse and panics.
//
// Every tier's reconstruction is checked (STANDARD.md int_relative): the value is rebuilt
// in int64, which cannot wrap, and the read fails with ErrValueOutOfRange unless the
// result lies in the domain and is strictly greater than previous. That binds in the one
// bit tier, in each of the five bucket tiers and in the absolute tier, whose 32 raw bits
// are read unsigned — a group with the top bit set is above the domain and is refused,
// rather than reappearing as a negative value.
//
// The absolute tier is canonical too: one that carries a current in the domain, above
// previous and no more than 69914 above it — a difference a bounded tier takes — fails
// with ErrNonCanonical, because the writer always takes the first tier that fits and
// the absolute encoding is a second encoding of that value. The domain and ordering
// rule comes first, so a current outside the domain is ErrValueOutOfRange whatever its
// difference. On failure *current is left unmodified.
func (s *ReadStream) SerializeIntRelative(previous int32, current *int32) error {
	if previous < intRelativeMin {
		panic(panicIntRelativeDomain)
	}
	if s.err != nil {
		return s.err
	}
	var flag bool
	if err := s.SerializeBool(&flag); err != nil {
		return err
	}
	if flag {
		return s.intRelativeResult(previous, int64(previous)+1, current)
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
			return s.intRelativeResult(previous, int64(previous)+int64(difference), current)
		}
	}
	var v uint32
	if err := s.readBits(&v, 32); err != nil {
		return err
	}
	// the absolute tier's group is unsigned: widening it to int64 keeps a top bit set
	// group above the domain, where intRelativeResult refuses it. The difference is
	// formed in int64 too, after the domain and ordering test, so it cannot wrap and a
	// value outside the domain never reaches the canonical test.
	absolute := int64(v)
	if absolute <= intRelativeMax && absolute > int64(previous) && absolute-int64(previous) <= intRelativeBoundedMax {
		return s.fail(ErrNonCanonical)
	}
	return s.intRelativeResult(previous, absolute, current)
}

// intRelativeResult checks a reconstructed int_relative value against the domain and
// against previous, and writes the caller's destination only once both hold. The
// reconstruction arrives as an int64 so it cannot have wrapped on the way here.
func (s *ReadStream) intRelativeResult(previous int32, reconstructed int64, current *int32) error {
	if reconstructed > intRelativeMax || reconstructed <= int64(previous) {
		return s.fail(ErrValueOutOfRange)
	}
	*current = int32(reconstructed)
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
