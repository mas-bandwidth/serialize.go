package serialize

import (
	"math"
	"testing"
)

// STANDARD.md: compressed_float arithmetic is float32 with TWO roundings on
// each side -- the writer's product rounds before 0.5 is added, the reader's
// product rounds before min is added. Go permits contracting a float multiply
// and add into a single FMA unless a conversion forces the intermediate
// rounding, and the arm64 backend takes that permission, so both
// SerializeCompressedFloat32 implementations carry a load-bearing float32()
// conversion. These tests pin values that DISCRIMINATE: every value pinned
// anywhere else in the family lands exactly on a quantum, and an on-quantum
// value agrees under float32, double and FMA alike -- which is how the arm64
// writer divergence shipped in v1.8.0 with every suite green. The CI matrix
// runs this file on Apple Silicon (macos-latest), so a reintroduced
// contraction or widening goes red in CI, not in production.
//
// Verified to fail before the fixes they guard: with the writer's conversion
// removed, TestCompressedFloatWriterTwoRoundings fails on arm64 (0.005 writes
// 0, not 1); with the reader's conversion removed,
// TestCompressedFloatReaderTwoRoundings fails on arm64 (integer 384 decodes
// to -96.159996, not -96.160004).

// TestCompressedFloatWriterTwoRoundings pins the quantized integers from
// STANDARD.md's own vector table over [0,10] at resolution 0.01. The value
// 0.005 discriminates contraction (an FMA writes 0); the others discriminate
// widening (double arithmetic writes 2, 10 and 999); 2.5 is the on-quantum
// anchor every arithmetic agrees on.
func TestCompressedFloatWriterTwoRoundings(t *testing.T) {
	cases := []struct {
		value   float32
		integer uint32
	}{
		{0.005, 1},    // half a quantum above min: FMA rounds once and writes 0
		{0.025, 3},    // double arithmetic writes 2
		{0.105, 11},   // double arithmetic writes 10
		{9.995, 1000}, // double arithmetic writes 999
		{2.5, 250},    // on-quantum anchor: agrees under every arithmetic
	}
	for _, c := range cases {
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		value := c.value
		if err := writeStream.SerializeCompressedFloat32(&value, 0, 10, 0.01); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		// read the raw quantized integer back: [0,10] at 0.01 gives
		// max_integer_value 1000, which takes 10 bits
		readStream := NewReadStream(buffer)
		var integer uint32
		if err := readStream.SerializeBits(&integer, 10); err != nil {
			t.Fatal(err)
		}
		if integer != c.integer {
			t.Errorf("value %v over [0,10] at 0.01: wrote integer %d, STANDARD.md requires %d",
				c.value, integer, c.integer)
		}
	}
}

// TestCompressedFloatReaderTwoRoundings pins the exact decoded float32 for a
// case where contraction changes the answer. Over [-100,100] at resolution
// 0.01 (max_integer_value 20000, 15 bits), integer 384 must decode to
// float32(float32(384/20000) * 200) + (-100) = -0x1.80a3d8p+06 (-96.160004);
// a fused decode gives -0x1.80a3d6p+06 (-96.159996), one bit off in the
// mantissa and a different answer than every two-rounding runtime. A zero min
// cannot catch this -- adding zero is exact -- so the range here is the point.
func TestCompressedFloatReaderTwoRoundings(t *testing.T) {
	buffer := make([]byte, 8)

	writeStream := NewWriteStream(buffer)
	integer := uint32(384)
	if err := writeStream.SerializeBits(&integer, 15); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()

	readStream := NewReadStream(buffer)
	value := float32(0)
	if err := readStream.SerializeCompressedFloat32(&value, -100, 100, 0.01); err != nil {
		t.Fatal(err)
	}
	const want = float32(-0x1.80a3d8p+06)
	if value != want {
		t.Errorf("integer 384 over [-100,100] at 0.01 decoded to %v (%#08x), want %v (%#08x)",
			value, math.Float32bits(value), want, math.Float32bits(want))
	}
}
