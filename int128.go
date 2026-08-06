package serialize

import "math/bits"

// 128 bit integers.
//
// Go has no native 128 bit integer type, so this file provides the two-lane pair the
// C++ serialize library defines for compilers without __int128: Uint128 and Int128,
// each a {Lo, Hi} pair of uint64 lanes. The semantics mirror the C++ emulated types
// exactly — two's complement arithmetic that wraps modulo 2^128, with the same
// documented choices where native __int128 has none: shift counts of 128 or more
// yield zero (all sign bits for the arithmetic right shift), and the one overflowing
// signed division, MinInt128 / -1, wraps to MinInt128.
//
// Division or modulo by zero is undefined behavior in the C++ library, native and
// emulated alike. Values read from a stream are untrusted, so the Go pair keeps the
// emulated implementation's totality: Div, Mod and DivMod with a zero divisor return
// zero rather than panicking, so a caller's mistake can never crash a process on
// packet data. That is an implementation detail and not a contract — callers may not
// rely on the value, and the differential tests exclude zero divisors, because the
// C++ side has no portable behavior there to agree with.

// Uint128 is an unsigned 128 bit integer as two uint64 lanes. The zero value is zero.
//
// Operations are value receivers returning new values, in the style of math/big's
// immutable operands but without allocation. Arithmetic wraps modulo 2^128, exactly
// like the C++ library's unsigned 128 bit types (native and emulated), so the two
// languages agree bit for bit on every operation the wire format needs.
type Uint128 struct {
	Lo uint64 // the low 64 bits. First, matching the little endian layout and the wire order.
	Hi uint64 // the high 64 bits.
}

// Uint128From64 returns the Uint128 with the value of x.
func Uint128From64(x uint64) Uint128 {
	return Uint128{Lo: x}
}

// Uint64 returns the low 64 bits, truncating like a native narrowing conversion.
func (x Uint128) Uint64() uint64 {
	return x.Lo
}

// Int128 reinterprets the bit pattern as a signed 128 bit integer.
func (x Uint128) Int128() Int128 {
	return Int128{Lo: x.Lo, Hi: x.Hi}
}

// IsZero reports whether x is zero.
func (x Uint128) IsZero() bool {
	return x == Uint128{}
}

// Len returns the number of bits required to represent x: floor(log2(x)) + 1,
// and zero for an input of zero.
func (x Uint128) Len() int {
	if x.Hi != 0 {
		return 64 + bits.Len64(x.Hi)
	}
	return bits.Len64(x.Lo)
}

// Cmp compares x and y as unsigned values and returns -1 if x < y, 0 if x == y,
// and +1 if x > y.
func (x Uint128) Cmp(y Uint128) int {
	switch {
	case x.Hi != y.Hi:
		if x.Hi < y.Hi {
			return -1
		}
		return 1
	case x.Lo != y.Lo:
		if x.Lo < y.Lo {
			return -1
		}
		return 1
	}
	return 0
}

// Add returns x + y, wrapping modulo 2^128.
func (x Uint128) Add(y Uint128) Uint128 {
	lo, carry := bits.Add64(x.Lo, y.Lo, 0)
	hi, _ := bits.Add64(x.Hi, y.Hi, carry)
	return Uint128{Lo: lo, Hi: hi}
}

// Sub returns x - y, wrapping modulo 2^128.
func (x Uint128) Sub(y Uint128) Uint128 {
	lo, borrow := bits.Sub64(x.Lo, y.Lo, 0)
	hi, _ := bits.Sub64(x.Hi, y.Hi, borrow)
	return Uint128{Lo: lo, Hi: hi}
}

// Mul returns x * y, wrapping modulo 2^128.
func (x Uint128) Mul(y Uint128) Uint128 {
	// the low 64 x 64 product is computed exactly, then the cross products fold into
	// the high lane modulo 2^64 — the same structure as the C++ emulated multiply
	hi, lo := bits.Mul64(x.Lo, y.Lo)
	hi += x.Lo*y.Hi + x.Hi*y.Lo
	return Uint128{Lo: lo, Hi: hi}
}

// DivMod returns the quotient and remainder of x / y.
//
// Division by zero is undefined (see the file comment): the pair stays total and
// returns zero quotient and zero remainder rather than panicking, which is an
// implementation detail and not a contract.
func (x Uint128) DivMod(y Uint128) (quotient, remainder Uint128) {
	if y.IsZero() {
		return Uint128{}, Uint128{}
	}
	if x.Hi == 0 && y.Hi == 0 {
		return Uint128{Lo: x.Lo / y.Lo}, Uint128{Lo: x.Lo % y.Lo}
	}
	if y.Hi == 0 && x.Hi < y.Lo {
		// the whole quotient fits one 128 by 64 hardware division
		q, r := bits.Div64(x.Hi, x.Lo, y.Lo)
		return Uint128{Lo: q}, Uint128{Lo: r}
	}
	// shift subtract long division over the remaining cases, mirroring the C++
	// emulated DivMod: correctness over speed — the serialization paths never divide
	for i := x.Len() - 1; i >= 0; i-- {
		remainder = remainder.Lsh(1)
		remainder.Lo |= x.Rsh(uint(i)).Lo & 1
		if remainder.Cmp(y) >= 0 {
			remainder = remainder.Sub(y)
			quotient = quotient.Or(Uint128{Lo: 1}.Lsh(uint(i)))
		}
	}
	return quotient, remainder
}

// Div returns x / y. Division by zero returns zero: see DivMod.
func (x Uint128) Div(y Uint128) Uint128 {
	quotient, _ := x.DivMod(y)
	return quotient
}

// Mod returns x % y. Modulo by zero returns zero: see DivMod.
func (x Uint128) Mod(y Uint128) Uint128 {
	_, remainder := x.DivMod(y)
	return remainder
}

// Neg returns -x: two's complement negation, wrapping modulo 2^128.
func (x Uint128) Neg() Uint128 {
	return Uint128{}.Sub(x)
}

// Not returns ^x, complementing both lanes.
func (x Uint128) Not() Uint128 {
	return Uint128{Lo: ^x.Lo, Hi: ^x.Hi}
}

// And returns x & y.
func (x Uint128) And(y Uint128) Uint128 {
	return Uint128{Lo: x.Lo & y.Lo, Hi: x.Hi & y.Hi}
}

// Or returns x | y.
func (x Uint128) Or(y Uint128) Uint128 {
	return Uint128{Lo: x.Lo | y.Lo, Hi: x.Hi | y.Hi}
}

// Xor returns x ^ y.
func (x Uint128) Xor(y Uint128) Uint128 {
	return Uint128{Lo: x.Lo ^ y.Lo, Hi: x.Hi ^ y.Hi}
}

// Lsh returns x << n. Shift counts of 128 or more yield zero, matching the
// documented choice of the C++ emulated types (native shifts that wide are
// undefined behavior in C++).
func (x Uint128) Lsh(n uint) Uint128 {
	// shifting a uint64 lane by 64 is where the lanes trade places, so the half
	// boundary is an explicit branch
	switch {
	case n == 0:
		return x
	case n < 64:
		return Uint128{Lo: x.Lo << n, Hi: x.Hi<<n | x.Lo>>(64-n)}
	case n < 128:
		return Uint128{Hi: x.Lo << (n - 64)}
	}
	return Uint128{}
}

// Rsh returns x >> n, a logical shift. Shift counts of 128 or more yield zero,
// matching the documented choice of the C++ emulated types.
func (x Uint128) Rsh(n uint) Uint128 {
	switch {
	case n == 0:
		return x
	case n < 64:
		return Uint128{Lo: x.Lo>>n | x.Hi<<(64-n), Hi: x.Hi >> n}
	case n < 128:
		return Uint128{Lo: x.Hi >> (n - 64)}
	}
	return Uint128{}
}

// Int128 is a signed two's complement 128 bit integer as two uint64 lanes. The zero
// value is zero.
//
// It is a thin two's complement layer over the unsigned lanes: addition, subtraction,
// multiplication and the bitwise operations produce the same bit patterns as Uint128,
// so they delegate to it. The signed specific pieces are the comparison (the high
// lane compares signed), Rsh (arithmetic: vacated bits fill with the sign), division
// and modulo (C++ truncation toward zero with the remainder sign following the
// dividend), and the sign extending Int128From64 constructor.
type Int128 struct {
	Lo uint64 // the low 64 bits. First, matching the little endian layout and the wire order.
	Hi uint64 // the high 64 bits. The top bit is the sign.
}

// Int128From64 returns the Int128 with the value of x, sign extending into the high
// lane like a native widening conversion.
func Int128From64(x int64) Int128 {
	var hi uint64
	if x < 0 {
		hi = ^uint64(0)
	}
	return Int128{Lo: uint64(x), Hi: hi}
}

// Int64 returns the low 64 bits, wrapping two's complement like a native narrowing
// conversion.
func (x Int128) Int64() int64 {
	return int64(x.Lo)
}

// Uint128 reinterprets the bit pattern as an unsigned 128 bit integer.
func (x Int128) Uint128() Uint128 {
	return Uint128{Lo: x.Lo, Hi: x.Hi}
}

// IsNegative reports whether x is negative.
func (x Int128) IsNegative() bool {
	return x.Hi>>63 != 0
}

// Cmp compares x and y as signed values and returns -1 if x < y, 0 if x == y, and
// +1 if x > y. The high lanes compare signed, the low lanes break ties unsigned.
func (x Int128) Cmp(y Int128) int {
	switch {
	case x.Hi != y.Hi:
		if int64(x.Hi) < int64(y.Hi) {
			return -1
		}
		return 1
	case x.Lo != y.Lo:
		if x.Lo < y.Lo {
			return -1
		}
		return 1
	}
	return 0
}

// Add returns x + y, wrapping two's complement.
func (x Int128) Add(y Int128) Int128 {
	return x.Uint128().Add(y.Uint128()).Int128()
}

// Sub returns x - y, wrapping two's complement.
func (x Int128) Sub(y Int128) Int128 {
	return x.Uint128().Sub(y.Uint128()).Int128()
}

// Mul returns x * y, wrapping two's complement.
func (x Int128) Mul(y Int128) Int128 {
	return x.Uint128().Mul(y.Uint128()).Int128()
}

// DivMod returns the quotient and remainder of x / y with C++ semantics: truncation
// toward zero, the remainder sign following the dividend. The one overflowing case,
// MinInt128 / -1, wraps to MinInt128 quotient and zero remainder — the bit pattern
// native two's complement hardware produces.
//
// Division by zero is undefined (see the file comment): the pair stays total and
// returns zero quotient and zero remainder rather than panicking, which is an
// implementation detail and not a contract.
func (x Int128) DivMod(y Int128) (quotient, remainder Int128) {
	// sign extraction, unsigned division on the magnitudes, then sign application
	dividendMagnitude := x.Uint128()
	if x.IsNegative() {
		dividendMagnitude = dividendMagnitude.Neg()
	}
	divisorMagnitude := y.Uint128()
	if y.IsNegative() {
		divisorMagnitude = divisorMagnitude.Neg()
	}
	unsignedQuotient, unsignedRemainder := dividendMagnitude.DivMod(divisorMagnitude)
	if x.IsNegative() != y.IsNegative() {
		unsignedQuotient = unsignedQuotient.Neg()
	}
	if x.IsNegative() {
		unsignedRemainder = unsignedRemainder.Neg()
	}
	return unsignedQuotient.Int128(), unsignedRemainder.Int128()
}

// Div returns x / y with C++ semantics: see DivMod.
func (x Int128) Div(y Int128) Int128 {
	quotient, _ := x.DivMod(y)
	return quotient
}

// Mod returns x % y with C++ semantics: see DivMod.
func (x Int128) Mod(y Int128) Int128 {
	_, remainder := x.DivMod(y)
	return remainder
}

// Neg returns -x: two's complement negation. -MinInt128 wraps to itself, like native.
func (x Int128) Neg() Int128 {
	return x.Uint128().Neg().Int128()
}

// Not returns ^x, complementing both lanes.
func (x Int128) Not() Int128 {
	return x.Uint128().Not().Int128()
}

// And returns x & y.
func (x Int128) And(y Int128) Int128 {
	return x.Uint128().And(y.Uint128()).Int128()
}

// Or returns x | y.
func (x Int128) Or(y Int128) Int128 {
	return x.Uint128().Or(y.Uint128()).Int128()
}

// Xor returns x ^ y.
func (x Int128) Xor(y Int128) Int128 {
	return x.Uint128().Xor(y.Uint128()).Int128()
}

// Lsh returns x << n: a logical shift of the bit pattern, matching what native two's
// complement hardware does. Shift counts of 128 or more yield zero, matching the
// documented choice of the C++ emulated types.
func (x Int128) Lsh(n uint) Int128 {
	return x.Uint128().Lsh(n).Int128()
}

// Rsh returns x >> n: an arithmetic shift, the vacated high bits filling with the
// sign. Shift counts of 128 or more yield all sign bits — 0 for non negative values,
// -1 for negative ones — the limit of shifting further, matching the documented
// choice of the C++ emulated types.
func (x Int128) Rsh(n uint) Int128 {
	if n >= 128 {
		if x.IsNegative() {
			return Int128From64(-1)
		}
		return Int128{}
	}
	result := x.Uint128().Rsh(n)
	if x.IsNegative() && n > 0 {
		result = result.Or(Uint128{}.Not().Lsh(128 - n))
	}
	return result.Int128()
}
