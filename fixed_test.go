package serialize

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

// Stream tests for the fixed point and 128 bit serialization, mirroring
// test_serialize_uint128, test_serialize_int128, test_serialize_fixed,
// test_serialize_fixed_validation, test_serialize_fixed_matches_int64 and
// test_serialize_fixed_wide in the C++ serialize library. The golden byte pins are
// derived independently from STANDARD.md's stated rules (offset encoding over raw
// bounds, 32 bit groups from least significant upward, uint128 as two 64 bit halves
// low first) and agree with the C++ library's own pinned bytes.

func TestSerializeUint128(t *testing.T) {
	// round trips across the value patterns: zero, max, each half alone, alternating
	// bits, distinct halves
	values := []Uint128{
		{},
		u128(0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF),
		u128(0xFFFFFFFFFFFFFFFF, 0),                  // high half only
		u128(0, 0xFFFFFFFFFFFFFFFF),                  // low half only
		u128(0xAAAAAAAAAAAAAAAA, 0x5555555555555555), // alternating bits
		u128(0x0123456789ABCDEF, 0xFEDCBA9876543210), // distinct halves
	}

	for _, value := range values {
		buffer := make([]byte, 16)

		writeStream := NewWriteStream(buffer)
		written := value
		if err := writeStream.SerializeUint128(&written); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		measureStream := NewMeasureStream()
		measured := value
		if err := measureStream.SerializeUint128(&measured); err != nil {
			t.Fatal(err)
		}
		if measureStream.BitsProcessed() != writeStream.BitsProcessed() || writeStream.BitsProcessed() != 128 {
			t.Fatalf("expected 128 bits, wrote %d, measured %d", writeStream.BitsProcessed(), measureStream.BitsProcessed())
		}

		readStream := NewReadStream(writeStream.Data())
		var readBack Uint128
		if err := readStream.SerializeUint128(&readBack); err != nil {
			t.Fatal(err)
		}
		if readBack != value {
			t.Fatalf("round trip mismatch: wrote %+v, read %+v", value, readBack)
		}
	}

	// cross form consistency: SerializeUint128 must be byte identical to two
	// SerializeUint64 calls on the halves, low half first. This is the portability
	// story: an implementation without a 128 bit type reproduces the wire exactly
	// with two 64 bit operations.
	{
		value := u128(0x0123456789ABCDEF, 0xFEDCBA9876543210)

		uint128Buffer := make([]byte, 16)
		uint128Stream := NewWriteStream(uint128Buffer)
		v := value
		uint128Stream.SerializeUint128(&v)
		uint128Stream.Flush()

		halvesBuffer := make([]byte, 16)
		halvesStream := NewWriteStream(halvesBuffer)
		lowHalf, highHalf := value.Lo, value.Hi
		halvesStream.SerializeUint64(&lowHalf)
		halvesStream.SerializeUint64(&highHalf)
		halvesStream.Flush()

		if uint128Stream.BitsProcessed() != halvesStream.BitsProcessed() || !bytes.Equal(uint128Buffer, halvesBuffer) {
			t.Fatalf("uint128 differs from two uint64 halves:\nuint128 %x\nhalves  %x", uint128Buffer, halvesBuffer)
		}
	}

	// golden pin: the wire format for a uint128 is its 16 bytes in little endian
	// order, low half first. Pinned forever. The expected bytes are derived from
	// STANDARD.md's stated rule and match the C++ library's golden_uint128_bytes
	// verbatim, so this pin is Go, C++ and the document agreeing.
	{
		goldenUint128Bytes := []byte{
			0x10, 0x32, 0x54, 0x76, 0x98, 0xBA, 0xDC, 0xFE,
			0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01,
		}
		goldenValue := u128(0x0123456789ABCDEF, 0xFEDCBA9876543210)

		buffer := make([]byte, 16)
		writeStream := NewWriteStream(buffer)
		written := goldenValue
		if err := writeStream.SerializeUint128(&written); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()
		if writeStream.BytesProcessed() != 16 || !bytes.Equal(writeStream.Data(), goldenUint128Bytes) {
			t.Fatalf("uint128 golden mismatch:\nexpected %x\ngot      %x", goldenUint128Bytes, writeStream.Data())
		}

		readStream := NewReadStream(goldenUint128Bytes)
		var readBack Uint128
		if err := readStream.SerializeUint128(&readBack); err != nil {
			t.Fatal(err)
		}
		if readBack != goldenValue {
			t.Fatalf("uint128 golden decode mismatch: %+v", readBack)
		}
	}

	// a truncated buffer must be refused rather than read past the end
	{
		readStream := NewReadStream(make([]byte, 15))
		var value Uint128
		if err := readStream.SerializeUint128(&value); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}
}

func TestSerializeInt128(t *testing.T) {
	// 1. WIRE IDENTITY WITH SerializeInt64 wherever the range fits 64 bits. This is
	//    what lets a schema widen a field without a wire change, so it is pinned by
	//    byte comparison rather than assumed.
	{
		const min64, max64 = int64(-5000000000), int64(+5000000000)
		values := []int64{min64, min64 + 1, -1, 0, +1, 4123456789, max64 - 1, max64}

		for _, value := range values {
			buffer128 := make([]byte, 32)
			w128 := NewWriteStream(buffer128)
			v128 := Int128From64(value)
			if err := w128.SerializeInt128(&v128, Int128From64(min64), Int128From64(max64)); err != nil {
				t.Fatal(err)
			}
			w128.Flush()

			buffer64 := make([]byte, 32)
			w64 := NewWriteStream(buffer64)
			v64 := value
			if err := w64.SerializeInt64(&v64, min64, max64); err != nil {
				t.Fatal(err)
			}
			w64.Flush()

			if w128.BitsProcessed() != w64.BitsProcessed() || !bytes.Equal(buffer128, buffer64) {
				t.Fatalf("value %d: int128 wire differs from int64 wire:\nint128 %x\nint64  %x", value, buffer128, buffer64)
			}

			readStream := NewReadStream(buffer128)
			var readBack Int128
			if err := readStream.SerializeInt128(&readBack, Int128From64(min64), Int128From64(max64)); err != nil {
				t.Fatal(err)
			}
			if readBack != Int128From64(value) {
				t.Fatalf("value %d: round trip mismatch: %+v", value, readBack)
			}
		}
	}

	// 2. the wide bands the 64 bit path cannot express at all: three group and four
	//    group ranges, and the offset math must run in the unsigned domain
	{
		wideMin := Int128From64(1).Lsh(100).Neg()
		wideMax := Int128From64(1).Lsh(100)
		values := []Int128{
			wideMin, wideMin.Add(Int128From64(1)), Int128From64(-1), {}, Int128From64(1),
			Int128From64(1).Lsh(99), wideMax.Sub(Int128From64(1)), wideMax,
		}

		for _, value := range values {
			buffer := make([]byte, 32)
			writeStream := NewWriteStream(buffer)
			written := value
			if err := writeStream.SerializeInt128(&written, wideMin, wideMax); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()
			if writeStream.BitsProcessed() != 102 { // BitsRequired128( -2^100, 2^100 ) == 102
				t.Fatalf("expected 102 bits, got %d", writeStream.BitsProcessed())
			}

			readStream := NewReadStream(writeStream.Data())
			var readBack Int128
			if err := readStream.SerializeInt128(&readBack, wideMin, wideMax); err != nil {
				t.Fatal(err)
			}
			if readBack != value {
				t.Fatalf("round trip mismatch: wrote %+v, read %+v", value, readBack)
			}
		}
	}

	// 3. the full 128 bit range: every group full, and the range is wider than 2^127
	{
		fullMin := Uint128From64(1).Lsh(127).Int128()       // MinInt128
		fullMax := Uint128From64(1).Lsh(127).Not().Int128() // MaxInt128
		values := []Int128{
			fullMin, fullMin.Add(Int128From64(1)), Int128From64(-1), {}, Int128From64(1),
			fullMax.Sub(Int128From64(1)), fullMax,
		}

		for _, value := range values {
			buffer := make([]byte, 32)
			writeStream := NewWriteStream(buffer)
			written := value
			if err := writeStream.SerializeInt128(&written, fullMin, fullMax); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()
			if writeStream.BitsProcessed() != 128 {
				t.Fatalf("expected 128 bits, got %d", writeStream.BitsProcessed())
			}

			readStream := NewReadStream(writeStream.Data())
			var readBack Int128
			if err := readStream.SerializeInt128(&readBack, fullMin, fullMax); err != nil {
				t.Fatal(err)
			}
			if readBack != value {
				t.Fatalf("round trip mismatch: wrote %+v, read %+v", value, readBack)
			}
		}
	}

	// 4. the measure stream must agree with the write stream exactly, at every group
	//    width
	{
		cases := []struct{ value, min, max Int128 }{
			{Int128{}, Int128{}, Int128From64(255)},
			{Int128From64(7), Int128From64(-5000000000), Int128From64(+5000000000)},
			{Int128From64(1), Int128From64(1).Lsh(100).Neg(), Int128From64(1).Lsh(100)},
			{Int128{}, Uint128From64(1).Lsh(127).Int128(), Uint128From64(1).Lsh(127).Not().Int128()},
		}

		for _, cse := range cases {
			buffer := make([]byte, 32)
			writeStream := NewWriteStream(buffer)
			written := cse.value
			if err := writeStream.SerializeInt128(&written, cse.min, cse.max); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()

			measureStream := NewMeasureStream()
			measured := cse.value
			if err := measureStream.SerializeInt128(&measured, cse.min, cse.max); err != nil {
				t.Fatal(err)
			}
			if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
				t.Fatalf("measure %d bits, write %d bits", measureStream.BitsProcessed(), writeStream.BitsProcessed())
			}
		}
	}

	// 5. a value outside the bounds must be REFUSED on read. The bit count is
	//    identical for both bound pairs here, so the reader consumes the same bits
	//    and the range check is what convicts it — proving the refusal, not just the
	//    absence of a crash.
	{
		buffer := make([]byte, 32)
		writeStream := NewWriteStream(buffer)
		written := Int128From64(255)
		if err := writeStream.SerializeInt128(&written, Int128{}, Int128From64(255)); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		if BitsRequired128(Uint128{}, Uint128From64(200)) != 8 {
			t.Fatal("test premise: both bound pairs must cost 8 bits")
		}

		readStream := NewReadStream(buffer)
		var readBack Int128
		if err := readStream.SerializeInt128(&readBack, Int128{}, Int128From64(200)); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}

	// 6. a truncated buffer must be refused rather than read past the end
	{
		readStream := NewReadStream(make([]byte, 4)) // 32 bits available, 128 required
		var readBack Int128
		fullMin := Uint128From64(1).Lsh(127).Int128()
		fullMax := Uint128From64(1).Lsh(127).Not().Int128()
		if err := readStream.SerializeInt128(&readBack, fullMin, fullMax); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}

	// 7. THE GOLDEN PIN. The expected bytes are derived from STANDARD.md's stated
	//    rule — offset in the unsigned 128 bit domain, 32 bit groups from least
	//    significant upward — and match the meaningful 9 bytes of the C++ library's
	//    golden_int128_bytes verbatim (the C++ pin carries three trailing zero bytes
	//    of dword flush padding beyond the 72 bit payload). Bounds of +/- 2^70 need
	//    72 bits, which is the THREE GROUP structure: 32, 32, then 8.
	{
		goldenInt128Bytes := []byte{
			0x11, 0x32, 0x54, 0x76, 0x98, 0xBA, 0xDC, 0xFE, 0x3F,
		}
		goldenMin := Int128From64(1).Lsh(70).Neg()
		goldenMax := Int128From64(1).Lsh(70)
		goldenValue := Int128From64(0x0123456789ABCDEF).Neg()

		buffer := make([]byte, 16)
		writeStream := NewWriteStream(buffer)
		written := goldenValue
		if err := writeStream.SerializeInt128(&written, goldenMin, goldenMax); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()
		if writeStream.BitsProcessed() != 72 || !bytes.Equal(writeStream.Data(), goldenInt128Bytes) {
			t.Fatalf("int128 golden mismatch (%d bits):\nexpected %x\ngot      %x", writeStream.BitsProcessed(), goldenInt128Bytes, writeStream.Data())
		}

		readStream := NewReadStream(goldenInt128Bytes)
		var readBack Int128
		if err := readStream.SerializeInt128(&readBack, goldenMin, goldenMax); err != nil {
			t.Fatal(err)
		}
		if readBack != goldenValue {
			t.Fatalf("int128 golden decode mismatch: %+v", readBack)
		}
	}

	// 8. a write of a value outside [min,max] must be refused with an error, not
	//    smuggled — the Go port checks on write where C++ has debug asserts
	{
		buffer := make([]byte, 32)
		writeStream := NewWriteStream(buffer)
		written := Int128From64(300)
		if err := writeStream.SerializeInt128(&written, Int128{}, Int128From64(255)); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}
}

// fixedConfig is one fixed point configuration: the Q format and the whole unit
// bounds, mirroring the compile time parameters of the C++ serialize_fixed call site.
type fixedConfig struct {
	integerBits  int
	fractionBits int
	min, max     int64
}

// checkFixedRoundTrip64 writes a raw value through the configuration, requires the
// measure stream to agree with the write stream exactly (fixed point serialization
// involves no alignment, so measure is exact, not just conservative), and reads it
// back bit for bit.
func checkFixedRoundTrip64(t *testing.T, cfg fixedConfig, rawValue int64) {
	t.Helper()

	buffer := make([]byte, 32)
	writeStream := NewWriteStream(buffer)
	written := rawValue
	if err := writeStream.SerializeFixed64(&written, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %d: write failed: %v", cfg, rawValue, err)
	}
	writeStream.Flush()

	measureStream := NewMeasureStream()
	measured := rawValue
	if err := measureStream.SerializeFixed64(&measured, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %d: measure failed: %v", cfg, rawValue, err)
	}
	if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
		t.Fatalf("%+v raw %d: measure %d bits, write %d bits", cfg, rawValue, measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}

	readStream := NewReadStream(writeStream.Data())
	var readBack int64
	if err := readStream.SerializeFixed64(&readBack, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %d: read failed: %v", cfg, rawValue, err)
	}
	if readBack != rawValue {
		t.Fatalf("%+v: round trip mismatch: wrote %d, read %d", cfg, rawValue, readBack)
	}
}

// checkFixedCases64 runs the full case list of the C++ check_fixed_cases through a
// configuration: exact raw bounds, one raw step inside each, whole unit values
// inside each bound, all fraction bits set, the middle of the range, and
// zero / +1.0 / -1.0 units where the bounds allow them. Raw math runs in the
// unsigned domain so unsigned Q formats with raw values past 2^63 work as bit
// patterns.
func checkFixedCases64(t *testing.T, cfg fixedConfig) {
	t.Helper()

	oneUnit := uint64(1) << cfg.fractionBits
	rawMin := uint64(cfg.min) << cfg.fractionBits
	rawMax := uint64(cfg.max) << cfg.fractionBits

	checkFixedRoundTrip64(t, cfg, int64(rawMin))
	checkFixedRoundTrip64(t, cfg, int64(rawMax))
	checkFixedRoundTrip64(t, cfg, int64(rawMin+1))
	checkFixedRoundTrip64(t, cfg, int64(rawMax-1))

	// whole unit values one unit inside each bound
	checkFixedRoundTrip64(t, cfg, int64(rawMin+oneUnit))
	checkFixedRoundTrip64(t, cfg, int64(rawMax-oneUnit))

	// a value with every fraction bit set
	checkFixedRoundTrip64(t, cfg, int64(rawMin+oneUnit-1))

	// the middle of the range
	checkFixedRoundTrip64(t, cfg, int64(rawMin+(rawMax-rawMin)/2))

	// zero, one and minus one whole units, where the bounds allow them
	if cfg.min <= 0 && cfg.max >= 0 {
		checkFixedRoundTrip64(t, cfg, 0)
	}
	if cfg.min <= 1 && cfg.max >= 1 {
		checkFixedRoundTrip64(t, cfg, int64(oneUnit))
	}
	if cfg.min <= -1 && cfg.max >= -1 {
		checkFixedRoundTrip64(t, cfg, -int64(oneUnit))
	}
}

// fixedMatrix64 is the storage x Q format matrix of the C++ test suite. Go storage
// is always int64, so the C++ int16/int32/uint storage configurations collapse onto
// the same method: the wire depends only on the fraction bits and the bounds.
var fixedMatrix64 = []fixedConfig{
	{8, 8, -100, +100},
	{12, 4, -2000, +2000},
	{16, 16, -30000, +30000},
	{24, 8, -8000000, +8000000},
	{32, 0, -100000, +100000}, // pure integer Q: fractionBits == 0 is legal
	{48, 16, -100000000000, +100000000000},
	{32, 32, -1000000, +1000000},
	{64, 0, -5000000000, +5000000000}, // pure integer Q at full width
	{16, 0, 0, 60000},                 // unsigned formats: capacity [0, 2^integerBits - 1]
	{16, 16, 0, 60000},
	{48, 16, 0, 1000000000},
	{16, 16, 0, 1},        // single unit range: the whole wire is the fractional part
	{48, 16, -3, +100000}, // asymmetric bounds
}

func TestSerializeFixed(t *testing.T) {
	for _, cfg := range fixedMatrix64 {
		checkFixedCases64(t, cfg)
	}

	// the wire cost is a constant of the configuration. Pin a few
	costCases := []struct {
		cfg  fixedConfig
		raw  int64
		bits int64
	}{
		{fixedConfig{48, 16, -100000, +100000}, 12345*65536 + 32768, 34}, // 12345.5 in Q48.16: 200000 << 16 raw values needs 34 bits
		{fixedConfig{16, 16, 0, 1}, 65536 / 2, 17},                       // 0.5 in Q16.16: 1 << 16 raw values needs 17 bits
		{fixedConfig{8, 8, -100, +100}, -832, 16},                        // -3.25 in Q8.8: 200 << 8 raw values needs 16 bits
	}
	for _, cse := range costCases {
		buffer := make([]byte, 16)
		stream := NewWriteStream(buffer)
		value := cse.raw
		if err := stream.SerializeFixed64(&value, cse.cfg.integerBits, cse.cfg.fractionBits, cse.cfg.min, cse.cfg.max); err != nil {
			t.Fatal(err)
		}
		if stream.BitsProcessed() != cse.bits {
			t.Fatalf("%+v: expected %d bits, got %d", cse.cfg, cse.bits, stream.BitsProcessed())
		}
	}
}

// checkFixedRejects64 recomputes the wire parameters independently of the codec,
// then hand builds a stream encoding an offset of exactly rawRange + 1: one raw
// step past rawMax, smuggled into the bit headroom. Reads must reject it.
func checkFixedRejects64(t *testing.T, cfg fixedConfig) {
	t.Helper()

	rawRange := (uint64(cfg.max) << cfg.fractionBits) - (uint64(cfg.min) << cfg.fractionBits)
	numBits := BitsRequired64(0, rawRange)

	maxEncodable := ^uint64(0)
	if numBits < 64 {
		maxEncodable = uint64(1)<<numBits - 1
	}
	if rawRange == maxEncodable {
		return // no headroom: every encoding decodes in range for this configuration
	}

	smuggled := rawRange + 1

	buffer := make([]byte, 16)
	writeStream := NewWriteStream(buffer)
	if numBits <= 32 {
		lo := uint32(smuggled)
		writeStream.SerializeBits(&lo, numBits)
	} else {
		lo, hi := uint32(smuggled), uint32(smuggled>>32)
		writeStream.SerializeBits(&lo, 32)
		writeStream.SerializeBits(&hi, numBits-32)
	}
	writeStream.Flush()

	readStream := NewReadStream(buffer)
	var value int64
	if err := readStream.SerializeFixed64(&value, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("%+v: expected ErrValueOutOfRange for smuggled offset, got %v", cfg, err)
	}
	if value != 0 {
		t.Fatalf("%+v: failed read must leave the value unmodified, got %d", cfg, value)
	}
}

func TestSerializeFixedValidation(t *testing.T) {
	// a malicious packet can smuggle a raw value past rawMax into the bit headroom
	// of the offset encoding. Reads must reject one raw step past the top of the
	// range, on every configuration in the matrix that has headroom.
	for _, cfg := range fixedMatrix64 {
		checkFixedRejects64(t, cfg)
	}

	// reads past the end of the buffer must fail cleanly
	{
		readStream := NewReadStream(make([]byte, 2))
		var value int64
		if err := readStream.SerializeFixed64(&value, 48, 16, -100000000000, +100000000000); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}

	// a write of a raw value outside the bounds must be refused with an error, not
	// smuggled — the Go port checks on write where C++ has debug asserts
	{
		buffer := make([]byte, 16)
		writeStream := NewWriteStream(buffer)
		value := int64(101 * 256) // 101.0 in Q8.8, past +100 whole units
		if err := writeStream.SerializeFixed64(&value, 8, 8, -100, +100); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}
}

func TestSerializeFixedMatchesInt64(t *testing.T) {
	// for any Q format the wire is SerializeInt64 of the raw value over the raw
	// bounds: fixed point adds no wire structure, only the scaling convention.
	// Sweep values and require identical bytes and identical bit counts: this
	// equivalence binds the new path to the proven one.
	cases := []struct {
		cfg    fixedConfig
		values []int64
	}{
		// fractionBits == 0 is pure integer Q, > 32 bit range: the two group path
		{fixedConfig{64, 0, -5000000000, +5000000000},
			[]int64{-5000000000, -4999999999, -1, 0, +1, 12345678, 4999999999, 5000000000}},
		// <= 32 bit range: the single group path
		{fixedConfig{32, 0, -100000, +100000},
			[]int64{-100000, -99999, -1, 0, +1, 54321, 99999, 100000}},
		// a fractional Q format: the raw values carry fraction bits
		{fixedConfig{16, 16, -30000, +30000},
			[]int64{-30000 * 65536, -(3*65536 + 16384), 0, 65536 / 2, 12345*65536 + 1, 30000 * 65536}},
	}

	for _, cse := range cases {
		rawMin := cse.cfg.min << cse.cfg.fractionBits
		rawMax := cse.cfg.max << cse.cfg.fractionBits

		for _, value := range cse.values {
			fixedBuffer := make([]byte, 16)
			fixedStream := NewWriteStream(fixedBuffer)
			fixedValue := value
			if err := fixedStream.SerializeFixed64(&fixedValue, cse.cfg.integerBits, cse.cfg.fractionBits, cse.cfg.min, cse.cfg.max); err != nil {
				t.Fatal(err)
			}
			fixedStream.Flush()

			int64Buffer := make([]byte, 16)
			int64Stream := NewWriteStream(int64Buffer)
			int64Value := value
			if err := int64Stream.SerializeInt64(&int64Value, rawMin, rawMax); err != nil {
				t.Fatal(err)
			}
			int64Stream.Flush()

			if fixedStream.BitsProcessed() != int64Stream.BitsProcessed() || !bytes.Equal(fixedBuffer, int64Buffer) {
				t.Fatalf("%+v value %d: fixed wire differs from int64 wire:\nfixed %x\nint64 %x", cse.cfg, value, fixedBuffer, int64Buffer)
			}
		}
	}
}

// checkFixedRoundTrip128 is the wide counterpart of checkFixedRoundTrip64.
func checkFixedRoundTrip128(t *testing.T, cfg fixedConfig, rawValue Int128) {
	t.Helper()

	buffer := make([]byte, 32)
	writeStream := NewWriteStream(buffer)
	written := rawValue
	if err := writeStream.SerializeFixed128(&written, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %+v: write failed: %v", cfg, rawValue, err)
	}
	writeStream.Flush()

	measureStream := NewMeasureStream()
	measured := rawValue
	if err := measureStream.SerializeFixed128(&measured, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %+v: measure failed: %v", cfg, rawValue, err)
	}
	if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
		t.Fatalf("%+v raw %+v: measure %d bits, write %d bits", cfg, rawValue, measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}

	readStream := NewReadStream(writeStream.Data())
	var readBack Int128
	if err := readStream.SerializeFixed128(&readBack, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); err != nil {
		t.Fatalf("%+v raw %+v: read failed: %v", cfg, rawValue, err)
	}
	if readBack != rawValue {
		t.Fatalf("%+v: round trip mismatch: wrote %+v, read %+v", cfg, rawValue, readBack)
	}
}

// checkFixedCases128 runs the wide configurations through the same case list.
func checkFixedCases128(t *testing.T, cfg fixedConfig) {
	t.Helper()

	oneUnit := Int128From64(1).Lsh(uint(cfg.fractionBits))
	rawMin := Int128From64(cfg.min).Lsh(uint(cfg.fractionBits))
	rawMax := Int128From64(cfg.max).Lsh(uint(cfg.fractionBits))
	one := Int128From64(1)

	checkFixedRoundTrip128(t, cfg, rawMin)
	checkFixedRoundTrip128(t, cfg, rawMax)
	checkFixedRoundTrip128(t, cfg, rawMin.Add(one))
	checkFixedRoundTrip128(t, cfg, rawMax.Sub(one))

	// whole unit values one unit inside each bound
	checkFixedRoundTrip128(t, cfg, rawMin.Add(oneUnit))
	checkFixedRoundTrip128(t, cfg, rawMax.Sub(oneUnit))

	// a value with every fraction bit set
	checkFixedRoundTrip128(t, cfg, rawMin.Add(oneUnit).Sub(one))

	// the middle of the range, computed in the unsigned domain
	middle := rawMin.Uint128().Add(rawMax.Uint128().Sub(rawMin.Uint128()).Rsh(1)).Int128()
	checkFixedRoundTrip128(t, cfg, middle)

	// zero, one and minus one whole units, where the bounds allow them
	if cfg.min <= 0 && cfg.max >= 0 {
		checkFixedRoundTrip128(t, cfg, Int128{})
	}
	if cfg.min <= 1 && cfg.max >= 1 {
		checkFixedRoundTrip128(t, cfg, oneUnit)
	}
	if cfg.min <= -1 && cfg.max >= -1 {
		checkFixedRoundTrip128(t, cfg, oneUnit.Neg())
	}
}

// fixedMatrix128 is the wide matrix of the C++ test suite: Q112.16 with a raw range
// past 64 bits (three groups on the wire), Q112.16 with a small range (a single
// group on wide storage), Q64.64 (the fraction alone spans 64 bits), Q64.64 over
// the full unit range (128 bits on the wire, four groups), the unsigned wide case,
// and the 33..64 bit two group band: both boundaries exactly, plus the example's
// own Q112.16 ±1e11 shape.
var fixedMatrix128 = []fixedConfig{
	{112, 16, -1152921504606846976, +1152921504606846976}, // ±2^60 units: 78 bits on the wire
	{112, 16, -2, +2},
	{64, 64, -1000, +1000},
	{64, 64, math.MinInt64, math.MaxInt64},        // full unit range: 128 bits on the wire
	{112, 16, 0, 2305843009213693952},             // 2^61 units, unsigned
	{112, 16, -32768, +32768},                     // 33 bits: the two group band's low edge
	{112, 16, -100000000000, +100000000000},       // 54 bits: the example's shape
	{112, 16, -140737488355328, +140737488355327}, // 64 bits: the band's high edge
}

func TestSerializeFixedWide(t *testing.T) {
	for _, cfg := range fixedMatrix128 {
		checkFixedCases128(t, cfg)
	}

	// the wire cost is a constant of the configuration, wide paths included. Pin a few
	costCases := []struct {
		cfg  fixedConfig
		raw  Int128
		bits int64
	}{
		{fixedConfig{112, 16, -1152921504606846976, +1152921504606846976}, Int128From64(12345 * 65536), 78},          // 2^61 << 16 raw values needs 78 bits
		{fixedConfig{64, 64, math.MinInt64, math.MaxInt64}, Int128{}, 128},                                           // the full unit range costs the full storage width
		{fixedConfig{112, 16, -100000000000, +100000000000}, Int128From64(12345678901).Mul(Int128From64(65536)), 54}, // the example's shape, inside the two group band
		{fixedConfig{112, 16, -32768, +32768}, Int128{}, 33},                                                         // the band's low edge
		{fixedConfig{112, 16, -140737488355328, +140737488355327}, Int128{}, 64},                                     // the band's high edge
	}
	for _, cse := range costCases {
		buffer := make([]byte, 24)
		stream := NewWriteStream(buffer)
		value := cse.raw
		if err := stream.SerializeFixed128(&value, cse.cfg.integerBits, cse.cfg.fractionBits, cse.cfg.min, cse.cfg.max); err != nil {
			t.Fatal(err)
		}
		if stream.BitsProcessed() != cse.bits {
			t.Fatalf("%+v: expected %d bits, got %d", cse.cfg, cse.bits, stream.BitsProcessed())
		}
	}

	// one raw step past rawMax must be rejected on read, through every group
	// structure — the 33..64 bit two group band included
	for _, cfg := range fixedMatrix128 {
		checkFixedRejects128(t, cfg)
	}

	// reads past the end of the buffer must fail cleanly
	{
		readStream := NewReadStream(make([]byte, 4))
		var value Int128
		if err := readStream.SerializeFixed128(&value, 112, 16, -1152921504606846976, +1152921504606846976); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}
}

// checkFixedRejects128 is the wide counterpart of checkFixedRejects64: it
// recomputes the wire parameters independently of the codec, hand builds a stream
// encoding an offset of exactly rawRange + 1, and requires the read to reject it.
func checkFixedRejects128(t *testing.T, cfg fixedConfig) {
	t.Helper()

	rawMin := Int128From64(cfg.min).Lsh(uint(cfg.fractionBits)).Uint128()
	rawMax := Int128From64(cfg.max).Lsh(uint(cfg.fractionBits)).Uint128()
	rawRange := rawMax.Sub(rawMin)

	numBits := rawRange.Len()

	maxEncodable := Uint128{}.Not()
	if numBits < 128 {
		maxEncodable = Uint128From64(1).Lsh(uint(numBits)).Sub(Uint128From64(1))
	}
	if rawRange == maxEncodable {
		return // no headroom: every encoding decodes in range for this configuration
	}

	smuggled := rawRange.Add(Uint128From64(1))

	buffer := make([]byte, 32)
	writeStream := NewWriteStream(buffer)
	bitsLeft := numBits
	for bitsLeft > 0 {
		groupBits := min(bitsLeft, 32)
		group := uint32(smuggled.Lo)
		writeStream.SerializeBits(&group, groupBits)
		smuggled = smuggled.Rsh(uint(groupBits))
		bitsLeft -= groupBits
	}
	writeStream.Flush()

	readStream := NewReadStream(buffer)
	var value Int128
	if err := readStream.SerializeFixed128(&value, cfg.integerBits, cfg.fractionBits, cfg.min, cfg.max); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("%+v: expected ErrValueOutOfRange for smuggled offset, got %v", cfg, err)
	}
	if value != (Int128{}) {
		t.Fatalf("%+v: failed read must leave the value unmodified, got %+v", cfg, value)
	}
}
