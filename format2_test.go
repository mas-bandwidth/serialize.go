package serialize

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// The format 2.0 rules this port carries on top of the corpus: the reason list as
// spelled, the write side of the finite rule, the compressed float declaration check,
// and the writer's and measure's end. The read side of every 2.0 rule is pinned by the
// corpus and its negative controls (conformance_test.go, controls_test.go); what is
// here is what no read of a vector can observe.
//
// Each test was run red against the 1.1 behavior planted back before it was kept: the
// float writes without the finite check, the compressed float writer without the
// declaration check, and a Refusal whose String spelled the Go constant name.

// TestRefusalNames pins the closed list exactly as STANDARD.md spells it, because the
// spelling is the cross-language contract, not decoration.
func TestRefusalNames(t *testing.T) {
	want := []struct {
		refusal Refusal
		name    string
	}{
		{ErrPastEnd, "past_end"},
		{ErrValueOutOfRange, "value_out_of_range"},
		{ErrNonzeroPadding, "nonzero_padding"},
		{ErrTrailingData, "trailing_data"},
		{ErrNonFiniteFloat, "non_finite_float"},
		{ErrInvalidUTF8, "invalid_utf8"},
		{ErrEmbeddedNul, "embedded_nul"},
		{ErrNonCanonical, "non_canonical"},
	}
	if len(want) != len(refusalNames)-1 {
		t.Fatalf("the closed list holds %d reasons, this test names %d", len(refusalNames)-1, len(want))
	}
	for _, w := range want {
		if got := w.refusal.String(); got != w.name {
			t.Errorf("Refusal %d spells %q, STANDARD.md spells %q", uint8(w.refusal), got, w.name)
		}
		if got := w.refusal.Error(); got != "serialize: "+w.name {
			t.Errorf("Refusal %d errors as %q", uint8(w.refusal), got)
		}
		var err error = w.refusal
		var refusal Refusal
		if !errors.As(err, &refusal) || refusal != w.refusal {
			t.Errorf("errors.As does not recover %s from the error value", w.name)
		}
	}
	if got := Refusal(0).String(); got != "" {
		t.Errorf("the zero Refusal names %q, want no reason", got)
	}
	if got := Refusal(200).String(); got != "" {
		t.Errorf("a Refusal outside the list names %q, want no reason", got)
	}
}

// TestFloatWriteFinite: the format carries finite values only, so a writer handed NaN or
// an infinity refuses it, as it refuses an int outside its range, and writes nothing;
// the measure agrees. Negative zero and the subnormals are finite and travel bit exact.
func TestFloatWriteFinite(t *testing.T) {
	nonFinite32 := []uint32{0x7F800000, 0xFF800000, 0x7FC00000, 0xFFC00000, 0x7F800001, 0x7FFFFFFF, 0xFFFFFFFF}
	for _, bits := range nonFinite32 {
		value := math.Float32frombits(bits)
		w := NewWriteStream(make([]byte, 16))
		if err := w.SerializeFloat32(&value); !errors.Is(err, ErrNonFiniteFloat) {
			t.Errorf("write float 0x%08X: got %v, want ErrNonFiniteFloat", bits, err)
		}
		if w.BitsProcessed() != 0 {
			t.Errorf("write float 0x%08X wrote %d bits", bits, w.BitsProcessed())
		}
		m := NewMeasureStream()
		if err := m.SerializeFloat32(&value); !errors.Is(err, ErrNonFiniteFloat) {
			t.Errorf("measure float 0x%08X: got %v, want ErrNonFiniteFloat", bits, err)
		}
	}
	nonFinite64 := []uint64{0x7FF0000000000000, 0xFFF0000000000000, 0x7FF8000000000000, 0x7FF0000000000001, 0xFFFFFFFFFFFFFFFF}
	for _, bits := range nonFinite64 {
		value := math.Float64frombits(bits)
		w := NewWriteStream(make([]byte, 16))
		if err := w.SerializeFloat64(&value); !errors.Is(err, ErrNonFiniteFloat) {
			t.Errorf("write double 0x%016X: got %v, want ErrNonFiniteFloat", bits, err)
		}
		if w.BitsProcessed() != 0 {
			t.Errorf("write double 0x%016X wrote %d bits", bits, w.BitsProcessed())
		}
		m := NewMeasureStream()
		if err := m.SerializeFloat64(&value); !errors.Is(err, ErrNonFiniteFloat) {
			t.Errorf("measure double 0x%016X: got %v, want ErrNonFiniteFloat", bits, err)
		}
	}

	// the finite edges round trip bit for bit: negative zero, the smallest and largest
	// subnormals, the largest finite value, each with its sign
	finite32 := []uint32{0x80000000, 0x00000001, 0x807FFFFF, 0x7F7FFFFF, 0xFF7FFFFF}
	finite64 := []uint64{0x8000000000000000, 0x0000000000000001, 0x7FEFFFFFFFFFFFFF}
	for _, bits := range finite32 {
		value := math.Float32frombits(bits)
		w := NewWriteStream(make([]byte, 16))
		if err := w.SerializeFloat32(&value); err != nil {
			t.Fatalf("write float 0x%08X: %v", bits, err)
		}
		w.Flush()
		r := NewReadStream(w.Data())
		var read float32
		if err := r.SerializeFloat32(&read); err != nil || math.Float32bits(read) != bits {
			t.Errorf("float 0x%08X read back 0x%08X (%v)", bits, math.Float32bits(read), err)
		}
	}
	for _, bits := range finite64 {
		value := math.Float64frombits(bits)
		w := NewWriteStream(make([]byte, 16))
		if err := w.SerializeFloat64(&value); err != nil {
			t.Fatalf("write double 0x%016X: %v", bits, err)
		}
		w.Flush()
		r := NewReadStream(w.Data())
		var read float64
		if err := r.SerializeFloat64(&read); err != nil || math.Float64bits(read) != bits {
			t.Errorf("double 0x%016X read back 0x%016X (%v)", bits, math.Float64bits(read), err)
		}
	}
}

// TestCompressedFloatDecodeMustBeFinite: over [3 * 2^103, FLT_MAX] at resolution 2^104
// every operand is finite, and still the top integer decodes to +inf (STANDARD.md, "The
// decode must be finite"). The declaration is non-conforming as a whole, so the writer
// panics on it even for a value that would decode finite; the reader does not panic,
// and refuses the non-finite decode with ErrNonFiniteFloat, destination untouched.
func TestCompressedFloatDecodeMustBeFinite(t *testing.T) {
	min := float32(3 * math.Pow(2, 103))
	max := float32(math.MaxFloat32)
	resolution := float32(math.Pow(2, 104))

	maxIntegerValue, bits, delta := CompressedFloatParams(min, max, resolution)
	if maxIntegerValue != 1<<24-2 || bits != 24 {
		t.Fatalf("the witness declaration derived max integer %d over %d bits, want 2^24-2 over 24", maxIntegerValue, bits)
	}
	if !math.IsInf(float64(min+delta), 1) {
		t.Fatalf("the witness declaration's top decode is %v, want +inf", min+delta)
	}

	for _, value := range []float32{min, max} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("writing %v under a declaration whose top decodes to +inf did not panic", value)
				}
				if message, _ := r.(string); !strings.Contains(message, "not finite") {
					t.Fatalf("unexpected panic: %v", r)
				}
			}()
			w := NewWriteStream(make([]byte, 16))
			v := value
			_ = w.SerializeCompressedFloat32(&v, min, max, resolution)
		}()
	}

	// the reader: the top integer decodes to +inf and is refused, one below it is not
	top := func(integer uint32) []byte {
		w := NewWriteStream(make([]byte, 16))
		w.SerializeBits(&integer, 24)
		w.Flush()
		return w.Data()
	}
	r := NewReadStream(top(maxIntegerValue))
	value := float32(-1)
	if err := r.SerializeCompressedFloat32(&value, min, max, resolution); !errors.Is(err, ErrNonFiniteFloat) {
		t.Fatalf("reading the top integer: got %v, want ErrNonFiniteFloat", err)
	}
	if value != -1 {
		t.Fatalf("the refused read wrote its destination: %v", value)
	}
	r = NewReadStream(top(maxIntegerValue - 1))
	if err := r.SerializeCompressedFloat32(&value, min, max, resolution); err != nil {
		t.Fatalf("reading one below the top integer: %v", err)
	}
	if math.IsInf(float64(value), 0) || math.IsNaN(float64(value)) {
		t.Fatalf("one below the top integer decoded to %v", value)
	}
}

// TestEndWriteAndMeasure: a writer's end emits nothing and a measure's costs nothing,
// so one serialize function carries its end on every stream. On a failed stream the end
// echoes the first failure, as every other operation does.
func TestEndWriteAndMeasure(t *testing.T) {
	w := NewWriteStream(make([]byte, 16))
	v := uint32(5)
	w.SerializeBits(&v, 3)
	if err := w.SerializeEnd(); err != nil {
		t.Fatal(err)
	}
	if w.BitsProcessed() != 3 {
		t.Fatalf("the writer's end moved the stream to bit %d", w.BitsProcessed())
	}
	w.Flush()
	if data := w.Data(); len(data) != 1 || data[0] != 0x05 {
		t.Fatalf("the writer emitted %x, want 05", data)
	}

	m := NewMeasureStream()
	if err := m.SerializeEnd(); err != nil || m.BitsProcessed() != 0 {
		t.Fatalf("the measure's end measured %d bits (%v), want 0", m.BitsProcessed(), err)
	}

	failed := NewWriteStream(make([]byte, 8))
	big := int32(9)
	failed.SerializeInt(&big, 0, 5)
	if err := failed.SerializeEnd(); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("the end on a failed writer gave %v, want the latched ErrValueOutOfRange", err)
	}
	failedMeasure := NewMeasureStream()
	failedMeasure.SerializeInt(&big, 0, 5)
	if err := failedMeasure.SerializeEnd(); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("the end on a failed measure gave %v, want the latched ErrValueOutOfRange", err)
	}

	// the reader: an accepted end leaves the stream exactly consumed, and a zero bit
	// read still succeeds there while a one bit read is past the end
	r := NewReadStream([]byte{0x05})
	r.SerializeBits(&v, 3)
	if err := r.SerializeEnd(); err != nil {
		t.Fatal(err)
	}
	if r.BitsProcessed() != 8 {
		t.Fatalf("the accepted end left the stream at bit %d, want 8", r.BitsProcessed())
	}
	degenerate := int32(0)
	if err := r.SerializeInt(&degenerate, 7, 7); err != nil || degenerate != 7 {
		t.Fatalf("a zero bit read after the end gave %d (%v)", degenerate, err)
	}
	var flag bool
	if err := r.SerializeBool(&flag); !errors.Is(err, ErrPastEnd) {
		t.Fatalf("a read after the end gave %v, want ErrPastEnd", err)
	}
}

// TestStringClaimedLengthCheckedBeforeAllocation: a string's length comes off the wire,
// so a few bytes can claim a payload of two gigabytes. The reader aligns, refuses a
// claimed length past the bytes that remain as ErrPastEnd, and only then allocates, so
// the refusal costs no allocation at all. (Found by the serialize.java port's cold
// reader, where the order was the other way round; bytes reads fill a caller-owned
// buffer and allocate nothing either way.)
func TestStringClaimedLengthCheckedBeforeAllocation(t *testing.T) {
	const bufferSize = math.MaxInt32 // a 31 bit length field
	w := NewWriteStream(make([]byte, 16))
	claimed := uint32(bufferSize - 1)
	w.SerializeBits(&claimed, 31)
	w.Flush()
	packet := w.Data() // four bytes claiming a 2^31 - 2 byte payload

	allocations := testing.AllocsPerRun(100, func() {
		r := NewReadStream(packet)
		value := "unmodified"
		if err := r.SerializeString(&value, bufferSize); !errors.Is(err, ErrPastEnd) {
			t.Fatalf("a claimed length past the stream gave %v, want ErrPastEnd", err)
		}
		if value != "unmodified" {
			t.Fatalf("the refused read wrote its destination")
		}
	})
	// NewReadStream itself allocates the stream; the refused string read adds nothing
	baseline := testing.AllocsPerRun(100, func() { _ = NewReadStream(packet) })
	if allocations > baseline {
		t.Fatalf("the refused read allocated %v times beyond the stream's own %v", allocations, baseline)
	}

	// and the same claim through bytes: the count is the caller's, the check the same
	r := NewReadStream(packet[:1])
	if err := r.SerializeBytes(make([]byte, 64)); !errors.Is(err, ErrPastEnd) {
		t.Fatalf("a byte count past the stream gave %v, want ErrPastEnd", err)
	}
}
