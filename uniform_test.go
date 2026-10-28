package serialize

import (
	"errors"
	"math"
	"testing"
)

// The uniform behaviour rules, IMPLEMENTING.md U1 to U16 (reference v2.1.0), and the
// writer check model they enforce. None of these is a corpus vector: each is
// write-side, measure-side or caller-misuse behaviour, which no stream observes. Each
// test names the rule it holds.

// panics reports whether f panicked.
func panics(f func()) (didPanic bool) {
	defer func() {
		if recover() != nil {
			didPanic = true
		}
	}()
	f()
	return false
}

// TestU7ReadDeclarationMisuseRefuses holds U7 and U16 on the read stream: a bad
// declaration on read is a refusal, value_out_of_range, never a panic. The failure
// state is checked first, so a stream that already refused keeps its first reason, and
// the destination is never written.
func TestU7ReadDeclarationMisuseRefuses(t *testing.T) {
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	cases := map[string]func(r *ReadStream) error{
		"bits 0":          func(r *ReadStream) error { v := uint32(7); return r.SerializeBits(&v, 0) },
		"bits 33":         func(r *ReadStream) error { v := uint32(7); return r.SerializeBits(&v, 33) },
		"bits64 0":        func(r *ReadStream) error { v := uint64(7); return r.SerializeBits64(&v, 0) },
		"bits64 65":       func(r *ReadStream) error { v := uint64(7); return r.SerializeBits64(&v, 65) },
		"int min > max":   func(r *ReadStream) error { v := int32(7); return r.SerializeInt(&v, 10, 5) },
		"int64 min > max": func(r *ReadStream) error { v := int64(7); return r.SerializeInt64(&v, 10, 5) },
		"int128 min > max": func(r *ReadStream) error {
			v := Int128From64(7)
			return r.SerializeInt128(&v, Int128From64(10), Int128From64(5))
		},
		"fixed64 min > max": func(r *ReadStream) error { v := int64(7); return r.SerializeFixed64(&v, 16, 16, 10, 5) },
		"fixed64 too wide":  func(r *ReadStream) error { v := int64(7); return r.SerializeFixed64(&v, 60, 16, 0, 5) },
		"fixed64 Q bounds":  func(r *ReadStream) error { v := int64(7); return r.SerializeFixed64(&v, 16, 16, -40000, 40000) },
		"fixed128 not 128":  func(r *ReadStream) error { v := Int128From64(7); return r.SerializeFixed128(&v, 64, 16, 0, 5) },
		"fixed128 Q bounds": func(r *ReadStream) error { v := Int128From64(7); return r.SerializeFixed128(&v, 4, 124, -100, 100) },
		"cf min == max":     func(r *ReadStream) error { v := float32(7); return r.SerializeCompressedFloat32(&v, 1, 1, 0.1) },
		"cf resolution 0":   func(r *ReadStream) error { v := float32(7); return r.SerializeCompressedFloat32(&v, 0, 1, 0) },
		"cf resolution NaN": func(r *ReadStream) error {
			v := float32(7)
			return r.SerializeCompressedFloat32(&v, 0, 1, float32(math.NaN()))
		},
		"precomputed bits": func(r *ReadStream) error {
			v := float32(7)
			return r.SerializeCompressedFloat32Precomputed(&v, 1000, 11, 10, 0)
		},
		"precomputed clamp": func(r *ReadStream) error {
			v := float32(7)
			return r.SerializeCompressedFloat32Precomputed(&v, math.MaxUint32, 32, 10, 0)
		},
		"precomputed delta": func(r *ReadStream) error {
			v := float32(7)
			return r.SerializeCompressedFloat32Precomputed(&v, 1000, 10, float32(math.Inf(1)), 0)
		},
		"string buffer 0":    func(r *ReadStream) error { v := "keep"; return r.SerializeString(&v, 0) },
		"string buffer -1":   func(r *ReadStream) error { v := "keep"; return r.SerializeString(&v, -1) },
		"string buffer wide": func(r *ReadStream) error { v := "keep"; return r.SerializeString(&v, math.MaxInt32+1) },
		"int_relative prev":  func(r *ReadStream) error { v := int32(7); return r.SerializeIntRelative(-1, &v) },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			if panics(func() { err = call(NewReadStream(data)) }) {
				t.Fatalf("a bad declaration on read panicked; U7 requires a refusal")
			}
			if !errors.Is(err, ErrValueOutOfRange) {
				t.Fatalf("got %v, want value_out_of_range", err)
			}
			// the failure state is checked first: a refused stream keeps its reason
			r := NewReadStream(data)
			r.Reject()
			if panics(func() { err = call(r) }) {
				t.Fatalf("a bad declaration on a refused stream panicked")
			}
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("on a refused stream got %v, want the first reason, rejected", err)
			}
		})
	}
}

// TestU7ReadDeclarationMisuseLeavesValue holds the non-mutation promise on the
// declaration refusals: the destination keeps its value.
func TestU7ReadDeclarationMisuseLeavesValue(t *testing.T) {
	r := NewReadStream([]byte{0xFF, 0xFF})
	v := uint32(7)
	_ = r.SerializeBits(&v, 0)
	s := "keep"
	_ = r.SerializeString(&s, 0)
	if v != 7 || s != "keep" {
		t.Fatalf("a refused declaration wrote the destination: %d %q", v, s)
	}
}

// TestU8OverWideBitsOnWrite holds U8: a value that does not fit its bit width is write
// misuse, caught by this port's always-on mechanism (the returned error), never masked.
func TestU8OverWideBitsOnWrite(t *testing.T) {
	w := NewWriteStream(make([]byte, 16))
	v := uint32(0x1FF)
	if err := w.SerializeBits(&v, 8); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("SerializeBits(0x1FF, 8) returned %v, want value_out_of_range", err)
	}
	if w.BitsProcessed() != 0 {
		t.Fatalf("the refused write wrote %d bits", w.BitsProcessed())
	}
	w64 := NewWriteStream(make([]byte, 16))
	v64 := uint64(1) << 40
	if err := w64.SerializeBits64(&v64, 40); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("SerializeBits64(1<<40, 40) returned %v, want value_out_of_range", err)
	}
	m := NewMeasureStream()
	if err := m.SerializeBits(&v, 8); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("measure SerializeBits(0x1FF, 8) returned %v, want value_out_of_range", err)
	}
	m64 := NewMeasureStream()
	if err := m64.SerializeBits64(&v64, 40); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("measure SerializeBits64(1<<40, 40) returned %v, want value_out_of_range", err)
	}
	// every value that fits is still written, at every width, including the full width
	for bits := 1; bits <= 32; bits++ {
		top := uint32(math.MaxUint32 >> (32 - bits))
		ok := NewWriteStream(make([]byte, 16))
		if err := ok.SerializeBits(&top, bits); err != nil {
			t.Fatalf("SerializeBits(%#x, %d) refused: %v", top, bits, err)
		}
	}
	for bits := 1; bits <= 64; bits++ {
		top := uint64(math.MaxUint64 >> (64 - bits))
		ok := NewWriteStream(make([]byte, 16))
		if err := ok.SerializeBits64(&top, bits); err != nil {
			t.Fatalf("SerializeBits64(%#x, %d) refused: %v", top, bits, err)
		}
	}
}

// TestCheckModelCompressedFloatDeclaration holds the writer check model on
// compressed_float (IMPLEMENTING.md, "The check model", and U9: compressed_float
// included): a declaration whose delta or delta / res is not finite in float32 is write
// misuse on write and measure, as the top decode already was on write.
func TestCheckModelCompressedFloatDeclaration(t *testing.T) {
	// delta = 1e38 is finite; delta / res = 1e38 / 1e-38 is not
	min, max, res := float32(0), float32(1e38), float32(1e-38)
	f := float32(0)
	if !panics(func() { _ = NewWriteStream(make([]byte, 16)).SerializeCompressedFloat32(&f, min, max, res) }) {
		t.Errorf("write accepted a declaration whose delta / res is not finite")
	}
	if !panics(func() { _ = NewMeasureStream().SerializeCompressedFloat32(&f, min, max, res) }) {
		t.Errorf("measure accepted a declaration whose delta / res is not finite")
	}
	// delta itself not finite, and the top decode: the measure follows the writer
	wide := float32(math.MaxFloat32)
	if !panics(func() { _ = NewMeasureStream().SerializeCompressedFloat32(&f, -wide, wide, 1e30) }) {
		t.Errorf("measure accepted a declaration whose delta is not finite")
	}
	if !panics(func() { _ = NewMeasureStream().SerializeCompressedFloat32(&f, -wide, wide, float32(math.Pow(2, 104))) }) {
		t.Errorf("measure accepted a declaration whose top decode is not finite")
	}
}

// TestU16PrecomputedMaxIntegerValue holds U16 on the precomputed constants: a
// max_integer_value above the 4294967040 clamp is underivable, write misuse on write and
// measure.
func TestU16PrecomputedMaxIntegerValue(t *testing.T) {
	v := float32(1)
	if !panics(func() {
		_ = NewWriteStream(make([]byte, 16)).SerializeCompressedFloat32Precomputed(&v, math.MaxUint32, 32, 10, 0)
	}) {
		t.Errorf("write accepted max_integer_value 0xFFFFFFFF")
	}
	if !panics(func() {
		_ = NewMeasureStream().SerializeCompressedFloat32Precomputed(&v, math.MaxUint32, 32, 10, 0)
	}) {
		t.Errorf("measure accepted max_integer_value 0xFFFFFFFF")
	}
	// the clamp itself is derivable and accepted
	if panics(func() {
		_ = NewWriteStream(make([]byte, 16)).SerializeCompressedFloat32Precomputed(&v, 4294967040, 32, 10, 0)
	}) {
		t.Errorf("write refused max_integer_value 4294967040, the clamp")
	}
}

// TestU4StringWriteChecks holds U3 and U4: the writer and the measure refuse a string
// that is not well-formed UTF-8, or that carries a NUL, with the reason a reader would
// give, and write nothing. Text is sent byte for byte or not at all.
func TestU4StringWriteChecks(t *testing.T) {
	cases := []struct {
		text string
		want error
	}{
		{"\xff", ErrInvalidUTF8},
		{"ok\xc0\xafok", ErrInvalidUTF8},
		{"\xed\xa0\x80", ErrInvalidUTF8}, // a lone surrogate, encoded
		{"a\x00b", ErrEmbeddedNul},
		{"\x00\xff", ErrEmbeddedNul}, // the NUL scan comes first, as on read
	}
	for _, c := range cases {
		text := c.text
		w := NewWriteStream(make([]byte, 64))
		if err := w.SerializeString(&text, 32); !errors.Is(err, c.want) {
			t.Errorf("write %q returned %v, want %v", c.text, err, c.want)
		}
		if w.BitsProcessed() != 0 {
			t.Errorf("write %q wrote %d bits", c.text, w.BitsProcessed())
		}
		m := NewMeasureStream()
		if err := m.SerializeString(&text, 32); !errors.Is(err, c.want) {
			t.Errorf("measure %q returned %v, want %v", c.text, err, c.want)
		}
	}
}

// TestU10WholeOperationRoom holds U10: bytes and string check room for the whole
// operation, field, align and payload, before writing any of it.
func TestU10WholeOperationRoom(t *testing.T) {
	// string: the 5 bit length fits in 8 bytes, the 10 byte payload does not
	w := NewWriteStream(make([]byte, 8))
	text := "0123456789"
	if err := w.SerializeString(&text, 32); !errors.Is(err, ErrPastEnd) {
		t.Fatalf("string returned %v, want past_end", err)
	}
	if w.BitsProcessed() != 0 {
		t.Fatalf("the refused string wrote %d bits ahead of its payload", w.BitsProcessed())
	}
	// bytes: three bits in, the align fits and the payload does not
	b := NewWriteStream(make([]byte, 8))
	three := uint32(5)
	b.SerializeBits(&three, 3)
	if err := b.SerializeBytes(make([]byte, 8)); !errors.Is(err, ErrPastEnd) {
		t.Fatalf("bytes returned %v, want past_end", err)
	}
	if b.BitsProcessed() != 3 {
		t.Fatalf("the refused bytes wrote %d align bits ahead of its payload", b.BitsProcessed()-3)
	}
	// the exact fit still writes: 3 bits, 5 align, 7 bytes
	e := NewWriteStream(make([]byte, 8))
	e.SerializeBits(&three, 3)
	if err := e.SerializeBytes(make([]byte, 7)); err != nil {
		t.Fatalf("the exact fit refused: %v", err)
	}
	// and a string that exactly fits: 5 length bits, 3 align, 7 bytes
	s := NewWriteStream(make([]byte, 8))
	seven := "0123456"
	if err := s.SerializeString(&seven, 32); err != nil {
		t.Fatalf("the exact string fit refused: %v", err)
	}
}

// TestCompressedFloatDecodeIsMinPlusDelta pins the read contract the doc now states
// (audit ledger, go M1): a successful read lies in [min, min + delta], where delta is
// max - min rounded in float32, and that can exceed max. Over [-1, 16777218] at
// resolution 1, delta rounds up to 16777220 and the top integer decodes to 16777219
// rounded, 16777220, above max. The decode is the STANDARD.md arithmetic and is
// correct; the 2.1 vector compressed-float-top-integer-decodes-above-max-by-delta-rounding
// holds it on the wire.
func TestCompressedFloatDecodeIsMinPlusDelta(t *testing.T) {
	min, max, res := float32(-1), float32(16777218), float32(1)
	maxInt, bits, delta := CompressedFloatParams(min, max, res)
	w := NewWriteStream(make([]byte, 16))
	w.SerializeBits(&maxInt, bits)
	w.Flush()
	r := NewReadStream(w.Data())
	var v float32
	if err := r.SerializeCompressedFloat32(&v, min, max, res); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if v != min+delta {
		t.Fatalf("the top integer decoded to %v, want min + delta = %v", v, min+delta)
	}
	if !(v > max) {
		t.Fatalf("the witness no longer decodes above max (%v <= %v): pick a new one", v, max)
	}
}

// TestU14FailAndNames holds U14: the refusal names with "none" for no refusal, and a
// public fail. On a read stream Fail latches its reason through the one latch, and a
// reason outside the closed list, none included, latches rejected: a refused stream
// never reports none. On a writer and a measure it latches nothing and never panics
// (U9), returning the reason for the serialize function to return.
func TestU14FailAndNames(t *testing.T) {
	if got := RefusalName(nil); got != "none" {
		t.Errorf("RefusalName(nil) = %q, want none", got)
	}
	if got := RefusalName(ErrNonCanonical); got != "non_canonical" {
		t.Errorf("RefusalName(ErrNonCanonical) = %q", got)
	}
	if got := RefusalName(rejection(errCallerRefuses)); got != "rejected" {
		t.Errorf("RefusalName of a wrapped caller refusal = %q, want rejected", got)
	}
	if got := Refusal(0).String(); got != "none" {
		t.Errorf("Refusal(0).String() = %q, want none", got)
	}

	r := NewReadStream([]byte{0xFF})
	if err := r.Fail(ErrNonFiniteFloat); !errors.Is(err, ErrNonFiniteFloat) {
		t.Fatalf("Fail(non_finite_float) returned %v", err)
	}
	if err := r.Fail(ErrTrailingData); !errors.Is(err, ErrNonFiniteFloat) {
		t.Fatalf("a second Fail returned %v, want the first reason", err)
	}
	for _, reason := range []Refusal{0, ErrRejected + 1, 200} {
		r := NewReadStream([]byte{0xFF})
		var err error
		if panics(func() { err = r.Fail(reason) }) {
			t.Fatalf("Fail(%d) panicked", reason)
		}
		if !errors.Is(err, ErrRejected) || !errors.Is(r.Err(), ErrRejected) {
			t.Fatalf("Fail(%d) latched %v, want rejected", reason, r.Err())
		}
	}

	for name, stream := range map[string]Stream{"write": NewWriteStream(make([]byte, 8)), "measure": NewMeasureStream()} {
		var err error
		if panics(func() { err = Fail(stream, ErrValueOutOfRange) }) {
			t.Fatalf("Fail on a %s stream panicked", name)
		}
		if !errors.Is(err, ErrValueOutOfRange) || stream.Err() != nil {
			t.Fatalf("Fail on a %s stream returned %v and latched %v", name, err, stream.Err())
		}
		if err := Fail(stream, 0); !errors.Is(err, ErrRejected) {
			t.Fatalf("Fail(none) on a %s stream returned %v, want rejected", name, err)
		}
	}
	read := NewReadStream([]byte{0xFF})
	if err := Fail(read, ErrPastEnd); !errors.Is(err, ErrPastEnd) || !errors.Is(read.Err(), ErrPastEnd) {
		t.Fatalf("Fail on a read stream returned %v and latched %v", err, read.Err())
	}
}
