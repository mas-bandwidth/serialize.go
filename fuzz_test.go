package serialize

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

type fuzzControl int

const (
	fuzzControlNone        fuzzControl = iota
	fuzzControlSentinel                // 1: sentinel: a refused scalar read writes its destination
	fuzzControlHostile                 // 2: hostile: a range check is tightened below what the reader accepts
	fuzzControlTerminality             // 3: terminality: a post-refusal read fails to preserve refusal/cursor
	fuzzControlRoundTrip               // 4: roundtrip: a round-trip value is expected to differ from what was written
	fuzzControlCanary                  // 5: canary: a byte past the tight write buffer is overwritten
	fuzzControlTrailing                // 6: trailing: the trailing-data refusal is expected to accept
)

type fuzzTB interface {
	Fatalf(format string, args ...any)
}

func fuzzCheck(t fuzzTB, pass string, cond bool, format string, args ...any) {
	if !cond {
		msg := fmt.Sprintf(format, args...)
		t.Fatalf("fuzz_check failed: pass %s: %s", pass, msg)
	}
}

func repeatByte32(b byte) uint32 {
	v := uint32(b)
	return v | v<<8 | v<<16 | v<<24
}

func repeatByte64(b byte) uint64 {
	v := uint64(repeatByte32(b))
	return v | v<<32
}

const fuzzReadSeqNumOps = 23

// runFuzzReadSentinel runs the hostile read sequence with every scalar destination
// pre-filled with a sentinel derived from pattern (0x5A or 0xA5). A refused scalar
// read must leave its destination untouched (STANDARD.md), and once the stream has
// refused, every subsequent operation must refuse with the same latched Refusal,
// advance the cursor by 0 bits, and leave its scalar destination untouched.
// It returns the index (0..fuzzReadSeqNumOps-1) of the first operation that refused,
// or fuzzReadSeqNumOps if every operation in the sequence accepted.
func runFuzzReadSentinel(t fuzzTB, data []byte, pattern byte, ctl fuzzControl) (int, Refusal) {
	stream := NewReadStream(data)
	var firstRefusal Refusal
	var bitsAtRefusal int64
	firstFailedIdx := fuzzReadSeqNumOps
	opIdx := 0

	checkOp := func(err error, unchanged bool) {
		idx := opIdx
		opIdx++
		if firstRefusal != 0 {
			gotBits := stream.BitsProcessed()
			if ctl == fuzzControlTerminality {
				gotBits++ // NEGATIVE CONTROL
			}
			fuzzCheck(t, "terminality", err != nil, "operation %d succeeded after refusal %v", idx, firstRefusal)
			fuzzCheck(t, "terminality", errors.Is(err, firstRefusal), "operation %d returned %v, want latched %v", idx, err, firstRefusal)
			fuzzCheck(t, "terminality", errors.Is(stream.Err(), firstRefusal), "stream.Err() is %v, want latched %v", stream.Err(), firstRefusal)
			fuzzCheck(t, "terminality", gotBits == bitsAtRefusal, "cursor moved from %d to %d after refusal", bitsAtRefusal, gotBits)
			fuzzCheck(t, "sentinel", unchanged, "refused operation %d on failed stream modified scalar destination (pattern %#02x)", idx, pattern)
			return
		}
		if err == nil {
			fuzzCheck(t, "sentinel", stream.Err() == nil, "operation %d returned nil but stream.Err() is %v", idx, stream.Err())
			return
		}
		var refusal Refusal
		fuzzCheck(t, "sentinel", errors.As(err, &refusal) && refusal >= ErrPastEnd && refusal <= ErrRejected,
			"refusal %v is not a valid Refusal reason", err)
		fuzzCheck(t, "sentinel", errors.Is(stream.Err(), refusal), "stream.Err() %v does not match returned %v", stream.Err(), refusal)
		fuzzCheck(t, "sentinel", unchanged, "refused operation %d modified scalar destination (pattern %#02x)", idx, pattern)
		firstRefusal = refusal
		bitsAtRefusal = stream.BitsProcessed()
		firstFailedIdx = idx
	}

	u32Sent := repeatByte32(pattern)
	u64Sent := repeatByte64(pattern)
	i32Sent := int32(u32Sent)
	i64Sent := int64(u64Sent)
	u8Sent := pattern
	u16Sent := uint16(pattern) | uint16(pattern)<<8
	boolSent := (pattern & 1) != 0
	f32Sent := math.Float32frombits(u32Sent)
	f64Sent := math.Float64frombits(u64Sent)
	strSent := string([]byte{pattern, pattern ^ 0xFF, pattern, pattern ^ 0xFF})
	u128Sent := Uint128{Lo: u64Sent, Hi: u64Sent}
	i128Sent := Int128{Lo: u64Sent, Hi: u64Sent}

	// op 0: bits(7)
	u32 := u32Sent
	err := stream.SerializeBits(&u32, 7)
	if ctl == fuzzControlSentinel && err != nil {
		u32 ^= 1 // NEGATIVE CONTROL
	}
	checkOp(err, u32 == u32Sent)

	// op 1: bits64(33)
	u64 := u64Sent
	err = stream.SerializeBits64(&u64, 33)
	checkOp(err, u64 == u64Sent)

	// op 2: int[-5, 100]
	i32 := i32Sent
	err = stream.SerializeInt(&i32, -5, 100)
	checkOp(err, i32 == i32Sent)

	// op 3: int64 full range
	i64 := i64Sent
	err = stream.SerializeInt64(&i64, math.MinInt64, math.MaxInt64)
	checkOp(err, i64 == i64Sent)

	// op 4: int_relative(100)
	rel := i32Sent
	err = stream.SerializeIntRelative(100, &rel)
	checkOp(err, rel == i32Sent)

	// op 5: uint8
	u8 := u8Sent
	err = stream.SerializeUint8(&u8)
	checkOp(err, u8 == u8Sent)

	// op 6: uint16
	u16 := u16Sent
	err = stream.SerializeUint16(&u16)
	checkOp(err, u16 == u16Sent)

	// op 7: uint32
	u32 = u32Sent
	err = stream.SerializeUint32(&u32)
	checkOp(err, u32 == u32Sent)

	// op 8: uint64
	u64 = u64Sent
	err = stream.SerializeUint64(&u64)
	checkOp(err, u64 == u64Sent)

	// op 9: bool
	flag := boolSent
	err = stream.SerializeBool(&flag)
	checkOp(err, flag == boolSent)

	// op 10: float32
	f32 := f32Sent
	err = stream.SerializeFloat32(&f32)
	checkOp(err, math.Float32bits(f32) == u32Sent)

	// op 11: compressed_float32
	f32 = f32Sent
	err = stream.SerializeCompressedFloat32(&f32, -1, 1, 0.001)
	checkOp(err, math.Float32bits(f32) == u32Sent)

	// op 12: float64
	f64 := f64Sent
	err = stream.SerializeFloat64(&f64)
	checkOp(err, math.Float64bits(f64) == u64Sent)

	// op 13: align (reached at bit 340, unaligned, so nonzero padding can refuse here)
	err = stream.SerializeAlign()
	checkOp(err, true)

	// op 14: string(300)
	str := strSent
	err = stream.SerializeString(&str, 300)
	checkOp(err, str == strSent)

	// op 15: bytes(33)
	buf := make([]byte, 33)
	err = stream.SerializeBytes(buf)
	checkOp(err, true) // caller-owned byte slice is unspecified after refusal

	// op 16: uint128
	v128 := u128Sent
	err = stream.SerializeUint128(&v128)
	checkOp(err, v128 == u128Sent)

	// op 17: fixed64 Q16.16
	fixed32 := i64Sent
	err = stream.SerializeFixed64(&fixed32, 16, 16, -1000, +1000)
	checkOp(err, fixed32 == i64Sent)

	// op 18: fixed64 Q48.16
	fixed64 := i64Sent
	err = stream.SerializeFixed64(&fixed64, 48, 16, -100000000000, +100000000000)
	checkOp(err, fixed64 == i64Sent)

	// op 19: fixed128 Q112.16
	wide := i128Sent
	err = stream.SerializeFixed128(&wide, 112, 16, -1152921504606846976, +1152921504606846976)
	checkOp(err, wide == i128Sent)

	// op 20: int128 ranged
	param := 0
	if len(data) > 0 {
		param = int(data[0])
	}
	bound128 := Int128From64(1).Lsh(uint(20 + param%100))
	ranged128 := i128Sent
	err = stream.SerializeInt128(&ranged128, bound128.Neg(), bound128)
	checkOp(err, ranged128 == i128Sent)

	// op 21: int128 full range
	full128 := i128Sent
	err = stream.SerializeInt128(&full128,
		Uint128From64(1).Lsh(127).Int128(),
		Uint128From64(1).Lsh(127).Not().Int128())
	checkOp(err, full128 == i128Sent)

	// op 22: end
	err = stream.SerializeEnd()
	checkOp(err, true)

	// Drain any remaining bits so every input is guaranteed to trigger a refusal and
	// then exercise post-refusal terminality and sentinel preservation.
	for firstRefusal == 0 {
		u32 = u32Sent
		err = stream.SerializeBits(&u32, 32)
		if ctl == fuzzControlSentinel && err != nil {
			u32 ^= 1 // NEGATIVE CONTROL
		}
		checkOp(err, u32 == u32Sent)
	}
	u32 = u32Sent
	err = stream.SerializeBits(&u32, 1)
	checkOp(err, u32 == u32Sent)
	i32 = i32Sent
	err = stream.SerializeInt(&i32, 0, 1)
	checkOp(err, i32 == i32Sent)

	return firstFailedIdx, firstRefusal
}

func checkFuzzReadStream(t fuzzTB, data []byte, ctl fuzzControl) {
	stream := NewReadStream(data)
	var golden goldenWireData
	_ = goldenWireSerialize(stream, &golden)

	stream.Reset(data)
	var u32 uint32
	if stream.SerializeBits(&u32, 7) == nil {
		fuzzCheck(t, "hostile", u32 < (1<<7), "7-bit read produced %#x", u32)
	}
	var u64 uint64
	if stream.SerializeBits64(&u64, 33) == nil {
		fuzzCheck(t, "hostile", u64 < (uint64(1)<<33), "33-bit read produced %#x", u64)
	}
	var i32 int32
	if stream.SerializeInt(&i32, -5, 100) == nil {
		maxAllowed := int32(100)
		if ctl == fuzzControlHostile {
			maxAllowed = 50 // NEGATIVE CONTROL
		}
		fuzzCheck(t, "hostile", i32 >= -5 && i32 <= maxAllowed, "int[-5,100] decoded %d", i32)
	}
	var i64 int64
	_ = stream.SerializeInt64(&i64, math.MinInt64, math.MaxInt64)
	var relative int32
	if stream.SerializeIntRelative(100, &relative) == nil {
		fuzzCheck(t, "hostile", relative > 100, "int_relative(100) decoded %d", relative)
	}
	var u8 uint8
	_ = stream.SerializeUint8(&u8)
	var u16 uint16
	_ = stream.SerializeUint16(&u16)
	_ = stream.SerializeUint32(&u32)
	_ = stream.SerializeUint64(&u64)
	var flag bool
	_ = stream.SerializeBool(&flag)
	// floats off hostile bytes are finite or refused: the format carries no NaN
	// and no infinity, whatever the bytes say
	var f32 float32
	if stream.SerializeFloat32(&f32) == nil {
		fuzzCheck(t, "hostile", float32BitsAreFinite(math.Float32bits(f32)),
			"float32 decoded a non-finite value: %#x", math.Float32bits(f32))
	}
	if stream.SerializeCompressedFloat32(&f32, -1, 1, 0.001) == nil {
		fuzzCheck(t, "hostile", float32BitsAreFinite(math.Float32bits(f32)) && f32 >= -1.001 && f32 <= 1.001,
			"compressed float decoded an invalid value: %v (%#x)", f32, math.Float32bits(f32))
	}
	var f64 float64
	if stream.SerializeFloat64(&f64) == nil {
		fuzzCheck(t, "hostile", float64BitsAreFinite(math.Float64bits(f64)),
			"double decoded a non-finite value: %#x", math.Float64bits(f64))
	}
	_ = stream.SerializeAlign()
	var str string
	if stream.SerializeString(&str, 300) == nil {
		fuzzCheck(t, "hostile", len(str) < 300 && utf8.ValidString(str) && strings.IndexByte(str, 0) < 0,
			"accepted invalid string of len %d", len(str))
	}
	buffer := make([]byte, 33)
	_ = stream.SerializeBytes(buffer)

	// 128 bit and fixed point reads off hostile bytes must decode within their
	// bounds or fail — never clamp, never wrap, never panic
	var v128 Uint128
	_ = stream.SerializeUint128(&v128)

	var fixed32 int64
	if stream.SerializeFixed64(&fixed32, 16, 16, -1000, +1000) == nil {
		fuzzCheck(t, "hostile", fixed32 >= -1000*65536 && fixed32 <= 1000*65536,
			"fixed64 q16.16 decoded out of bounds: %d", fixed32)
	}

	var fixed64 int64
	if stream.SerializeFixed64(&fixed64, 48, 16, -100000000000, +100000000000) == nil {
		fuzzCheck(t, "hostile", fixed64 >= -100000000000*65536 && fixed64 <= 100000000000*65536,
			"fixed64 q48.16 decoded out of bounds: %d", fixed64)
	}

	var wide Int128
	if stream.SerializeFixed128(&wide, 112, 16, -1152921504606846976, +1152921504606846976) == nil { // ±2^60 units: a raw range past 64 bits
		rawBound := Int128From64(1152921504606846976).Lsh(16)
		fuzzCheck(t, "hostile", wide.Cmp(rawBound.Neg()) >= 0 && wide.Cmp(rawBound) <= 0,
			"fixed128 q112.16 decoded out of bounds: %+v", wide)
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
		fuzzCheck(t, "hostile", ranged128.Cmp(bound128.Neg()) >= 0 && ranged128.Cmp(bound128) <= 0,
			"int128 decoded out of bounds: %+v", ranged128)
	}

	var full128 Int128
	_ = stream.SerializeInt128(&full128,
		Uint128From64(1).Lsh(127).Int128(),       // MinInt128
		Uint128From64(1).Lsh(127).Not().Int128()) // MaxInt128

	_ = stream.SerializeEnd()

	// every refusal names its rule from the closed list
	if err := stream.Err(); err != nil {
		var refusal Refusal
		fuzzCheck(t, "hostile", errors.As(err, &refusal) && refusal >= ErrPastEnd && refusal <= ErrRejected,
			"the stream refused with %v, which names no reason", err)
	}

	// Pass 2: two-sentinel destination preservation and post-refusal terminality
	idxA, refA := runFuzzReadSentinel(t, data, 0x5A, ctl)
	idxB, refB := runFuzzReadSentinel(t, data, 0xA5, ctl)
	fuzzCheck(t, "sentinel", idxA == idxB && refA == refB,
		"sentinel passes disagreed on refusal: 0x5A=(%d,%v) vs 0xA5=(%d,%v)", idxA, refA, idxB, refB)
}

// buildValidReadStreamPrefix writes the first stopBefore operations of the
// checkFuzzReadStream / runFuzzReadSentinel sequence into ws using valid values.
func buildValidReadStreamPrefix(ws *WriteStream, stopBefore int) {
	for step := range stopBefore {
		switch step {
		case 0:
			v := uint32(0x2A) // with step 1 low bit = 1, data[0] = 0xAA = 170, param%100 = 70
			ws.SerializeBits(&v, 7)
		case 1:
			v := uint64(0x123456789)
			ws.SerializeBits64(&v, 33)
		case 2:
			v := int32(42)
			ws.SerializeInt(&v, -5, 100)
		case 3:
			v := int64(-123456789012345)
			ws.SerializeInt64(&v, math.MinInt64, math.MaxInt64)
		case 4:
			v := int32(101) // difference = 1 -> 1 bit (0), bringing total to 112 bits (14 bytes, byte-aligned)
			ws.SerializeIntRelative(100, &v)
		case 5:
			v := uint8(0x7F)
			ws.SerializeUint8(&v)
		case 6:
			v := uint16(0xBEEF)
			ws.SerializeUint16(&v)
		case 7:
			v := uint32(0xCAFEBABE)
			ws.SerializeUint32(&v)
		case 8:
			v := uint64(0x0102030405060708)
			ws.SerializeUint64(&v)
		case 9:
			v := true
			ws.SerializeBool(&v)
		case 10:
			v := float32(1.25)
			ws.SerializeFloat32(&v)
		case 11:
			v := float32(0.5)
			ws.SerializeCompressedFloat32(&v, -1, 1, 0.001)
		case 12:
			v := float64(-2.75)
			ws.SerializeFloat64(&v)
		case 13:
			ws.SerializeAlign()
		case 14:
			v := "hi"
			ws.SerializeString(&v, 300)
		case 15:
			buf := bytes.Repeat([]byte{0x33}, 33)
			ws.SerializeBytes(buf)
		case 16:
			v := Uint128{Lo: 11, Hi: 22}
			ws.SerializeUint128(&v)
		case 17:
			v := int64(250 * 65536)
			ws.SerializeFixed64(&v, 16, 16, -1000, +1000)
		case 18:
			v := int64(-500000 * 65536)
			ws.SerializeFixed64(&v, 48, 16, -100000000000, +100000000000)
		case 19:
			v := Int128From64(1000).Lsh(16)
			ws.SerializeFixed128(&v, 112, 16, -1152921504606846976, +1152921504606846976)
		case 20:
			bound128 := Int128From64(1).Lsh(90)
			v := Int128From64(777)
			ws.SerializeInt128(&v, bound128.Neg(), bound128)
		case 21:
			v := Int128From64(-999)
			ws.SerializeInt128(&v,
				Uint128From64(1).Lsh(127).Int128(),
				Uint128From64(1).Lsh(127).Not().Int128())
		case 22:
			ws.SerializeEnd()
		}
	}
}

type fuzzReadFaultCase struct {
	name    string
	step    int
	refusal Refusal
	data    []byte
}

// buildFuzzReadStreamFaultCases builds valid-prefix + single-fault inputs so every
// operation in checkFuzzReadStream / runFuzzReadSentinel is exercised as the FIRST
// failing operation (plus the full valid packet that accepts all 23 steps).
func buildFuzzReadStreamFaultCases() []fuzzReadFaultCase {
	var cases []fuzzReadFaultCase

	makeFault := func(name string, step int, refusal Refusal, inject func(ws *WriteStream)) {
		buf := make([]byte, 512)
		ws := NewWriteStream(buf)
		buildValidReadStreamPrefix(ws, step)
		inject(ws)
		ws.Flush()
		cases = append(cases, fuzzReadFaultCase{
			name:    name,
			step:    step,
			refusal: refusal,
			data:    append([]byte(nil), ws.Data()...),
		})
	}

	// 1. Truncation (ErrPastEnd) at every bit-consuming step (all steps except 13=align and 22=end):
	for step := range 22 {
		if step == 4 || step == 13 {
			continue
		}
		makeFault(fmt.Sprintf("step_%d_past_end", step), step, ErrPastEnd, func(ws *WriteStream) {})
	}
	// At step 4 (int_relative at bit 111), writing 1 bit (tag=1) reaches bit 112 (14 bytes) and then runs out of bits reading the second tag bit:
	makeFault("step_4_past_end", 4, ErrPastEnd, func(ws *WriteStream) {
		zero := uint32(0)
		ws.SerializeBits(&zero, 1)
	})

	// 2. Semantic faults as the FIRST refusal at each step with a domain/format rule:
	// step 2: int[-5, 100] (range 105 -> 7 bits; 120 > 105 -> ErrValueOutOfRange)
	makeFault("step_2_int_out_of_range", 2, ErrValueOutOfRange, func(ws *WriteStream) {
		raw := uint32(120)
		ws.SerializeBits(&raw, 7)
	})

	// step 4: int_relative(100) non-canonical (4 tag bits 0xF selects 32-bit tier [281..MaxInt32], followed by diff=1 < 281 -> ErrNonCanonical)
	makeFault("step_4_int_relative_non_canonical", 4, ErrNonCanonical, func(ws *WriteStream) {
		prefix := uint32(0xF)
		ws.SerializeBits(&prefix, 4)
		diff := uint32(1)
		ws.SerializeBits(&diff, 32)
	})

	// step 10: float32 NaN (0x7FC00000 -> ErrNonFiniteFloat)
	makeFault("step_10_float32_nan", 10, ErrNonFiniteFloat, func(ws *WriteStream) {
		nanBits := uint32(0x7FC00000)
		ws.SerializeUint32(&nanBits)
	})

	// step 11: compressed_float32 [-1, 1, 0.001] (maxIntegerValue=2000, 11 bits; 2047 > 2000 -> ErrValueOutOfRange)
	makeFault("step_11_compressed_float32_out_of_range", 11, ErrValueOutOfRange, func(ws *WriteStream) {
		raw := uint32(2047)
		ws.SerializeBits(&raw, 11)
	})

	// step 12: float64 +Inf (0x7FF0000000000000 -> ErrNonFiniteFloat)
	makeFault("step_12_float64_inf", 12, ErrNonFiniteFloat, func(ws *WriteStream) {
		infBits := uint64(0x7FF0000000000000)
		ws.SerializeUint64(&infBits)
	})

	// step 13: align at bit 340 (4 padding bits; writing 1 -> ErrNonzeroPadding)
	makeFault("step_13_align_nonzero_padding", 13, ErrNonzeroPadding, func(ws *WriteStream) {
		one := uint32(1)
		ws.SerializeBits(&one, 4)
	})

	// step 14: string(300) invalid UTF-8 (len=1, byte 0xFF -> ErrInvalidUTF8)
	makeFault("step_14_string_invalid_utf8", 14, ErrInvalidUTF8, func(ws *WriteStream) {
		var length int32 = 1
		ws.SerializeInt(&length, 0, 299)
		ws.SerializeAlign()
		bad := uint8(0xFF)
		ws.SerializeUint8(&bad)
	})

	// step 14: string(300) embedded NUL (len=1, byte 0x00 -> ErrEmbeddedNul)
	makeFault("step_14_string_embedded_nul", 14, ErrEmbeddedNul, func(ws *WriteStream) {
		var length int32 = 1
		ws.SerializeInt(&length, 0, 299)
		ws.SerializeAlign()
		nul := uint8(0x00)
		ws.SerializeUint8(&nul)
	})

	// step 17: fixed64 Q16.16 [-1000, +1000] (range = 2000*65536 = 131072000, 27 bits; all-ones 0x7FFFFFF > 131072000 -> ErrValueOutOfRange)
	makeFault("step_17_fixed64_q16_16_out_of_range", 17, ErrValueOutOfRange, func(ws *WriteStream) {
		raw := uint32(0x7FFFFFF)
		ws.SerializeBits(&raw, 27)
	})

	// step 18: fixed64 Q48.16 [-1e11, +1e11] (54 bits; all-ones (1<<54)-1 > range -> ErrValueOutOfRange)
	makeFault("step_18_fixed64_q48_16_out_of_range", 18, ErrValueOutOfRange, func(ws *WriteStream) {
		raw := (uint64(1) << 54) - 1
		ws.SerializeBits64(&raw, 54)
	})

	// step 19: fixed128 Q112.16 [-2^60, +2^60] (range = 2^77, 78 bits -> 64 low bits + 14 high bits; all-ones -> ErrValueOutOfRange)
	makeFault("step_19_fixed128_q112_16_out_of_range", 19, ErrValueOutOfRange, func(ws *WriteStream) {
		lo := ^uint64(0)
		hi := uint32((1 << 14) - 1)
		ws.SerializeBits64(&lo, 64)
		ws.SerializeBits(&hi, 14)
	})

	// step 20: int128 ranged [-2^90, +2^90] (range = 2^91, 92 bits -> 64 low bits + 28 high bits; all-ones -> ErrValueOutOfRange)
	makeFault("step_20_int128_ranged_out_of_range", 20, ErrValueOutOfRange, func(ws *WriteStream) {
		lo := ^uint64(0)
		hi := uint32((1 << 28) - 1)
		ws.SerializeBits64(&lo, 64)
		ws.SerializeBits(&hi, 28)
	})

	// step 22: end with nonzero padding (after step 21, bit position is unaligned; write 1 in padding bit -> ErrNonzeroPadding)
	makeFault("step_22_end_nonzero_padding", 22, ErrNonzeroPadding, func(ws *WriteStream) {
		one := uint32(1)
		ws.SerializeBits(&one, 1)
	})

	// step 22: end with trailing byte (valid prefix through step 22, plus 1 extra zero byte -> ErrTrailingData)
	{
		buf := make([]byte, 512)
		ws := NewWriteStream(buf)
		buildValidReadStreamPrefix(ws, 23)
		ws.Flush()
		trailing := append(append([]byte(nil), ws.Data()...), 0x00)
		cases = append(cases, fuzzReadFaultCase{
			name:    "step_22_end_trailing_data",
			step:    22,
			refusal: ErrTrailingData,
			data:    trailing,
		})
	}

	return cases
}

// FuzzReadStream feeds arbitrary bytes to a read stream and exercises every serialize
// method. Reads of malicious packet data must fail with errors, never panic, leave
// scalar destinations untouched on refusal, and latch refusal terminally.
func FuzzReadStream(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF})
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Add(goldenWireBytes)
	validBuf := make([]byte, 512)
	validWS := NewWriteStream(validBuf)
	buildValidReadStreamPrefix(validWS, fuzzReadSeqNumOps)
	validWS.Flush()
	f.Add(append([]byte(nil), validWS.Data()...))
	for _, fc := range buildFuzzReadStreamFaultCases() {
		f.Add(fc.data)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		checkFuzzReadStream(t, data, fuzzControlNone)
	})
}

func checkFuzzRoundTrip(t fuzzTB, a uint32, aBits uint8, b uint64, bBits uint8, c int32, d int32,
	str string, bulk []byte, flag bool, f32 float32, f64 float64, ctl fuzzControl) {

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
	str = strings.ToValidUTF8(str, "")
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
		fuzzCheck(t, "roundtrip", panics(func() { probe.SerializeFloat32(&f32) }),
			"the writer accepted the non-finite float %#x", math.Float32bits(f32))
		fuzzCheck(t, "roundtrip", panics(func() { probe.SerializeCompressedFloat32(&f32, -1000, 1000, 0.01) }),
			"the writer accepted the non-finite compressed float %#x", math.Float32bits(f32))
		f32 = 0
	}
	if !float64BitsAreFinite(math.Float64bits(f64)) {
		probe := NewWriteStream(make([]byte, 8))
		fuzzCheck(t, "roundtrip", panics(func() { probe.SerializeFloat64(&f64) }),
			"the writer accepted the non-finite double %#x", math.Float64bits(f64))
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
	var value64 int64
	if uint64(max64-min64) == math.MaxUint64 {
		value64 = int64(b ^ uint64(d))
	} else {
		value64 = min64 + int64((uint64(c)*0x9E3779B97F4A7C15)%(uint64(max64-min64)+1))
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

	writeAll := func(ws *WriteStream) {
		w128 := value128
		wFixed64 := expectedFixed64
		wWide := expectedWide
		wInt128 := expectedInt128
		af := aField
		bf := bField
		cv := c
		fl := flag
		f32v := f32
		f64v := f64
		strv := str
		bulkv := append([]byte(nil), bulk...)
		ws.SerializeBits(&af, abits)
		ws.SerializeBits64(&bf, bbits)
		ws.SerializeInt(&cv, min, max)
		ws.SerializeBool(&fl)
		ws.SerializeFloat32(&f32v)
		ws.SerializeFloat64(&f64v)
		ws.SerializeString(&strv, 256)
		ws.SerializeBytes(bulkv)
		ws.SerializeUint128(&w128)
		ws.SerializeFixed64(&wFixed64, 48, 16, -100000000000, +100000000000)
		ws.SerializeFixed128(&wWide, 112, 16, -1152921504606846976, +1152921504606846976)
		ws.SerializeInt128(&wInt128, int128Bound.Neg(), int128Bound)
	}

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
	fuzzCheck(t, "roundtrip", measureStream.Err() == nil, "measure failed: %v", measureStream.Err())
	fuzzCheck(t, "roundtrip", measureStream.BitsProcessed() == writeStream.BitsProcessed(),
		"measure %d bits, write %d bits for the same unaligned fields", measureStream.BitsProcessed(), writeStream.BitsProcessed())

	writeAll(writeStream)
	fuzzCheck(t, "roundtrip", writeStream.Err() == nil, "write failed: %v", writeStream.Err())
	writeStream.Flush()

	// Tight write-buffer + 16-byte 0xAA canary check: re-run the exact write sequence
	// into the smallest multiple of 8 holding the written bytes, followed by 16 canary
	// bytes, and verify no byte past tightBytes is touched.
	{
		written := writeStream.BytesProcessed()
		tightBytes := int(((written + 7) / 8) * 8)
		if tightBytes == 0 {
			tightBytes = 8
		}
		const canaryBytes = 16
		tight := make([]byte, tightBytes+canaryBytes)
		for i := range tight[:tightBytes] {
			tight[i] = 0xA5
		}
		for i := range tight[tightBytes:] {
			tight[tightBytes+i] = 0xAA
		}
		tightStream := NewWriteStream(tight[:tightBytes])
		v64, w8, w16, w32, w64, cur, cf := value64, u8, u16, u32, u64, current, cfValue
		tightStream.SerializeInt64(&v64, min64, max64)
		tightStream.SerializeUint8(&w8)
		tightStream.SerializeUint16(&w16)
		tightStream.SerializeUint32(&w32)
		tightStream.SerializeUint64(&w64)
		degenerate, degenerate64 := c, value64
		tightStream.SerializeInt(&degenerate, c, c)
		tightStream.SerializeInt64(&degenerate64, value64, value64)
		tightStream.SerializeIntRelative(previous, &cur)
		tightStream.SerializeCompressedFloat32(&cf, cfMin, cfMax, cfRes)
		writeAll(tightStream)
		tightStream.Flush()
		fuzzCheck(t, "roundtrip", tightStream.BytesProcessed() == written,
			"tight BytesProcessed %d != %d", tightStream.BytesProcessed(), written)
		fuzzCheck(t, "roundtrip", bytes.Equal(tight[:written], writeStream.Data()),
			"tight buffer bytes differ from writeStream.Data()")
		if ctl == fuzzControlCanary {
			tight[tightBytes] = 0 // NEGATIVE CONTROL
		}
		for i := range canaryBytes {
			fuzzCheck(t, "canary", tight[tightBytes+i] == 0xAA,
				"canary byte %d corrupted: %#02x", i, tight[tightBytes+i])
		}
	}

	readAll := func(rs *ReadStream) (int64, uint8, uint16, uint32, uint64, int32, int64, int32, float32, uint32, uint64, int32, string, []byte, bool, float32, float64, Uint128, int64, Int128, Int128) {
		var r64 int64
		var r8 uint8
		var r16 uint16
		var r32 uint32
		var ru64 uint64
		var rDegenerate int32
		var rDegenerate64 int64
		var rCurrent int32
		var rcf float32
		rs.SerializeInt64(&r64, min64, max64)
		rs.SerializeUint8(&r8)
		rs.SerializeUint16(&r16)
		rs.SerializeUint32(&r32)
		rs.SerializeUint64(&ru64)
		rs.SerializeInt(&rDegenerate, c, c)
		rs.SerializeInt64(&rDegenerate64, value64, value64)
		rs.SerializeIntRelative(previous, &rCurrent)
		rs.SerializeCompressedFloat32(&rcf, cfMin, cfMax, cfRes)
		var ra uint32
		var rb uint64
		var rc int32
		var rstr string
		rbulk := make([]byte, len(bulk))
		var rflag bool
		var rf32 float32
		var rf64 float64
		rs.SerializeBits(&ra, abits)
		rs.SerializeBits64(&rb, bbits)
		rs.SerializeInt(&rc, min, max)
		rs.SerializeBool(&rflag)
		rs.SerializeFloat32(&rf32)
		rs.SerializeFloat64(&rf64)
		rs.SerializeString(&rstr, 256)
		rs.SerializeBytes(rbulk)
		var r128 Uint128
		rs.SerializeUint128(&r128)
		var rFixed64 int64
		rs.SerializeFixed64(&rFixed64, 48, 16, -100000000000, +100000000000)
		var rWide Int128
		rs.SerializeFixed128(&rWide, 112, 16, -1152921504606846976, +1152921504606846976)
		var rInt128 Int128
		rs.SerializeInt128(&rInt128, int128Bound.Neg(), int128Bound)
		return r64, r8, r16, r32, ru64, rDegenerate, rDegenerate64, rCurrent, rcf, ra, rb, rc, rstr, rbulk, rflag, rf32, rf64, r128, rFixed64, rWide, rInt128
	}

	readStream := NewReadStream(writeStream.Data())
	r64, r8, r16, r32, ru64, rDegenerate, rDegenerate64, rCurrent, rcf, ra, rb, rc, rstr, rbulk, rflag, rf32, rf64, r128, rFixed64, rWide, rInt128 := readAll(readStream)
	// the stream holds exactly what was written, so its end must accept
	readStream.SerializeEnd()
	fuzzCheck(t, "roundtrip", readStream.Err() == nil, "read failed: %v", readStream.Err())

	fuzzCheck(t, "roundtrip", r64 == value64 && r8 == u8 && r16 == u16 && r32 == u32 && ru64 == u64,
		"int64/uintN mismatch: wrote %d %d %d %d %d, read %d %d %d %d %d", value64, u8, u16, u32, u64, r64, r8, r16, r32, ru64)
	fuzzCheck(t, "roundtrip", rDegenerate == c && rDegenerate64 == value64,
		"degenerate mismatch: wrote %d %d, read %d %d", c, value64, rDegenerate, rDegenerate64)
	fuzzCheck(t, "roundtrip", rCurrent == current,
		"int_relative mismatch: previous %d, wrote %d, read %d", previous, current, rCurrent)
	clamped := cfValue
	if clamped < cfMin {
		clamped = cfMin
	} else if clamped > cfMax {
		clamped = cfMax
	}
	fuzzCheck(t, "roundtrip", math.Abs(float64(rcf-clamped)) <= float64(cfRes),
		"compressed float mismatch: wrote %v (clamped %v), read %v", cfValue, clamped, rcf)
	fuzzCheck(t, "roundtrip", ra == a&uint32(uint64(1)<<abits-1),
		"bits mismatch: wrote %#x (%d bits), read %#x", a, abits, ra)
	fuzzCheck(t, "roundtrip", rb == b&(uint64(1)<<bbits-1),
		"bits64 mismatch: wrote %#x (%d bits), read %#x", b, bbits, rb)
	expectedC := c
	if ctl == fuzzControlRoundTrip {
		expectedC = c + 1 // NEGATIVE CONTROL
	}
	fuzzCheck(t, "roundtrip", rc == expectedC,
		"int mismatch: wrote %d in [%d,%d], read %d", c, min, max, rc)
	fuzzCheck(t, "roundtrip", rflag == flag, "bool mismatch")
	fuzzCheck(t, "roundtrip", math.Float32bits(rf32) == math.Float32bits(f32),
		"float32 mismatch: wrote %x, read %x", math.Float32bits(f32), math.Float32bits(rf32))
	fuzzCheck(t, "roundtrip", math.Float64bits(rf64) == math.Float64bits(f64),
		"float64 mismatch: wrote %x, read %x", math.Float64bits(f64), math.Float64bits(rf64))
	fuzzCheck(t, "roundtrip", rstr == str, "string mismatch: wrote %q, read %q", str, rstr)
	fuzzCheck(t, "roundtrip", bytes.Equal(rbulk, bulk), "bytes mismatch: wrote %x, read %x", bulk, rbulk)
	fuzzCheck(t, "roundtrip", r128 == value128, "uint128 mismatch: wrote %+v, read %+v", value128, r128)
	fuzzCheck(t, "roundtrip", rFixed64 == expectedFixed64, "fixed64 mismatch: wrote %d, read %d", expectedFixed64, rFixed64)
	fuzzCheck(t, "roundtrip", rWide == expectedWide, "fixed128 mismatch: wrote %+v, read %+v", expectedWide, rWide)
	fuzzCheck(t, "roundtrip", rInt128 == expectedInt128, "int128 mismatch: wrote %+v, read %+v", expectedInt128, rInt128)

	// One byte more than the writer reported is trailing data, refused at the end.
	longerBytes := append(append([]byte(nil), writeStream.Data()...), 0x00)
	longerStream := NewReadStream(longerBytes)
	readAll(longerStream)
	fuzzCheck(t, "roundtrip", longerStream.Err() == nil, "longer read failed before end: %v", longerStream.Err())
	endErr := longerStream.SerializeEnd()
	if ctl == fuzzControlTrailing {
		fuzzCheck(t, "trailing", endErr == nil, "NEGATIVE CONTROL: expected trailing data to be accepted")
	} else {
		fuzzCheck(t, "trailing", errors.Is(endErr, ErrTrailingData), "expected ErrTrailingData, got %v", endErr)
	}
}

// FuzzRoundTrip writes fuzzed values and reads them back, verifying they survive the
// trip exactly.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint32(0), uint8(0), uint64(0), uint8(0), int32(0), int32(0), "", []byte{}, false, float32(0), 0.0)
	f.Add(^uint32(0), uint8(0), uint64(1)<<63, uint8(0), int32(0), int32(0), "", []byte{}, false, float32(0), 0.0)
	f.Add(uint32(0xDEADBEEF), uint8(31), uint64(0x123456789ABCDEF0), uint8(63), int32(-1000), int32(1000),
		"hello world", []byte{1, 2, 3}, true, float32(3.14159), 1.0/3.0)

	f.Fuzz(func(t *testing.T, a uint32, aBits uint8, b uint64, bBits uint8, c int32, d int32,
		str string, bulk []byte, flag bool, f32 float32, f64 float64) {
		checkFuzzRoundTrip(t, a, aBits, b, bBits, c, d, str, bulk, flag, f32, f64, fuzzControlNone)
	})
}

// a64 widens a fuzzed uint32 for operand mixing.
func a64(a uint32) uint64 {
	return uint64(a) | uint64(a)<<32
}

type fuzzFailure struct {
	msg string
}

type fuzzRecorder struct {
	failed string
}

func (r *fuzzRecorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
	panic(fuzzFailure{msg: r.failed})
}

func runFuzzReplay(tb fuzzTB, count int, ctl fuzzControl) {
	state := uint64(0x9E3779B97F4A7C15)
	next := func() uint32 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return uint32(state >> 16)
	}
	next64 := func() uint64 {
		return uint64(next())<<32 | uint64(next())
	}

	checkFuzzReadStream(tb, []byte{}, ctl)
	checkFuzzReadStream(tb, []byte{0xFF}, ctl)
	checkFuzzReadStream(tb, bytes.Repeat([]byte{0xFF}, 64), ctl)
	checkFuzzReadStream(tb, goldenWireBytes, ctl)
	validBuf := make([]byte, 512)
	validWS := NewWriteStream(validBuf)
	buildValidReadStreamPrefix(validWS, fuzzReadSeqNumOps)
	validWS.Flush()
	checkFuzzReadStream(tb, validWS.Data(), ctl)
	for _, fc := range buildFuzzReadStreamFaultCases() {
		checkFuzzReadStream(tb, fc.data, ctl)
	}
	checkFuzzRoundTrip(tb, ^uint32(0), 0, uint64(1)<<63, 0, 0, 0, "", nil, false, 0, 0, ctl)

	for n := range count {
		length := 1 + int(next()%256)
		if n%7 == 0 {
			length = int(next() % 8)
		}
		buf := make([]byte, length)
		for i := range buf {
			buf[i] = byte(next())
		}
		if n%11 == 0 {
			fill := byte(0x00)
			if n%2 == 1 {
				fill = 0xFF
			}
			for i := range buf {
				buf[i] = fill
			}
		}
		checkFuzzReadStream(tb, buf, ctl)

		a := next()
		aBits := uint8(next())
		b := next64()
		bBits := uint8(next())
		c := int32(next())
		d := int32(next())
		strLen := int(next() % 32)
		strBytes := make([]byte, strLen)
		for i := range strBytes {
			strBytes[i] = byte(32 + (next() % 95))
		}
		bulkLen := int(next() % 64)
		bulk := make([]byte, bulkLen)
		for i := range bulk {
			bulk[i] = byte(next())
		}
		flag := (next() & 1) != 0
		f32 := math.Float32frombits(next() & 0x807FFFFF)
		f64 := math.Float64frombits(next64() & 0x800FFFFFFFFFFFFF)
		if len(buf) >= 8 {
			f32 = math.Float32frombits(binary.LittleEndian.Uint32(buf[:4]) & 0x807FFFFF)
			f64 = math.Float64frombits(binary.LittleEndian.Uint64(buf[:8]) & 0x800FFFFFFFFFFFFF)
		}
		checkFuzzRoundTrip(tb, a, aBits, b, bBits, c, d, string(strBytes), bulk, flag, f32, f64, ctl)
	}
}

// TestFuzzRoundTripFullRangeInt64 verifies that checkFuzzRoundTrip handles the
// full 64-bit signed range [math.MinInt64, math.MaxInt64] without wrapping
// uint64(max64-min64)+1 to zero.
func TestFuzzRoundTripFullRangeInt64(t *testing.T) {
	checkFuzzRoundTrip(t, ^uint32(0), 0, uint64(1)<<63, 0, 0, 0, "", nil, false, 0, 0, fuzzControlNone)
}

// TestFuzzReadFirstFailureCoverage verifies that the valid-prefix + single-fault
// corpus exercises every operation 0..fuzzReadSeqNumOps-1 as the FIRST failing
// operation (plus the full valid sequence that accepts all 23 operations).
func TestFuzzReadFirstFailureCoverage(t *testing.T) {
	validBuf := make([]byte, 512)
	validWS := NewWriteStream(validBuf)
	buildValidReadStreamPrefix(validWS, fuzzReadSeqNumOps)
	validWS.Flush()
	idxA, _ := runFuzzReadSentinel(t, validWS.Data(), 0x5A, fuzzControlNone)
	idxB, _ := runFuzzReadSentinel(t, validWS.Data(), 0xA5, fuzzControlNone)
	if idxA != fuzzReadSeqNumOps || idxB != fuzzReadSeqNumOps {
		t.Fatalf("valid packet refused early at step %d / %d", idxA, idxB)
	}

	coveredSteps := make([]bool, fuzzReadSeqNumOps)
	for _, fc := range buildFuzzReadStreamFaultCases() {
		gotIdx, gotRefusal := runFuzzReadSentinel(t, fc.data, 0x5A, fuzzControlNone)
		gotIdxB, gotRefusalB := runFuzzReadSentinel(t, fc.data, 0xA5, fuzzControlNone)
		if gotIdx != fc.step || gotRefusal != fc.refusal || gotIdxB != fc.step || gotRefusalB != fc.refusal {
			t.Fatalf("fault case %s: want step=%d refusal=%v, got 0x5A=(%d,%v) 0xA5=(%d,%v)",
				fc.name, fc.step, fc.refusal, gotIdx, gotRefusal, gotIdxB, gotRefusalB)
		}
		coveredSteps[fc.step] = true
	}
	for step, ok := range coveredSteps {
		if !ok {
			t.Fatalf("operation step %d was never exercised as the first failing operation", step)
		}
	}
}

// TestFuzzReplay runs the hostile-read, two-sentinel, tight-canary, and round-trip
// fuzz checks over a deterministic xorshift stream during every `go test` invocation.
func TestFuzzReplay(t *testing.T) {
	runFuzzReplay(t, 500, fuzzControlNone)
}

// TestFuzzControls is the negative-control suite for the fuzz harnesses: each control
// sabotages one check and passes only when the deterministic replay traps in the
// pass that owns that check.
func TestFuzzControls(t *testing.T) {
	controls := []struct {
		ctl  fuzzControl
		pass string
	}{
		{fuzzControlSentinel, "sentinel"},
		{fuzzControlHostile, "hostile"},
		{fuzzControlTerminality, "terminality"},
		{fuzzControlRoundTrip, "roundtrip"},
		{fuzzControlCanary, "canary"},
		{fuzzControlTrailing, "trailing"},
	}

	for _, tc := range controls {
		t.Run(tc.pass, func(t *testing.T) {
			rec := &fuzzRecorder{}
			func() {
				defer func() {
					if r := recover(); r != nil {
						if _, ok := r.(fuzzFailure); !ok {
							panic(r)
						}
					}
				}()
				runFuzzReplay(rec, 200, tc.ctl)
			}()
			wantPrefix := "fuzz_check failed: pass " + tc.pass + ":"
			if !strings.HasPrefix(rec.failed, wantPrefix) {
				t.Fatalf("control %s did not trap in pass %s; got %q", tc.pass, tc.pass, rec.failed)
			}
		})
	}
}
