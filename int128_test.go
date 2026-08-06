package serialize

import (
	"encoding/binary"
	"math"
	"math/big"
	"testing"
)

// The 128 bit pair tests mirror test_uint128_emulation, test_int128_emulation,
// test_bits_required128 and the differential tests in the C++ serialize library.
// The known answer checks pin the documented semantics operator by operator; the
// differential tests then prove every operation against math/big, an implementation
// that shares no code with the pair.

func u128(hi, lo uint64) Uint128 {
	return Uint128{Lo: lo, Hi: hi}
}

func i128(hi, lo uint64) Int128 {
	return Int128{Lo: lo, Hi: hi}
}

func TestUint128(t *testing.T) {
	lo := uint64(0xFEDCBA9876543210)
	hi := uint64(0x0123456789ABCDEF)

	// construction from uint64 fills the low lane. Uint64 truncates back to it
	if v := Uint128From64(lo); v.Lo != lo || v.Hi != 0 || v.Uint64() != lo {
		t.Fatalf("Uint128From64: %+v", v)
	}
	if u128(hi, lo).Uint64() != lo {
		t.Fatal("Uint64 must truncate to the low lane")
	}

	// signed sources sign extend (negatives wrap modulo 2^128), unsigned sources zero
	// extend: the same conversions as the C++ constructors
	if Int128From64(-1).Uint128() != u128(0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF) {
		t.Fatal("Int128From64(-1) bit pattern")
	}
	if Int128From64(math.MinInt64).Uint128() != u128(0xFFFFFFFFFFFFFFFF, 0x8000000000000000) {
		t.Fatal("Int128From64(MinInt64) bit pattern")
	}
	if Uint128From64(uint64(1)<<63) != u128(0, 0x8000000000000000) {
		t.Fatal("Uint128From64(1<<63) must zero extend")
	}
	if Int128From64(7).Uint128() != u128(0, 7) {
		t.Fatal("non negative signed: zero high lane")
	}

	// comparison, driven by the high lane, the low lane, and equality
	a := u128(1, 0)
	b := u128(0, 0xFFFFFFFFFFFFFFFF)
	c := u128(1, 1)
	if a.Cmp(b) <= 0 || b.Cmp(a) >= 0 {
		t.Fatal("the high lane dominates")
	}
	if c.Cmp(a) <= 0 {
		t.Fatal("the low lane breaks ties")
	}
	aAgain := u128(1, 0)
	if a.Cmp(aAgain) != 0 || a != aAgain || a == b {
		t.Fatal("equality")
	}

	// left shift edges: 0, 1, 63, 64, 65, 127, and out of range counts
	v := u128(hi, lo)
	shiftCases := []struct {
		n    uint
		want Uint128
	}{
		{0, v},
		{1, u128(hi<<1|lo>>63, lo<<1)},
		{63, u128(hi<<63|lo>>1, lo<<63)},
		{64, u128(lo, 0)}, // the low lane becomes the high lane
		{65, u128(lo<<1, 0)},
		{127, u128(lo<<63, 0)},
		{128, Uint128{}}, // >= 128 yields zero, documented
		{200, Uint128{}},
	}
	for _, cse := range shiftCases {
		if got := v.Lsh(cse.n); got != cse.want {
			t.Fatalf("Lsh(%d): got %+v, want %+v", cse.n, got, cse.want)
		}
	}

	// right shift edges: the mirror image
	rshCases := []struct {
		n    uint
		want Uint128
	}{
		{0, v},
		{1, u128(hi>>1, lo>>1|hi<<63)},
		{63, u128(hi>>63, lo>>63|hi<<1)},
		{64, u128(0, hi)}, // the high lane becomes the low lane
		{65, u128(0, hi>>1)},
		{127, u128(0, hi>>63)},
		{128, Uint128{}}, // >= 128 yields zero, documented
		{200, Uint128{}},
	}
	for _, cse := range rshCases {
		if got := v.Rsh(cse.n); got != cse.want {
			t.Fatalf("Rsh(%d): got %+v, want %+v", cse.n, got, cse.want)
		}
	}

	// addition carries out of the low lane, subtraction borrows back into it
	max64 := Uint128From64(0xFFFFFFFFFFFFFFFF)
	allOnes := u128(0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF)
	one := Uint128From64(1)
	if max64.Add(one) != u128(1, 0) {
		t.Fatal("carry out of the low lane")
	}
	if allOnes.Add(one) != (Uint128{}) {
		t.Fatal("wraps modulo 2^128")
	}
	if (Uint128{}).Sub(one) != allOnes {
		t.Fatal("borrow wraps back")
	}
	if u128(1, 0).Sub(one) != max64 {
		t.Fatal("borrow out of the high lane")
	}

	// multiplication: lane crossing products with known answers
	twoTo32 := Uint128From64(1 << 32)
	if Uint128From64(3).Mul(Uint128From64(5)) != Uint128From64(15) {
		t.Fatal("3 * 5")
	}
	if twoTo32.Mul(twoTo32) != u128(1, 0) {
		t.Fatal("2^64 crosses into the high lane")
	}
	if max64.Mul(max64) != u128(0xFFFFFFFFFFFFFFFE, 1) {
		t.Fatal("max64 * max64") // 2^128 - 2^65 + 1
	}
	if u128(1, 0).Mul(Uint128From64(5)) != u128(5, 0) {
		t.Fatal("the high lane path")
	}
	if allOnes.Mul(allOnes) != one {
		t.Fatal("( 2^128 - 1 )^2 mod 2^128")
	}

	// division and modulo: the small fast path, wide dividends, wide divisors, and
	// the TOTALITY of division by zero — the operation is undefined and these pin
	// only that it returns rather than panics, never that the value is a contract
	divCases := []struct {
		x, y, quo, rem Uint128
	}{
		{Uint128From64(100), Uint128From64(7), Uint128From64(14), Uint128From64(2)},
		{u128(1, 7), Uint128From64(16), Uint128From64(1 << 60), Uint128From64(7)}, // 2^64 + 7 over 16
		{u128(1<<63, 0), u128(1, 0), Uint128From64(1 << 63), Uint128{}},           // 2^127 over 2^64
		{allOnes, Uint128From64(3), u128(0x5555555555555555, 0x5555555555555555), Uint128{}},
		{Uint128From64(7), u128(1, 0), Uint128{}, Uint128From64(7)}, // dividend below divisor
		{allOnes, Uint128{}, Uint128{}, Uint128{}},                  // by zero is UNDEFINED: this pins totality, not a value contract
	}
	for _, cse := range divCases {
		if got := cse.x.Div(cse.y); got != cse.quo {
			t.Fatalf("Div(%+v, %+v): got %+v, want %+v", cse.x, cse.y, got, cse.quo)
		}
		if got := cse.x.Mod(cse.y); got != cse.rem {
			t.Fatalf("Mod(%+v, %+v): got %+v, want %+v", cse.x, cse.y, got, cse.rem)
		}
	}

	// bitwise operations work lane by lane
	p := u128(0xF0F0F0F0F0F0F0F0, 0xAAAAAAAAAAAAAAAA)
	q := u128(0xFF00FF00FF00FF00, 0xCCCCCCCCCCCCCCCC)
	if p.And(q) != u128(0xF000F000F000F000, 0x8888888888888888) {
		t.Fatal("And")
	}
	if p.Or(q) != u128(0xFFF0FFF0FFF0FFF0, 0xEEEEEEEEEEEEEEEE) {
		t.Fatal("Or")
	}
	if p.Xor(q) != u128(0x0FF00FF00FF00FF0, 0x6666666666666666) {
		t.Fatal("Xor")
	}
	if p.Not() != u128(0x0F0F0F0F0F0F0F0F, 0x5555555555555555) {
		t.Fatal("Not")
	}

	// negation is two's complement
	if (Uint128{}).Neg() != (Uint128{}) {
		t.Fatal("-0")
	}
	if one.Neg() != allOnes {
		t.Fatal("-1 wraps to all ones")
	}
	if v.Neg().Add(v) != (Uint128{}) {
		t.Fatal("-v + v")
	}

	// Len and IsZero
	if (Uint128{}).Len() != 0 || !(Uint128{}).IsZero() {
		t.Fatal("zero")
	}
	if one.Len() != 1 || u128(1, 0).Len() != 65 || allOnes.Len() != 128 || one.IsZero() {
		t.Fatal("Len")
	}
}

func TestInt128(t *testing.T) {
	int128Min := i128(0x8000000000000000, 0)
	int128Max := i128(0x7FFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF)
	minusOne := Int128From64(-1)

	// Int128From64 sign extends into the high lane; Int64 truncates back to the low
	// lane; the Uint128 conversions preserve the bit pattern both ways
	if minusOne != i128(0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF) {
		t.Fatal("Int128From64(-1)")
	}
	if Int128From64(math.MinInt64) != i128(0xFFFFFFFFFFFFFFFF, 0x8000000000000000) {
		t.Fatal("Int128From64(MinInt64)")
	}
	if Int128From64(math.MaxInt64) != i128(0, 0x7FFFFFFFFFFFFFFF) {
		t.Fatal("Int128From64(MaxInt64)")
	}
	if Int128From64(-5).Int64() != -5 || Int128From64(math.MinInt64).Int64() != math.MinInt64 {
		t.Fatal("Int64 truncation")
	}
	if minusOne.Uint128().Int128() != minusOne {
		t.Fatal("bit preserving conversions")
	}

	// signed ordering: negatives below positives, the high lane compares signed, the
	// low lane breaks ties unsigned
	zero := Int128{}
	if minusOne.Cmp(zero) >= 0 || zero.Cmp(Int128From64(1)) >= 0 {
		t.Fatal("negatives below positives")
	}
	if int128Min.Cmp(Int128From64(math.MinInt64)) >= 0 || int128Min.Cmp(minusOne) >= 0 {
		t.Fatal("int128Min below every int64")
	}
	if int128Max.Cmp(Int128From64(math.MaxInt64)) <= 0 || int128Min.Cmp(int128Max) >= 0 {
		t.Fatal("int128Max above every int64")
	}
	if i128(0xFFFFFFFFFFFFFFFF, 5).Cmp(i128(0xFFFFFFFFFFFFFFFF, 7)) >= 0 {
		t.Fatal("low lane tiebreak among negatives")
	}
	if Int128From64(1).Cmp(minusOne) <= 0 {
		t.Fatal("1 > -1")
	}
	// a uint64 with the top bit set stays a large positive value: it does NOT wrap
	// negative — the exact cross signed case the C++ constructors pin
	if Uint128From64(1<<63).Int128().Cmp(zero) <= 0 {
		t.Fatal("2^63 is positive as an Int128")
	}

	// addition, subtraction and multiplication with mixed signs, and the documented wrap
	if Int128From64(-3).Add(Int128From64(5)) != Int128From64(2) {
		t.Fatal("-3 + 5")
	}
	if Int128From64(3).Sub(Int128From64(5)) != Int128From64(-2) {
		t.Fatal("3 - 5")
	}
	if Int128From64(-3).Mul(Int128From64(5)) != Int128From64(-15) {
		t.Fatal("-3 * 5")
	}
	if Int128From64(-3).Mul(Int128From64(-5)) != Int128From64(15) {
		t.Fatal("-3 * -5")
	}
	if int128Max.Add(Int128From64(1)) != int128Min {
		t.Fatal("wraps two's complement, documented")
	}
	if int128Min.Sub(Int128From64(1)) != int128Max {
		t.Fatal("wraps back")
	}
	if Int128From64(math.MaxInt64).Mul(Int128From64(4)) != i128(1, 0xFFFFFFFFFFFFFFFC) {
		t.Fatal("crosses the lane boundary")
	}

	// the arithmetic right shift fills with the sign: edges and out of range counts
	rshCases := []struct {
		x    Int128
		n    uint
		want Int128
	}{
		{int128Min, 0, int128Min},
		{int128Min, 1, i128(0xC000000000000000, 0)},
		{int128Min, 63, i128(0xFFFFFFFFFFFFFFFF, 0)}, // -2^64
		{int128Min, 64, Int128From64(math.MinInt64)},
		{int128Min, 65, Int128From64(math.MinInt64 / 2)},
		{int128Min, 127, minusOne},
		{int128Min, 128, minusOne}, // all sign bits, documented
		{int128Min, 200, minusOne},
		{Int128From64(-7), 1, Int128From64(-4)}, // arithmetic shift rounds toward negative infinity
		{int128Max, 127, zero},
		{int128Max, 128, zero}, // non negative: sign fill is zero
		{Int128From64(1).Lsh(100), 100, Int128From64(1)},
	}
	for _, cse := range rshCases {
		if got := cse.x.Rsh(cse.n); got != cse.want {
			t.Fatalf("Rsh(%+v, %d): got %+v, want %+v", cse.x, cse.n, got, cse.want)
		}
	}

	// the left shift moves the bit pattern like hardware: negative values included
	if minusOne.Lsh(1) != Int128From64(-2) {
		t.Fatal("-1 << 1")
	}
	if minusOne.Lsh(64) != i128(0xFFFFFFFFFFFFFFFF, 0) {
		t.Fatal("-1 << 64")
	}
	if Int128From64(1).Lsh(127) != int128Min {
		t.Fatal("1 << 127")
	}
	if minusOne.Lsh(128) != zero {
		t.Fatal("out of range yields zero, documented")
	}

	// division and modulo: all four sign quadrants, truncation toward zero, the
	// remainder sign follows the dividend, and the documented edge choices
	divCases := []struct {
		x, y, quo, rem Int128
	}{
		{Int128From64(7), Int128From64(3), Int128From64(2), Int128From64(1)},
		{Int128From64(-7), Int128From64(3), Int128From64(-2), Int128From64(-1)},
		{Int128From64(7), Int128From64(-3), Int128From64(-2), Int128From64(1)},
		{Int128From64(-7), Int128From64(-3), Int128From64(2), Int128From64(-1)},
		{minusOne, Int128From64(2), zero, minusOne}, // truncation toward zero, not the floor
		{int128Min, Int128From64(2), i128(0xC000000000000000, 0), zero},
		{int128Min, Int128From64(1), int128Min, zero},
		{int128Min, minusOne, int128Min, zero}, // the one overflowing case wraps, documented
		{int128Max, zero, zero, zero},          // by zero is UNDEFINED: this pins totality, not a value contract
	}
	for _, cse := range divCases {
		if got := cse.x.Div(cse.y); got != cse.quo {
			t.Fatalf("Div(%+v, %+v): got %+v, want %+v", cse.x, cse.y, got, cse.quo)
		}
		if got := cse.x.Mod(cse.y); got != cse.rem {
			t.Fatalf("Mod(%+v, %+v): got %+v, want %+v", cse.x, cse.y, got, cse.rem)
		}
	}

	// unary minus is two's complement negation; Not and the bitwise operations work
	// on the pattern
	if Int128From64(5).Neg() != Int128From64(-5) || Int128From64(-5).Neg() != Int128From64(5) {
		t.Fatal("Neg")
	}
	if int128Min.Neg() != int128Min {
		t.Fatal("-MinInt128 wraps to itself, documented")
	}
	if zero.Not() != minusOne {
		t.Fatal("Not")
	}
	if minusOne.And(Int128From64(5)) != Int128From64(5) {
		t.Fatal("And")
	}
	if zero.Or(minusOne) != minusOne {
		t.Fatal("Or")
	}
	if minusOne.Xor(minusOne) != zero {
		t.Fatal("Xor")
	}

	// IsNegative
	if !minusOne.IsNegative() || !int128Min.IsNegative() || zero.IsNegative() || int128Max.IsNegative() {
		t.Fatal("IsNegative")
	}
}

func TestBitsRequired128(t *testing.T) {
	wrappedBound := int64(-5000000000) // a variable: the constant conversion below would not compile
	cases := []struct {
		min, max Uint128
		want     int
	}{
		{Uint128{}, Uint128{}, 0},
		{Uint128{}, Uint128From64(1), 1},
		{Uint128{}, Uint128From64(255), 8},
		{Uint128{}, Uint128From64(4294967295), 32},
		{Uint128{}, Uint128From64(4294967296), 33},
		{Uint128{}, Uint128From64(0xFFFFFFFFFFFFFFFF), 64},
		// the boundary the 64 bit helper cannot reach: one past a full low lane
		// needs the high lane
		{Uint128{}, u128(1, 0), 65},
		{Uint128{}, Uint128From64(1).Lsh(127), 128},
		{Uint128{}, Uint128{}.Not(), 128},
		// NEGATIVE BOUNDS MUST ARRIVE SIGN EXTENDED, which is what Int128.Uint128
		// does. This is the same 34 bits the 64 bit helper reports for the range.
		{Int128From64(-5000000000).Uint128(), Int128From64(+5000000000).Uint128(), 34},
		// AND THE TRAP IT WOULD BE EASY TO WALK INTO, pinned so nobody "fixes" the
		// conversion: widening an ALREADY WRAPPED uint64 bound zero extends instead
		// of sign extending, so the range comes out just under 2^128 and the field
		// would cost 128 bits instead of 34. Correct arithmetic on the wrong input.
		{Uint128From64(uint64(wrappedBound)), Uint128From64(uint64(int64(+5000000000))), 128},
		// a range wider than 2^127: the subtraction must run in the unsigned domain
		{Uint128From64(1), Uint128{}.Not(), 128},
	}
	for _, cse := range cases {
		if got := BitsRequired128(cse.min, cse.max); got != cse.want {
			t.Fatalf("BitsRequired128(%+v, %+v): got %d, want %d", cse.min, cse.max, got, cse.want)
		}
	}

	// the two helpers must agree wherever the range fits 64 bits, or the wire
	// identity claim in STANDARD.md is false and SerializeInt128 would silently
	// disagree with SerializeInt64
	agree := []struct{ min, max uint64 }{{0, 4294967296}, {0, 1 << 40}}
	for _, cse := range agree {
		if BitsRequired128(Uint128From64(cse.min), Uint128From64(cse.max)) != BitsRequired64(cse.min, cse.max) {
			t.Fatalf("BitsRequired128 disagrees with BitsRequired64 over [%d,%d]", cse.min, cse.max)
		}
	}
}

// The differential tests prove the pair against math/big, operand pair by operand
// pair, using the same fixed seed LCG as the C++ differential test. math/big shares
// no code with the pair, so agreement here is two independent implementations
// agreeing — the Go analogue of the C++ emulated-versus-native differential.

var big128Mod = new(big.Int).Lsh(big.NewInt(1), 128) // 2^128

func bigFromUint128(x Uint128) *big.Int {
	v := new(big.Int).SetUint64(x.Hi)
	v.Lsh(v, 64)
	return v.Or(v, new(big.Int).SetUint64(x.Lo))
}

func bigFromInt128(x Int128) *big.Int {
	v := bigFromUint128(x.Uint128())
	if x.IsNegative() {
		v.Sub(v, big128Mod)
	}
	return v
}

func uint128FromBig(v *big.Int) Uint128 {
	m := new(big.Int).Mod(v, big128Mod) // big.Int.Mod is Euclidean: the result is always in [0, 2^128)
	var buf [16]byte
	m.FillBytes(buf[:]) // big endian; buf indexing keeps this portable to 32 bit platforms, where big.Word is 32 bits
	return Uint128{
		Hi: binary.BigEndian.Uint64(buf[0:8]),
		Lo: binary.BigEndian.Uint64(buf[8:16]),
	}
}

// testLCG is the fixed seed LCG the C++ differential test uses to generate operand
// lanes, with the same multiplier, increment and seed.
type testLCG uint64

func (lcg *testLCG) next() uint64 {
	*lcg = *lcg*6364136223846793005 + 1442695040888963407
	return uint64(*lcg)
}

func TestUint128Differential(t *testing.T) {
	lcg := testLCG(0x123456789ABCDEF0)

	for i := range 400 {
		aHi, aLo := lcg.next(), lcg.next()
		bHi, bLo := lcg.next(), lcg.next()

		// bias some operands toward the interesting edges: empty lanes, saturated
		// lanes, powers of two, equal operands
		switch i % 8 {
		case 1:
			aHi = 0
		case 2:
			aLo = 0
		case 3:
			bHi = 0
		case 4:
			bLo = 0
		case 5:
			aHi, aLo = 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF
		case 6:
			bHi, bLo = 0, uint64(1)<<(i%63) // powers of two divide cleanly
		case 7:
			bHi, bLo = aHi, aLo
		}

		a, b := u128(aHi, aLo), u128(bHi, bLo)
		ba, bb := bigFromUint128(a), bigFromUint128(b)

		check := func(op string, got Uint128, want *big.Int) {
			t.Helper()
			if got != uint128FromBig(want) {
				t.Fatalf("iteration %d: %s(%+v, %+v): got %+v, want %+v", i, op, a, b, got, uint128FromBig(want))
			}
		}

		check("Add", a.Add(b), new(big.Int).Add(ba, bb))
		check("Sub", a.Sub(b), new(big.Int).Sub(ba, bb))
		check("Mul", a.Mul(b), new(big.Int).Mul(ba, bb))
		check("And", a.And(b), new(big.Int).And(ba, bb))
		check("Or", a.Or(b), new(big.Int).Or(ba, bb))
		check("Xor", a.Xor(b), new(big.Int).Xor(ba, bb))
		check("Not", a.Not(), new(big.Int).Sub(new(big.Int).Sub(big128Mod, big.NewInt(1)), ba))
		check("Neg", a.Neg(), new(big.Int).Neg(ba))

		if !b.IsZero() {
			// zero divisors are excluded PERMANENTLY, mirroring the C++ differential:
			// division by zero is undefined on the C++ side (native hardware disagrees
			// with itself there), so agreement is not a property either side can
			// promise. Totality is pinned in TestUint128 instead.
			check("Div", a.Div(b), new(big.Int).Quo(ba, bb))
			check("Mod", a.Mod(b), new(big.Int).Rem(ba, bb))
		}

		shift := uint(bLo % 128) // out of range counts are a documented divergence from big.Int: pinned in TestUint128
		check("Lsh", a.Lsh(shift), new(big.Int).Lsh(ba, shift))
		check("Rsh", a.Rsh(shift), new(big.Int).Rsh(ba, shift))

		if (a.Cmp(b) < 0) != (ba.Cmp(bb) < 0) || (a.Cmp(b) > 0) != (ba.Cmp(bb) > 0) || (a == b) != (ba.Cmp(bb) == 0) {
			t.Fatalf("iteration %d: Cmp(%+v, %+v) disagrees with math/big", i, a, b)
		}
		if a.Len() != ba.BitLen() {
			t.Fatalf("iteration %d: Len(%+v) = %d, math/big says %d", i, a, a.Len(), ba.BitLen())
		}
	}
}

func TestInt128Differential(t *testing.T) {
	// the signed counterpart: the same LCG drives the two's complement layer through
	// math/big, covering all four sign quadrants as the lanes come up negative
	lcg := testLCG(0xFEDCBA9876543210)

	for i := range 400 {
		aHi, aLo := lcg.next(), lcg.next()
		bHi, bLo := lcg.next(), lcg.next()

		switch i % 8 {
		case 1:
			aHi = 0
		case 2:
			aHi = 0xFFFFFFFFFFFFFFFF // small magnitude negatives
		case 3:
			bHi = 0
		case 4:
			bHi = 0xFFFFFFFFFFFFFFFF
		case 5:
			aHi, aLo = 0x8000000000000000, 0 // MinInt128
		case 6:
			bHi, bLo = 0, uint64(1)<<(i%63)
		case 7:
			bHi, bLo = aHi, aLo
		}

		a, b := i128(aHi, aLo), i128(bHi, bLo)
		ba, bb := bigFromInt128(a), bigFromInt128(b)

		check := func(op string, got Int128, want *big.Int) {
			t.Helper()
			if got != uint128FromBig(want).Int128() {
				t.Fatalf("iteration %d: %s(%+v, %+v): got %+v, want %+v", i, op, a, b, got, uint128FromBig(want).Int128())
			}
		}

		check("Add", a.Add(b), new(big.Int).Add(ba, bb))
		check("Sub", a.Sub(b), new(big.Int).Sub(ba, bb))
		check("Mul", a.Mul(b), new(big.Int).Mul(ba, bb))
		check("Neg", a.Neg(), new(big.Int).Neg(ba))

		minInt128 := i128(0x8000000000000000, 0)
		if b != (Int128{}) && (a != minInt128 || b != Int128From64(-1)) {
			// the excluded cases: division by zero is undefined (totality pinned in
			// TestInt128), and MinInt128 / -1 is the documented wrap (pinned there too)
			// — big.Int would report the unwrapped 2^127
			check("Div", a.Div(b), new(big.Int).Quo(ba, bb)) // Quo/Rem truncate toward zero: C++ semantics
			check("Mod", a.Mod(b), new(big.Int).Rem(ba, bb))
		}

		shift := uint(bLo % 128)
		check("Lsh", a.Lsh(shift), new(big.Int).Lsh(ba, shift))
		check("Rsh", a.Rsh(shift), new(big.Int).Rsh(ba, shift)) // big.Int Rsh on negatives is arithmetic: same semantics

		if (a.Cmp(b) < 0) != (ba.Cmp(bb) < 0) || (a.Cmp(b) > 0) != (ba.Cmp(bb) > 0) || (a == b) != (ba.Cmp(bb) == 0) {
			t.Fatalf("iteration %d: Cmp(%+v, %+v) disagrees with math/big", i, a, b)
		}
		if a.IsNegative() != (ba.Sign() < 0) {
			t.Fatalf("iteration %d: IsNegative(%+v) disagrees with math/big", i, a)
		}
	}
}
