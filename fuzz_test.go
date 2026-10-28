package serialize

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
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
		_ = stream.SerializeIntRelative(100, &relative)
		var str string
		_ = stream.SerializeString(&str, 300)
		_ = stream.SerializeAlign()
		buffer := make([]byte, 33)
		_ = stream.SerializeBytes(buffer)
		// floats off hostile bytes are finite or refused: the format carries no NaN
		// and no infinity, whatever the bytes say
		var f32 float32
		if stream.SerializeCompressedFloat32(&f32, -1, 1, 0.001) == nil && !float32BitsAreFinite(math.Float32bits(f32)) {
			t.Fatalf("compressed float decoded a non-finite value: %#x", math.Float32bits(f32))
		}
		var f64 float64
		if stream.SerializeFloat64(&f64) == nil && !float64BitsAreFinite(math.Float64bits(f64)) {
			t.Fatalf("double decoded a non-finite value: %#x", math.Float64bits(f64))
		}

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

		_ = stream.SerializeEnd()

		// every refusal names its rule from the closed list
		if err := stream.Err(); err != nil {
			var refusal Refusal
			if !errors.As(err, &refusal) || refusal < ErrPastEnd || refusal > ErrRejected {
				t.Fatalf("the stream refused with %v, which names no reason", err)
			}
		}
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
		// a value wider than its field is write misuse since 2.1 (IMPLEMENTING.md U8),
		// so the harness writes the fuzzed values masked to their widths
		aField := a & uint32(uint64(1)<<abits-1)
		bField := b & (uint64(1)<<bbits - 1)

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

		// the round trip serializes str with SerializeString, whose payload is
		// well-formed UTF-8 without interior NUL — the writer's contract, and since
		// the reader-validation ruling refused on read. The fuzzer explores arbitrary
		// bytes, so canonicalize to a conforming payload here: contract-violating
		// writes are undefined by STANDARD.md and arbitrary bytes belong to
		// SerializeBytes, which this harness fuzzes via bulk.
		str = strings.ToValidUTF8(str, "�")
		str = strings.ReplaceAll(str, "\x00", "")
		if len(str) > 255 {
			cut := 255
			for cut > 0 && !utf8.RuneStart(str[cut]) {
				cut-- // don't split a multibyte rune at the truncation point
			}
			str = str[:cut]
		}
		if len(bulk) > 512 {
			bulk = bulk[:512]
		}

		// float and double carry finite values only (format 2.0): NaN and the
		// infinities are write misuse and trap (IMPLEMENTING.md U9), which is checked
		// here, and the round trip carries the finite part of the fuzzer's space bit
		// for bit. The compressed float traps on them too.
		if !float32BitsAreFinite(math.Float32bits(f32)) {
			probe := NewWriteStream(make([]byte, 8))
			if !panics(func() { probe.SerializeFloat32(&f32) }) {
				t.Fatalf("the writer accepted the non-finite float %#x", math.Float32bits(f32))
			}
			if !panics(func() { probe.SerializeCompressedFloat32(&f32, -1000, 1000, 0.01) }) {
				t.Fatalf("the writer accepted the non-finite compressed float %#x", math.Float32bits(f32))
			}
			f32 = 0
		}
		if !float64BitsAreFinite(math.Float64bits(f64)) {
			probe := NewWriteStream(make([]byte, 8))
			if !panics(func() { probe.SerializeFloat64(&f64) }) {
				t.Fatalf("the writer accepted the non-finite double %#x", math.Float64bits(f64))
			}
			f64 = 0
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

		// the operations the first harness missed (audit ledger, go L5), derived from the
		// same arguments: int64 over a fuzzed range, the fixed width unsigned integers,
		// degenerate ranges at 32 and 64 bits, int_relative across every tier, and a
		// compressed float over a fixed declaration
		min64, max64 := int64(b), int64(b)^int64(a64(a))
		if min64 > max64 {
			min64, max64 = max64, min64
		}
		value64 := min64 + int64((uint64(c)*0x9E3779B97F4A7C15)%(uint64(max64-min64)+1))
		if uint64(max64-min64) == math.MaxUint64 {
			value64 = int64(b ^ uint64(d))
		}
		u8, u16, u32, u64 := uint8(a), uint16(b), uint32(d), b^0xC2B2AE3D27D4EB4F
		previous := c & math.MaxInt32
		if previous == math.MaxInt32 {
			previous--
		}
		current := previous + 1 + int32(uint32(d)%uint32(math.MaxInt32-previous))
		cfValue := f32
		if !float32BitsAreFinite(math.Float32bits(cfValue)) {
			cfValue = 0
		}
		const cfMin, cfMax, cfRes = float32(-1000), float32(1000), float32(0.01)

		measureStream := NewMeasureStream()
		writeStream := NewWriteStream(buffer)
		for _, stream := range []Stream{measureStream, writeStream} {
			v64, w8, w16, w32, w64, cur, cf := value64, u8, u16, u32, u64, current, cfValue
			stream.SerializeInt64(&v64, min64, max64)
			stream.SerializeUint8(&w8)
			stream.SerializeUint16(&w16)
			stream.SerializeUint32(&w32)
			stream.SerializeUint64(&w64)
			degenerate, degenerate64 := c, value64
			stream.SerializeInt(&degenerate, c, c)
			stream.SerializeInt64(&degenerate64, value64, value64)
			stream.SerializeIntRelative(previous, &cur)
			stream.SerializeCompressedFloat32(&cf, cfMin, cfMax, cfRes)
		}
		if err := measureStream.Err(); err != nil {
			t.Fatalf("measure failed: %v", err)
		}
		if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
			t.Fatalf("measure %d bits, write %d bits for the same unaligned fields", measureStream.BitsProcessed(), writeStream.BitsProcessed())
		}
		writeStream.SerializeBits(&aField, abits)
		writeStream.SerializeBits64(&bField, bbits)
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
		var r64 int64
		var r8 uint8
		var r16 uint16
		var r32 uint32
		var ru64 uint64
		var rDegenerate int32
		var rDegenerate64 int64
		var rCurrent int32
		var rcf float32
		readStream.SerializeInt64(&r64, min64, max64)
		readStream.SerializeUint8(&r8)
		readStream.SerializeUint16(&r16)
		readStream.SerializeUint32(&r32)
		readStream.SerializeUint64(&ru64)
		readStream.SerializeInt(&rDegenerate, c, c)
		readStream.SerializeInt64(&rDegenerate64, value64, value64)
		readStream.SerializeIntRelative(previous, &rCurrent)
		readStream.SerializeCompressedFloat32(&rcf, cfMin, cfMax, cfRes)
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
		// the stream holds exactly what was written, so its end must accept
		readStream.SerializeEnd()
		if err := readStream.Err(); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		if r64 != value64 || r8 != u8 || r16 != u16 || r32 != u32 || ru64 != u64 {
			t.Fatalf("int64/uintN mismatch: wrote %d %d %d %d %d, read %d %d %d %d %d", value64, u8, u16, u32, u64, r64, r8, r16, r32, ru64)
		}
		if rDegenerate != c || rDegenerate64 != value64 {
			t.Fatalf("degenerate mismatch: wrote %d %d, read %d %d", c, value64, rDegenerate, rDegenerate64)
		}
		if rCurrent != current {
			t.Fatalf("int_relative mismatch: previous %d, wrote %d, read %d", previous, current, rCurrent)
		}
		clamped := cfValue
		if clamped < cfMin {
			clamped = cfMin
		} else if clamped > cfMax {
			clamped = cfMax
		}
		if math.Abs(float64(rcf-clamped)) > float64(cfRes) {
			t.Fatalf("compressed float mismatch: wrote %v (clamped %v), read %v", cfValue, clamped, rcf)
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
