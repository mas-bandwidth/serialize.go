package serialize

// The precomputed compressed float test section, mirroring the serialize.h tests that
// landed with mas-bandwidth/serialize#83 (the reference leg of schema issue
// mas-bandwidth/schema#82), plus one Go-only leg: the schema compiler's Go backend has
// folded these constants into generated code since mas-bandwidth/schema#79, so the
// differential here holds FOUR implementations to bit identity where the C++ reference
// holds three — the frozen pre-split body, the derive-per-call entry point, the
// precomputed entry point, and the exact arithmetic shape the schema Go emitter inlines.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
	"testing"
)

// Frozen verbatim copies of the pre-split SerializeCompressedFloat32 implementations,
// comments elided, statements identical (locals included: the float32 stores are what
// pin the two roundings). FROZEN: never edit them. They are the code the audited home
// replaced — the derivation as it lived in compressedFloatParams and the per-stream
// bodies as they stood before the split — kept so the differential proves the split
// changed nothing, forever. DO NOT EDIT — with one recorded exception: the normative
// integer clamp of 2026-08-23 (schema#109) is applied to the frozen write below,
// exactly as the C++ reference applied it to its own frozen copy in
// mas-bandwidth/serialize#88. The clamp amends the wire format itself, so every leg
// of the differential carries it; it is marked where it appears.

func frozenCompressedFloatParams(min, max, resolution float32) (maxIntegerValue uint32, bits int, delta float32) {
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

func frozenCompressedFloatWrite(s *WriteStream, value *float32, min, max, resolution float32) error {
	maxIntegerValue, bits, delta := frozenCompressedFloatParams(min, max, resolution)
	normalizedValue := (*value - min) / delta
	if !(normalizedValue >= 0) {
		normalizedValue = 0
	} else if !(normalizedValue <= 1) {
		normalizedValue = 1
	}
	integerValue := uint32(math.Floor(float64(float32(normalizedValue*float32(maxIntegerValue)) + 0.5)))
	// the normative integer clamp (STANDARD.md 2026-08-23, schema#109) — same clamp
	// as the audited home, same reason; the C++ reference amended its frozen copy the
	// same way in mas-bandwidth/serialize#88
	if integerValue > maxIntegerValue {
		integerValue = maxIntegerValue
	}
	return s.writeBits(integerValue, bits)
}

func frozenCompressedFloatRead(s *ReadStream, value *float32, min, max, resolution float32) error {
	maxIntegerValue, bits, delta := frozenCompressedFloatParams(min, max, resolution)
	var integerValue uint32
	if err := s.readBits(&integerValue, bits); err != nil {
		return err
	}
	if integerValue > maxIntegerValue {
		return s.fail(ErrValueOutOfRange)
	}
	normalizedValue := float32(integerValue) / float32(maxIntegerValue)
	*value = float32(normalizedValue*delta) + min
	return nil
}

func frozenCompressedFloatMeasure(s *MeasureStream, min, max, resolution float32) error {
	_, bits, _ := frozenCompressedFloatParams(min, max, resolution)
	return s.measure(int64(bits))
}

// The fold leg: the arithmetic shape the schema compiler's Go backend inlines at every
// compressed float call site since mas-bandwidth/schema#79, with the generation-time
// literals parameterized. For a min == 0 declaration the emitter omits the `- min`
// on write and the `+ min` on read; both omissions are exact identities (x - 0 == x
// for every finite x the subtraction form sees, and the decoded product is never
// negative because the wire integer and delta are non-negative), so this one
// parameterized body stands for both emitted forms.

// errFoldValidation stands for the generated package's ErrValidation: the fold read
// refuses an integer smuggled into the bit headroom before decoding it.
var errFoldValidation = errors.New("fold: integer above maxIntegerValue on the wire")

func foldCompressedFloatWrite(s *WriteStream, value float32, maxIntegerValue uint32, bits int, delta, min float32) error {
	normalizedValue := (value - min) / delta
	if !(normalizedValue >= 0) { // the runtime's clamp form — it forces NaN into range too
		normalizedValue = 0
	} else if !(normalizedValue <= 1) {
		normalizedValue = 1
	}
	integerValue := uint32(float32(normalizedValue*float32(maxIntegerValue)) + 0.5)
	// the normative integer clamp (STANDARD.md 2026-08-23, schema#109): the emitter
	// follows the runtime, so the emitted shape carries the clamp too. Before it,
	// this leg's SerializeBits masked the too-wide code to zero where the audited
	// home leaked it — the two writer families disagreed on the wire in [2^23, 2^24)
	if integerValue > maxIntegerValue {
		integerValue = maxIntegerValue
	}
	return s.SerializeBits(&integerValue, bits)
}

func foldCompressedFloatRead(s *ReadStream, value *float32, maxIntegerValue uint32, bits int, delta, min float32) error {
	integerValue := uint32(0)
	if err := s.SerializeBits(&integerValue, bits); err != nil {
		return err
	}
	if integerValue > maxIntegerValue { // a value smuggled into the bit headroom is refused
		return errFoldValidation
	}
	normalizedValue := float32(integerValue) / float32(maxIntegerValue)
	*value = float32(normalizedValue*delta) + min
	return nil
}

// The declaration corpus: every compressed float declaration the schema compiler's
// examples, bench corpus and test data emit (the first eleven rows are the values the
// schema PR #79 differential published), plus this repo's own declarations, plus
// shapes at the edges of the derivation itself. The constants are pinned: they are
// what a schema compiler emits for each declaration, and CompressedFloatParams must
// derive exactly them.
type compressedFloatShape struct {
	min, max, res           float32
	expectedMaxIntegerValue uint32
	expectedBits            int
}

var compressedFloatShapes = []compressedFloatShape{
	// the schema compiler's corpus: examples, bench/corpus/RealWorld.schema and its test data
	{0.0, 2000.0, 0.1, 20000, 15},
	{-2.0, 2.0, 0.25, 16, 5},
	{-90.0, 90.0, 0.5, 360, 9},
	{0.0, 30.0, 0.5, 60, 6},
	{-100.0, 100.0, 0.25, 800, 10},
	{0.0, 2000.0, 1.0, 2000, 11},
	{0.0, 10.0, 0.02, 500, 9},
	{0.0, 100.0, 0.01, 10000, 14},
	{-180.0, 180.0, 0.01, 36000, 16},
	{0.0, 10.0, 0.01, 1000, 10}, // also this repo's golden wire and compat harness declaration
	{-5.0, 5.0, 0.001, 10000, 14},
	// this repo's own declarations
	{-100.0, 100.0, 0.01, 20000, 15},    // the compat harness shift vector and the reader conformance declaration
	{-10.0, 10.0, 0.01, 2000, 11},       // the C++ reference's fuzz harness declaration
	{-1.0, 1.0, 0.001, 2000, 11},        // this repo's fuzz harness and example declaration
	{-1024.0, 1024.0, 0.01, 204800, 18}, // the benchmark packet's position declaration
	{-1.0, 1.0, 0.0001, 20000, 15},      // the benchmark packet's orientation declaration
	// shapes at the edges of the derivation itself
	{0.0, 1.0, 2.0, 1, 1},                     // resolution coarser than the range: values clamps up to 1
	{0.0, 15.0, 1.0, 15, 4},                   // step count exactly fills the wire width: no headroom to refuse
	{0.0, 1000000.0, 1.0, 1000000, 20},        // a million steps
	{0.0, 10000000000.0, 1.0, 4294967040, 32}, // values clamps down to the largest float below 2^32
	// shapes that discriminate the rounding rule itself: a fractional step count BELOW the
	// half step, where ceil and round disagree. Every row above lands on an integer or within
	// half a step of one, so all of them derive the same constants under either rule -- the
	// corpus could not see a swap (mas-bandwidth/schema#108).
	{0.0, 10.0, 0.3, 34, 6}, // 33.333332 steps: ceil 34, round 33 -- same width, different step count
	{0.0, 63.3, 1.0, 64, 7}, // 63.3 steps: ceil 64 (7 bits), round 63 (6 bits) -- straddles a power of two, so the WIRE WIDTH moves
	// shapes in [2^23, 2^24), where the float32 ulp reaches 1 and the +0.5 rounding could
	// push the code past maxIntegerValue before the normative clamp (2026-08-23,
	// schema#109). The corpus was empty in this band, which is how the defect hid.
	{0.0, 8388609.0, 1.0, 8388609, 24},   // 2^23+1: the reader-rejects witness
	{0.0, 16777215.0, 1.0, 16777215, 24}, // 2^24-1: the wire-divergence witness
}

// TestCompressedFloatParams pins the derived constants for the whole declaration
// corpus against the generation-time table: the adoption contract for every schema
// emitter is that its folded constants equal CompressedFloatParams exactly, per
// declaration, and this is the table both sides are held to.
func TestCompressedFloatParams(t *testing.T) {
	for _, shape := range compressedFloatShapes {
		maxIntegerValue, bits, delta := CompressedFloatParams(shape.min, shape.max, shape.res)
		if maxIntegerValue != shape.expectedMaxIntegerValue {
			t.Errorf("[%v,%v] at %v: maxIntegerValue %d, want %d",
				shape.min, shape.max, shape.res, maxIntegerValue, shape.expectedMaxIntegerValue)
		}
		if bits != shape.expectedBits {
			t.Errorf("[%v,%v] at %v: bits %d, want %d", shape.min, shape.max, shape.res, bits, shape.expectedBits)
		}
		if delta != shape.max-shape.min {
			t.Errorf("[%v,%v] at %v: delta %v, want %v", shape.min, shape.max, shape.res, delta, shape.max-shape.min)
		}
	}
}

// TestCompressedFloatPrecomputedValidation mirrors the C++
// test_compressed_float_precomputed_validation: the constants
// SerializeCompressedFloat32 derives on every call, derived once instead — the
// precomputed read path must refuse the same smuggled integers and accept the same
// conforming ones as the derive-per-call path.
func TestCompressedFloatPrecomputedValidation(t *testing.T) {
	maxIntegerValue, bits, delta := CompressedFloatParams(0, 10, 0.01)
	if maxIntegerValue != 1000 || bits != 10 || delta != 10.0 {
		t.Fatalf("params for [0,10] at 0.01: got (%d,%d,%v), want (1000,10,10)", maxIntegerValue, bits, delta)
	}

	// a malicious packet can encode integer values above maxIntegerValue in the bit
	// headroom. reads must reject them.
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		outOfRange := uint32(1023) // maxIntegerValue is 1000 for [0,10] at res 0.01 -> 10 bits
		writeStream.SerializeBits(&outOfRange, 10)
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32Precomputed(&value, maxIntegerValue, bits, delta, 0); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}

	// the highest conforming integer still decodes
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		topOfRange := uint32(1000)
		writeStream.SerializeBits(&topOfRange, 10)
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32Precomputed(&value, maxIntegerValue, bits, delta, 0); err != nil {
			t.Fatal(err)
		}
		if value != 10.0 { // 1000 / 1000 * 10 + 0: exact at the top of the range
			t.Fatalf("expected 10.0, got %v", value)
		}
	}

	// a NaN value must quantize into range rather than corrupt the stream. The C++
	// reference debug-asserts a non-finite write; this port's established behavior is
	// the clamp (see SerializeCompressedFloat32Precomputed on WriteStream), and the
	// precomputed entry point inherits it.
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := math.Float32frombits(0x7fc00000) // quiet NaN
		if err := writeStream.SerializeCompressedFloat32Precomputed(&written, maxIntegerValue, bits, delta, 0); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		value := float32(-1.0)
		if err := readStream.SerializeCompressedFloat32Precomputed(&value, maxIntegerValue, bits, delta, 0); err != nil {
			t.Fatal(err)
		}
		if !(value >= 0.0 && value <= 10.0) { // NaN clamps to the low end of the range
			t.Fatalf("expected value in [0,10], got %v", value)
		}
	}
}

// TestCompressedFloatPrecomputedPanics is the Go half of the C++
// test_compressed_float_precomputed_asserts: constants that are not what
// CompressedFloatParams derives are a caller bug — the field would not occupy the
// width every other conforming implementation of the declaration expects — and this
// port panics unconditionally where the C++ library debug-asserts.
func TestCompressedFloatPrecomputedPanics(t *testing.T) {
	expectPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}

	value := float32(5.0)

	// a wire width that disagrees with the step count, on all three streams
	expectPanic("write inconsistent bits", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 11, 10.0, 0)
	})
	expectPanic("read inconsistent bits", func() {
		s := NewReadStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 11, 10.0, 0)
	})
	expectPanic("measure inconsistent bits", func() {
		s := NewMeasureStream()
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 11, 10.0, 0)
	})

	// a zero step count, a non-positive delta, and a non-finite delta
	expectPanic("zero maxIntegerValue", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 0, 0, 10.0, 0)
	})
	expectPanic("zero delta", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 10, 0.0, 0)
	})
	expectPanic("negative delta", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 10, -10.0, 0)
	})
	expectPanic("infinite delta", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 10, float32(math.Inf(1)), 0)
	})
	expectPanic("NaN delta", func() {
		s := NewWriteStream(make([]byte, 8))
		_ = s.SerializeCompressedFloat32Precomputed(&value, 1000, 10, float32(math.NaN()), 0)
	})
}

// precomputedStream is the one method interface a unified serialize function declares
// for itself when it wants the precomputed entry point — the pattern the Stream
// interface documentation points at, exercised here so it is known to work. All three
// concrete streams satisfy it.
type precomputedStream interface {
	SerializeCompressedFloat32Precomputed(value *float32, maxIntegerValue uint32, bits int, delta, min float32) error
	SerializeAlign() error
	Err() error
}

var (
	_ precomputedStream = (*WriteStream)(nil)
	_ precomputedStream = (*ReadStream)(nil)
	_ precomputedStream = (*MeasureStream)(nil)
)

// serializePrecomputedConformance drives the precomputed entry point with the
// constants a schema compiler derives for [-100,100] at resolution 0.01 written as
// literals — exactly what generated code would pass.
func serializePrecomputedConformance(stream precomputedStream, a, b, c *float32) error {
	stream.SerializeCompressedFloat32Precomputed(a, 20000, 15, 200.0, -100.0)
	stream.SerializeCompressedFloat32Precomputed(b, 20000, 15, 200.0, -100.0)
	stream.SerializeCompressedFloat32Precomputed(c, 20000, 15, 200.0, -100.0)
	stream.SerializeAlign()
	return stream.Err()
}

// TestCompressedFloatPrecomputedConformance mirrors the C++
// test_compressed_float_precomputed_conformance: the non-zero-min conformance vector
// through the PRECOMPUTED entry point. Same pinned bytes, same pinned decoded bit
// patterns as the C++ reference — the precomputed path is held to the conformance law
// directly, independently of the differential below.
func TestCompressedFloatPrecomputedConformance(t *testing.T) {
	pinnedBytes := []byte{0x10, 0xA7, 0x06, 0x80, 0x82, 0x06}

	// write side: the precomputed quantization must produce exactly the pinned bytes
	{
		buffer := make([]byte, 64)
		stream := NewWriteStream(buffer)
		a := float32(0.0)
		b := float32(-99.875)
		c := float32(-33.34)
		if err := serializePrecomputedConformance(stream, &a, &b, &c); err != nil {
			t.Fatal(err)
		}
		stream.Flush()
		if got := stream.BytesProcessed(); got != int64(len(pinnedBytes)) {
			t.Fatalf("bytes processed %d, want %d", got, len(pinnedBytes))
		}
		for i, pinned := range pinnedBytes {
			if buffer[i] != pinned {
				t.Fatalf("byte %d is %#02x, want %#02x", i, buffer[i], pinned)
			}
		}
	}

	// read side: the decoded floats are pinned bit-exactly, as in the C++ reference
	{
		stream := NewReadStream(pinnedBytes)
		a := float32(-1.0)
		b := float32(-1.0)
		c := float32(-1.0)
		if err := serializePrecomputedConformance(stream, &a, &b, &c); err != nil {
			t.Fatal(err)
		}
		if bits := math.Float32bits(a); bits != 0x00000000 {
			t.Fatalf("decoded a is %#08x, want 0x00000000", bits)
		}
		if bits := math.Float32bits(b); bits != 0xC2C7BD71 {
			t.Fatalf("decoded b is %#08x, want 0xC2C7BD71", bits)
		}
		if bits := math.Float32bits(c); bits != 0xC2055C2A {
			t.Fatalf("decoded c is %#08x, want 0xC2055C2A", bits)
		}
	}
}

// The mas-bandwidth/schema#82 differential: four implementations must be
// indistinguishable in measured bits, wire bytes, read acceptance and decoded BIT
// PATTERNS on every input:
//
//	frozen      -- the frozenCompressedFloat* functions above, verbatim copies of the
//	               pre-split implementation. FROZEN: they are the code the audited
//	               home replaced, kept so this differential proves the split changed
//	               nothing, forever.
//	legacy      -- SerializeCompressedFloat32(value, min, max, resolution), which
//	               since the split derives the constants per call with
//	               CompressedFloatParams and forwards to the audited home.
//	precomputed -- SerializeCompressedFloat32Precomputed with constants derived once
//	               per declaration, exactly as a schema compiler derives them at
//	               generation time.
//	fold        -- the foldCompressedFloat* functions above: the arithmetic shape the
//	               schema Go emitter has inlined at every call site since
//	               mas-bandwidth/schema#79. This leg is Go-only: it pins the runtime
//	               and the generated code to the same wire from the runtime's side.
//
// Inputs per declaration: a dense sweep with overshoot past both bounds, the
// quantization step edges and midpoints with their one-ulp neighbors (the midpoints
// are where a fused or widened writer diverges — STANDARD.md's 0.005-over-[0,10]
// class), specials (bounds and their one-ulp neighbors, negative zero, subnormals,
// float extremes), non-finite values (this port clamps them into range on write, so
// unlike the C++ differential they run in every build), LCG uniform in-range values
// and LCG uniform float32 bit patterns — and on the read side every representable
// wire integer including the bit headroom, exhaustively up to 16-bit widths and
// sampled with the boundary codes pinned above that. Decoded values compare by BIT
// PATTERN, never by tolerance: the divergence a fused or widened implementation
// produces is one ulp, invisible to any tolerance.

type differential struct {
	t     *testing.T
	count uint64

	// digest absorbs every wire byte and every decoded bit pattern the sweep
	// produces, so the corpus is an ABSOLUTE pin and not only a relative one.
	// See TestCompressedFloatPrecomputedDifferential for why that distinction is
	// the whole point.
	digest hash.Hash
	scalar [8]byte

	shape           *compressedFloatShape
	maxIntegerValue uint32
	bits            int
	delta           float32

	phase string
	value float32
	code  uint32
}

// absorb feeds one observed quantity into the corpus digest. Everything the
// differential compares between implementations is also absorbed here, so a change
// that moves the wire anywhere in the sweep changes the digest even when every
// implementation moves together and all the relative checks stay green.
func (d *differential) absorb(value uint64) {
	binary.LittleEndian.PutUint64(d.scalar[:], value)
	d.digest.Write(d.scalar[:])
}

func (d *differential) check(condition bool) {
	d.count++
	if !condition {
		d.t.Helper()
		d.t.Fatalf("differential check %d failed in phase %q: shape [%v,%v] at %v, value %v (%#08x), code %d",
			d.count, d.phase, d.shape.min, d.shape.max, d.shape.res, d.value, math.Float32bits(d.value), d.code)
	}
}

// checkValue runs one written value through all four implementations: measured bits,
// wire bytes and decoded bit patterns must agree exactly.
func (d *differential) checkValue(value float32) {
	shape := d.shape
	d.value, d.code = value, 0

	// measure: the three runtime forms agree on cost (the fold has no measure form —
	// generated code precomputes MaxBytes at generation time)
	d.phase = "measure"
	measureFrozen := NewMeasureStream()
	d.check(frozenCompressedFloatMeasure(measureFrozen, shape.min, shape.max, shape.res) == nil)
	measureLegacy := NewMeasureStream()
	measureValue := value
	d.check(measureLegacy.SerializeCompressedFloat32(&measureValue, shape.min, shape.max, shape.res) == nil)
	measurePrecomputed := NewMeasureStream()
	measureValue = value
	d.check(measurePrecomputed.SerializeCompressedFloat32Precomputed(&measureValue, d.maxIntegerValue, d.bits, d.delta, shape.min) == nil)
	d.check(measureFrozen.BitsProcessed() == measureLegacy.BitsProcessed())
	d.check(measureFrozen.BitsProcessed() == measurePrecomputed.BitsProcessed())

	// write: byte identical wire from all four
	d.phase = "write"
	bufferFrozen := make([]byte, 8)
	bufferLegacy := make([]byte, 8)
	bufferPrecomputed := make([]byte, 8)
	bufferFold := make([]byte, 8)

	writeFrozen := NewWriteStream(bufferFrozen)
	writeValue := value
	d.check(frozenCompressedFloatWrite(writeFrozen, &writeValue, shape.min, shape.max, shape.res) == nil)
	writeFrozen.Flush()

	writeLegacy := NewWriteStream(bufferLegacy)
	writeValue = value
	d.check(writeLegacy.SerializeCompressedFloat32(&writeValue, shape.min, shape.max, shape.res) == nil)
	writeLegacy.Flush()

	writePrecomputed := NewWriteStream(bufferPrecomputed)
	writeValue = value
	d.check(writePrecomputed.SerializeCompressedFloat32Precomputed(&writeValue, d.maxIntegerValue, d.bits, d.delta, shape.min) == nil)
	writePrecomputed.Flush()

	writeFold := NewWriteStream(bufferFold)
	d.check(foldCompressedFloatWrite(writeFold, value, d.maxIntegerValue, d.bits, d.delta, shape.min) == nil)
	writeFold.Flush()

	d.check(writeFrozen.BitsProcessed() == writeLegacy.BitsProcessed())
	d.check(writeFrozen.BitsProcessed() == writePrecomputed.BitsProcessed())
	d.check(writeFrozen.BitsProcessed() == writeFold.BitsProcessed())
	d.check(writeFrozen.BitsProcessed() == measureFrozen.BitsProcessed())
	for i := range bufferFrozen {
		d.check(bufferFrozen[i] == bufferLegacy[i])
		d.check(bufferFrozen[i] == bufferPrecomputed[i])
		d.check(bufferFrozen[i] == bufferFold[i])
	}

	// read: decoded BIT PATTERNS agree exactly — one ulp of divergence must fail
	d.phase = "read"
	readFrozen := NewReadStream(bufferFrozen)
	var decodedFrozen float32
	d.check(frozenCompressedFloatRead(readFrozen, &decodedFrozen, shape.min, shape.max, shape.res) == nil)
	readLegacy := NewReadStream(bufferFrozen)
	var decodedLegacy float32
	d.check(readLegacy.SerializeCompressedFloat32(&decodedLegacy, shape.min, shape.max, shape.res) == nil)
	readPrecomputed := NewReadStream(bufferFrozen)
	var decodedPrecomputed float32
	d.check(readPrecomputed.SerializeCompressedFloat32Precomputed(&decodedPrecomputed, d.maxIntegerValue, d.bits, d.delta, shape.min) == nil)
	readFold := NewReadStream(bufferFrozen)
	var decodedFold float32
	d.check(foldCompressedFloatRead(readFold, &decodedFold, d.maxIntegerValue, d.bits, d.delta, shape.min) == nil)

	d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedLegacy))
	d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedPrecomputed))
	d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedFold))

	// absolute anchor: the input, the measured width, the wire it produced and the
	// value it decoded back to. The four legs have just been asserted identical, so
	// absorbing the frozen leg absorbs all of them.
	d.absorb(uint64(math.Float32bits(value)))
	d.absorb(uint64(measureFrozen.BitsProcessed()))
	for _, b := range bufferFrozen {
		d.absorb(uint64(b))
	}
	d.absorb(uint64(math.Float32bits(decodedFrozen)))
}

// checkCode runs one wire integer through all four read paths: acceptance must agree
// (the headroom refusal, with the refusal error pinned per implementation), and
// accepted codes must decode to identical bit patterns.
func (d *differential) checkCode(code uint32) {
	shape := d.shape
	d.value, d.code, d.phase = 0, code, "code"

	buffer := make([]byte, 8)
	writeStream := NewWriteStream(buffer)
	raw := code
	d.check(writeStream.SerializeBits(&raw, d.bits) == nil)
	writeStream.Flush()

	readFrozen := NewReadStream(buffer)
	var decodedFrozen float32
	errFrozen := frozenCompressedFloatRead(readFrozen, &decodedFrozen, shape.min, shape.max, shape.res)

	readLegacy := NewReadStream(buffer)
	var decodedLegacy float32
	errLegacy := readLegacy.SerializeCompressedFloat32(&decodedLegacy, shape.min, shape.max, shape.res)

	readPrecomputed := NewReadStream(buffer)
	var decodedPrecomputed float32
	errPrecomputed := readPrecomputed.SerializeCompressedFloat32Precomputed(&decodedPrecomputed, d.maxIntegerValue, d.bits, d.delta, shape.min)

	readFold := NewReadStream(buffer)
	var decodedFold float32
	errFold := foldCompressedFloatRead(readFold, &decodedFold, d.maxIntegerValue, d.bits, d.delta, shape.min)

	accepted := code <= d.maxIntegerValue // the headroom refusal itself
	d.check((errFrozen == nil) == accepted)
	d.check((errLegacy == nil) == accepted)
	d.check((errPrecomputed == nil) == accepted)
	d.check((errFold == nil) == accepted)

	// absolute anchor: the code, whether it was accepted, and what it decoded to
	d.absorb(uint64(code))
	d.absorb(uint64(d.bits))
	if accepted {
		d.absorb(1)
		d.absorb(uint64(math.Float32bits(decodedFrozen)))
	} else {
		d.absorb(0)
	}

	if accepted {
		d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedLegacy))
		d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedPrecomputed))
		d.check(math.Float32bits(decodedFrozen) == math.Float32bits(decodedFold))
	} else {
		d.check(errors.Is(errFrozen, ErrValueOutOfRange))
		d.check(errors.Is(errLegacy, ErrValueOutOfRange))
		d.check(errors.Is(errPrecomputed, ErrValueOutOfRange))
		d.check(errors.Is(errFold, errFoldValidation))
	}
}

func (d *differential) runShape(shape *compressedFloatShape, sweepSteps, lcgRounds, exhaustiveReadBits int, lcg *uint64) {
	next := func() uint64 {
		*lcg = *lcg*6364136223846793005 + 1442695040888963407
		return *lcg
	}

	d.shape = shape
	d.value, d.code = 0, 0

	maxIntegerValue, bits, delta := CompressedFloatParams(shape.min, shape.max, shape.res)
	d.maxIntegerValue, d.bits, d.delta = maxIntegerValue, bits, delta

	// the derived constants are pinned against the schema compiler's generation-time table
	d.phase = "params"
	d.check(maxIntegerValue == shape.expectedMaxIntegerValue)
	d.check(bits == shape.expectedBits)
	d.check(delta == shape.max-shape.min)

	// absolute anchor: the declaration and the constants derived from it
	d.absorb(uint64(math.Float32bits(shape.min)))
	d.absorb(uint64(math.Float32bits(shape.max)))
	d.absorb(uint64(math.Float32bits(shape.res)))
	d.absorb(uint64(maxIntegerValue))
	d.absorb(uint64(bits))
	d.absorb(uint64(math.Float32bits(delta)))

	dmin := float64(shape.min)
	ddelta := float64(delta)

	// dense sweep with overshoot a quarter of the range past both bounds
	{
		lo := dmin - 0.25*ddelta
		span := 1.5 * ddelta
		for i := 0; i <= sweepSteps; i++ {
			d.checkValue(float32(lo + span*float64(i)/float64(sweepSteps)))
		}
	}

	// quantization step edges and midpoints, with their one-ulp neighbors. the
	// midpoints are the discriminating band: 0.005 over [0,10] at 0.01 quantizes to 1
	// under the required two roundings and to 0 under a fused or widened writer
	// (STANDARD.md)
	{
		stride := uint64(maxIntegerValue)/512 + 1
		for k := uint64(0); k <= uint64(maxIntegerValue); k += stride { // 64 bit: k += stride must not wrap at the 2^32-clamped shape
			onQuantum := float32(dmin + ddelta*(float64(k)/float64(maxIntegerValue)))
			midpoint := float32(dmin + ddelta*((float64(k)+0.5)/float64(maxIntegerValue)))
			d.checkValue(onQuantum)
			d.checkValue(math.Nextafter32(onQuantum, -math.MaxFloat32))
			d.checkValue(math.Nextafter32(onQuantum, math.MaxFloat32))
			d.checkValue(midpoint)
			d.checkValue(math.Nextafter32(midpoint, -math.MaxFloat32))
			d.checkValue(math.Nextafter32(midpoint, math.MaxFloat32))
		}
	}

	// specials: the bounds and their one-ulp neighbors, both zeros, subnormals, extremes
	{
		specials := []float32{
			shape.min,
			shape.max,
			math.Nextafter32(shape.min, -math.MaxFloat32),
			math.Nextafter32(shape.min, math.MaxFloat32),
			math.Nextafter32(shape.max, -math.MaxFloat32),
			math.Nextafter32(shape.max, math.MaxFloat32),
			shape.min - shape.res,
			shape.max + shape.res,
			shape.min + shape.res*0.5,
			shape.max - shape.res*0.5,
			shape.min - delta,
			shape.max + delta,
			0.0,
			float32(math.Copysign(0, -1)),
			shape.res,
			-shape.res,
			math.MaxFloat32,
			-math.MaxFloat32,
			1.175494351e-38, // the smallest normal float32
			-1.175494351e-38,
			1.401298464e-45, // the smallest subnormal
			-1.401298464e-45,
			1.0e30,
			-1.0e30,
		}
		for _, special := range specials {
			d.checkValue(special)
		}
	}

	// non-finite inputs: NaN and both infinities. The C++ reference can only drive
	// these in release builds (debug asserts); this port's established behavior is
	// the clamp, in every build, so the differential drives them unconditionally —
	// all four implementations must force them to the same wire.
	{
		nonFinitePatterns := []uint32{0x7F800000, 0xFF800000, 0x7FC00000, 0x7F800001, 0xFFC00001}
		for _, pattern := range nonFinitePatterns {
			d.checkValue(math.Float32frombits(pattern))
		}
	}

	// LCG uniform values across the range and its overshoot band
	for range lcgRounds {
		fraction := float64(next()>>11) * (1.0 / 9007199254740992.0) // [0,1) in 53 bits
		d.checkValue(float32(dmin - 0.25*ddelta + fraction*1.5*ddelta))
	}

	// LCG uniform float32 bit patterns, finite and non-finite alike (the clamp is
	// total in this port)
	for range lcgRounds {
		d.checkValue(math.Float32frombits(uint32(next() >> 32)))
	}

	// the read side: every representable wire integer, including the bit headroom a
	// malicious packet can encode into. exhaustive up to exhaustiveReadBits widths;
	// above that the boundary codes are pinned and the interior is sampled
	{
		topCode := uint32(0xFFFFFFFF)
		if bits < 32 {
			topCode = uint32(1)<<bits - 1
		}
		if bits <= exhaustiveReadBits {
			for code := uint64(0); code <= uint64(topCode); code++ {
				d.checkCode(uint32(code))
			}
		} else {
			for code := uint32(0); code <= 1024; code++ {
				d.checkCode(code)
			}
			windowLo := maxIntegerValue - 512 // maxIntegerValue >= 2^12 on every sampled shape, so no underflow
			windowHi := maxIntegerValue + 512
			if topCode-maxIntegerValue < 512 {
				windowHi = topCode
			}
			for code := windowLo; code <= windowHi && code >= windowLo; code++ {
				d.checkCode(code)
			}
			for code := topCode - 64; code <= topCode && code >= topCode-64; code++ {
				d.checkCode(code)
			}
			for range lcgRounds {
				d.checkCode(uint32(next()>>32) & topCode)
			}
		}
	}
}

// corpusDigest is the SHA-256 of the whole differential corpus: for every declaration
// its derived constants, and for every swept value and every wire code the measured
// width, the emitted wire bytes and the decoded bit pattern. It is a GOLDEN value —
// the wire this library produces for nine million inputs, in one constant.
//
// It exists because a differential is a RELATIVE instrument and cannot see a change
// applied uniformly to everything it compares. Removing the load-bearing float32()
// conversion from the audited home alone fails this test at check 2,091,027; removing
// it from the audited home AND the frozen leg AND the fold leg together passes all
// 9,350,235 checks on arm64 while the wire silently changes — the two roundings
// STANDARD.md requires become one, and 0.005 over [0,10] at 0.01 starts writing 0
// instead of 1. That is the Go form of the trap the C++ reference leg found at
// -ffp-contract=fast (mas-bandwidth/schema#82): a differential can be green while the
// arithmetic is wrong. The digest closes it — being an absolute value rather than a
// comparison, it moves the moment the wire moves, however many legs moved with it.
//
// It is also a cross-architecture wire pin. The wire format is frozen and identical on
// every target (invariant 1), so this constant must be the same on arm64, amd64, 386,
// s390x and wasm; the CI cross job runs the suite under GOARCH=386, and a target whose
// float arithmetic diverged would fail here rather than in production. arm64 is the
// architecture that matters most: gc contracts float multiply-add into FMA there and
// not on amd64, so the identical falsification above passes every test in this repo
// when built for amd64.
//
// To re-pin after a DELIBERATE corpus change (a new declaration, a different sweep
// density): confirm the change is to the corpus and not to the wire, then update this
// constant to the value the failure reports. Never re-pin to make a red test green
// without establishing which of the two moved.
//
// Re-pinned 2026-08-23 for the two [2^23, 2^24) declarations added with the normative
// integer clamp (schema#109): the suite was first run green with the clamp in the
// audited home and the old corpus — the unclamped frozen and fold legs agreeing with
// the clamped runtime on every old shape proves the clamp moved no pre-existing byte —
// and only then were the new rows added and this constant re-pinned.
const corpusDigest = "2f87bcb49e58df5d380a23a38d62c2bc512788b2ec4e3e956ab95b3876ff92ec"

func TestCompressedFloatPrecomputedDifferential(t *testing.T) {
	// the reference's sweep density and the exhaustive read side through 16-bit
	// widths, in every mode: the whole corpus runs in about two seconds even under
	// the race detector, so unlike the 320MB buffer test there is nothing for -short
	// to skip
	const sweepSteps = 2048
	const lcgRounds = 2048
	const exhaustiveReadBits = 16
	const minimumChecks = 5000000

	lcg := uint64(0xC0FFEE1234567890) // fixed seed: failures reproduce

	d := &differential{t: t, digest: sha256.New()}
	for i := range compressedFloatShapes {
		d.runShape(&compressedFloatShapes[i], sweepSteps, lcgRounds, exhaustiveReadBits, &lcg)
	}

	// the coverage floor: if the differential ever silently shrinks below the mass it
	// was built with, that is a test bug, and it fails here instead of fading quietly
	if d.count < minimumChecks {
		t.Fatalf("differential ran %d checks, the floor is %d", d.count, minimumChecks)
	}

	// the absolute anchor: the relative checks above prove the four implementations
	// agree, and this proves what they agree ON has not moved
	if got := hex.EncodeToString(d.digest.Sum(nil)); got != corpusDigest {
		t.Fatalf("compressed float corpus digest is %s, want %s\n"+
			"the four implementations still agree with each other, so this is a change to the WIRE, "+
			"not to one implementation: every conforming runtime in the family disagrees with this build. "+
			"See the corpusDigest doc comment before re-pinning.", got, corpusDigest)
	}

	t.Logf("%d checks, four implementations, %d declarations, corpus digest %s",
		d.count, len(compressedFloatShapes), corpusDigest)
}
