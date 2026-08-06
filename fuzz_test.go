package serialize

import (
	"bytes"
	"math"
	"testing"
)

// FuzzReadStream feeds arbitrary bytes to a read stream and exercises every serialize
// method. Reads of malicious packet data must fail with errors, never panic.
func FuzzReadStream(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF})
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Add(goldenWireBytes)

	f.Fuzz(func(t *testing.T, data []byte) {
		stream := NewReadStream(data)
		var golden goldenWireData
		_ = goldenWireSerialize(stream, &golden)

		stream.Reset(data)
		var u32 uint32
		_ = stream.SerializeBits(&u32, 7)
		var u64 uint64
		_ = stream.SerializeBits64(&u64, 33)
		var i32 int32
		_ = stream.SerializeInt(&i32, -5, 100)
		var i64 int64
		_ = stream.SerializeInt64(&i64, math.MinInt64, math.MaxInt64)
		var relative int32
		_ = stream.SerializeIntRelative(-10, &relative)
		var str string
		_ = stream.SerializeString(&str, 300)
		var wstr string
		_ = stream.SerializeWideString(&wstr, 300)
		_ = stream.SerializeAlign()
		buffer := make([]byte, 33)
		_ = stream.SerializeBytes(buffer)
		var f32 float32
		_ = stream.SerializeCompressedFloat32(&f32, -1, 1, 0.001)
		var f64 float64
		_ = stream.SerializeFloat64(&f64)

		// 128 bit and fixed point reads off hostile bytes must decode within their
		// bounds or fail — never clamp, never wrap, never panic
		var v128 Uint128
		_ = stream.SerializeUint128(&v128)

		var fixed32 int64
		if stream.SerializeFixed64(&fixed32, 16, 16, -1000, +1000) == nil {
			if fixed32 < -1000*65536 || fixed32 > 1000*65536 {
				t.Fatalf("fixed64 q16.16 decoded out of bounds: %d", fixed32)
			}
		}

		var fixed64 int64
		if stream.SerializeFixed64(&fixed64, 48, 16, -100000000000, +100000000000) == nil {
			if fixed64 < -100000000000*65536 || fixed64 > 100000000000*65536 {
				t.Fatalf("fixed64 q48.16 decoded out of bounds: %d", fixed64)
			}
		}

		var wide Int128
		if stream.SerializeFixed128(&wide, 112, 16, -1152921504606846976, +1152921504606846976) == nil { // ±2^60 units: a raw range past 64 bits
			rawBound := Int128From64(1152921504606846976).Lsh(16)
			if wide.Cmp(rawBound.Neg()) < 0 || wide.Cmp(rawBound) > 0 {
				t.Fatalf("fixed128 q112.16 decoded out of bounds: %+v", wide)
			}
		}

		// ranged 128 bit integers, with the bound width varying by the first data
		// byte so the corpus walks every group structure: one group, two, three and
		// four. Hostile bytes must decode inside the bounds or fail the read.
		param := 0
		if len(data) > 0 {
			param = int(data[0])
		}
		bound128 := Int128From64(1).Lsh(uint(20 + param%100))
		var ranged128 Int128
		if stream.SerializeInt128(&ranged128, bound128.Neg(), bound128) == nil {
			if ranged128.Cmp(bound128.Neg()) < 0 || ranged128.Cmp(bound128) > 0 {
				t.Fatalf("int128 decoded out of bounds: %+v", ranged128)
			}
		}

		var full128 Int128
		_ = stream.SerializeInt128(&full128,
			Uint128From64(1).Lsh(127).Int128(),       // MinInt128
			Uint128From64(1).Lsh(127).Not().Int128()) // MaxInt128
	})
}

// FuzzRoundTrip writes fuzzed values and reads them back, verifying they survive the
// trip exactly.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint32(0), uint8(0), uint64(0), uint8(0), int32(0), int32(0), "", []byte{}, false, float32(0), 0.0)
	f.Add(uint32(0xDEADBEEF), uint8(31), uint64(0x123456789ABCDEF0), uint8(63), int32(-1000), int32(1000),
		"hello world", []byte{1, 2, 3}, true, float32(3.14159), 1.0/3.0)

	f.Fuzz(func(t *testing.T, a uint32, aBits uint8, b uint64, bBits uint8, c int32, d int32,
		str string, bulk []byte, flag bool, f32 float32, f64 float64) {

		abits := int(aBits)%32 + 1
		bbits := int(bBits)%64 + 1

		min, max := c, d
		if min > max {
			min, max = max, min
		}
		if min == max {
			if max == math.MaxInt32 {
				min--
			} else {
				max++
			}
		}

		if len(str) > 255 {
			str = str[:255]
		}
		if len(bulk) > 512 {
			bulk = bulk[:512]
		}

		buffer := make([]byte, 2048)

		// 128 bit and fixed point operands are derived from the fuzzed args rather
		// than widening the fuzz signature, which would orphan the persisted corpus.
		// Raw fixed point values are placed uniformly inside the raw bounds.
		value128 := Uint128{Lo: b * 0x9E3779B97F4A7C15, Hi: b ^ a64(a)}
		const fixed64Range = uint64(200000000000)*65536 + 1 // bounds ±1e11 whole units in Q48.16
		expectedFixed64 := -100000000000*65536 + int64(b%fixed64Range)
		wideRange := Uint128From64(1152921504606846976).Lsh(17) // ( 2 * 2^60 ) << 16: bounds ±2^60 whole units in Q112.16
		wideRaw := Uint128{Lo: b * 0x2545F4914F6CDD1D, Hi: b ^ 0xD1342543DE82EF95}.Mod(wideRange.Add(Uint128From64(1)))
		expectedWide := Int128From64(-1152921504606846976).Lsh(16).Add(wideRaw.Int128())
		int128Bound := Int128From64(1).Lsh(uint(20 + int(aBits)%100))
		expectedInt128 := int128Bound.Neg().Add(
			Uint128{Lo: b ^ 0xA0761D6478BD642F, Hi: uint64(a)}.Mod(int128Bound.Uint128().Lsh(1).Add(Uint128From64(1))).Int128())

		writeStream := NewWriteStream(buffer)
		writeStream.SerializeBits(&a, abits)
		writeStream.SerializeBits64(&b, bbits)
		writeStream.SerializeInt(&c, min, max)
		writeStream.SerializeBool(&flag)
		writeStream.SerializeFloat32(&f32)
		writeStream.SerializeFloat64(&f64)
		writeStream.SerializeString(&str, 256)
		writeStream.SerializeBytes(bulk)
		w128 := value128
		writeStream.SerializeUint128(&w128)
		wFixed64 := expectedFixed64
		writeStream.SerializeFixed64(&wFixed64, 48, 16, -100000000000, +100000000000)
		wWide := expectedWide
		writeStream.SerializeFixed128(&wWide, 112, 16, -1152921504606846976, +1152921504606846976)
		wInt128 := expectedInt128
		writeStream.SerializeInt128(&wInt128, int128Bound.Neg(), int128Bound)
		if err := writeStream.Err(); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		writeStream.Flush()

		readStream := NewReadStream(writeStream.Data())
		var ra uint32
		var rb uint64
		var rc int32
		var rstr string
		rbulk := make([]byte, len(bulk))
		var rflag bool
		var rf32 float32
		var rf64 float64
		readStream.SerializeBits(&ra, abits)
		readStream.SerializeBits64(&rb, bbits)
		readStream.SerializeInt(&rc, min, max)
		readStream.SerializeBool(&rflag)
		readStream.SerializeFloat32(&rf32)
		readStream.SerializeFloat64(&rf64)
		readStream.SerializeString(&rstr, 256)
		readStream.SerializeBytes(rbulk)
		var r128 Uint128
		readStream.SerializeUint128(&r128)
		var rFixed64 int64
		readStream.SerializeFixed64(&rFixed64, 48, 16, -100000000000, +100000000000)
		var rWide Int128
		readStream.SerializeFixed128(&rWide, 112, 16, -1152921504606846976, +1152921504606846976)
		var rInt128 Int128
		readStream.SerializeInt128(&rInt128, int128Bound.Neg(), int128Bound)
		if err := readStream.Err(); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		if ra != a&uint32(uint64(1)<<abits-1) {
			t.Fatalf("bits mismatch: wrote %#x (%d bits), read %#x", a, abits, ra)
		}
		if rb != b&(uint64(1)<<bbits-1) {
			t.Fatalf("bits64 mismatch: wrote %#x (%d bits), read %#x", b, bbits, rb)
		}
		if rc != c {
			t.Fatalf("int mismatch: wrote %d in [%d,%d], read %d", c, min, max, rc)
		}
		if rflag != flag {
			t.Fatal("bool mismatch")
		}
		if math.Float32bits(rf32) != math.Float32bits(f32) {
			t.Fatalf("float32 mismatch: wrote %x, read %x", math.Float32bits(f32), math.Float32bits(rf32))
		}
		if math.Float64bits(rf64) != math.Float64bits(f64) {
			t.Fatalf("float64 mismatch: wrote %x, read %x", math.Float64bits(f64), math.Float64bits(rf64))
		}
		if rstr != str {
			t.Fatalf("string mismatch: wrote %q, read %q", str, rstr)
		}
		if !bytes.Equal(rbulk, bulk) {
			t.Fatalf("bytes mismatch: wrote %x, read %x", bulk, rbulk)
		}
		if r128 != value128 {
			t.Fatalf("uint128 mismatch: wrote %+v, read %+v", value128, r128)
		}
		if rFixed64 != expectedFixed64 {
			t.Fatalf("fixed64 mismatch: wrote %d, read %d", expectedFixed64, rFixed64)
		}
		if rWide != expectedWide {
			t.Fatalf("fixed128 mismatch: wrote %+v, read %+v", expectedWide, rWide)
		}
		if rInt128 != expectedInt128 {
			t.Fatalf("int128 mismatch: wrote %+v, read %+v", expectedInt128, rInt128)
		}
	})
}

// a64 widens a fuzzed uint32 for operand mixing.
func a64(a uint32) uint64 {
	return uint64(a) | uint64(a)<<32
}
