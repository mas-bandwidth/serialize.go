package serialize

import (
	"errors"
	"math"
	"testing"
)

// The 2.1.1 rulings (reference v2.1.1, IMPLEMENTING.md U6, U9 and U15): write and
// measure misuse traps at the call, and a write or measure stream records nothing.
// This port has no unchecked build, so it traps in every run, and its trap is a Go
// panic. None of these is a corpus vector: no stream observes a writer's misuse.

// trapped reports whether f panicked, and with what.
func trapped(f func()) (value any, didPanic bool) {
	defer func() {
		if r := recover(); r != nil {
			value, didPanic = r, true
		}
	}()
	f()
	return nil, false
}

// writeMisuse is every value misuse the check model names, as one call on a unified
// stream, so the same case runs on the writer and on the measure (U6).
var writeMisuse = map[string]func(s Stream){
	"int above max":     func(s Stream) { v := int32(11); s.SerializeInt(&v, 0, 10) },
	"int below min":     func(s Stream) { v := int32(-1); s.SerializeInt(&v, 0, 10) },
	"int64 above max":   func(s Stream) { v := int64(11); s.SerializeInt64(&v, 0, 10) },
	"int128 above max":  func(s Stream) { v := Int128From64(11); s.SerializeInt128(&v, Int128From64(0), Int128From64(10)) },
	"fixed64 above max": func(s Stream) { v := int64(11 << 16); s.SerializeFixed64(&v, 16, 16, 0, 10) },
	"fixed128 above max": func(s Stream) {
		v := Int128From64(11).Lsh(16)
		s.SerializeFixed128(&v, 112, 16, 0, 10)
	},
	"bits over wide":   func(s Stream) { v := uint32(0x1FF); s.SerializeBits(&v, 8) },
	"bits64 over wide": func(s Stream) { v := uint64(0x1FF); s.SerializeBits64(&v, 8) },
	"bits64 over 32":   func(s Stream) { v := uint64(1) << 40; s.SerializeBits64(&v, 40) },
	"float NaN":        func(s Stream) { v := float32(math.NaN()); s.SerializeFloat32(&v) },
	"float +inf":       func(s Stream) { v := float32(math.Inf(1)); s.SerializeFloat32(&v) },
	"double NaN":       func(s Stream) { v := math.NaN(); s.SerializeFloat64(&v) },
	"double -inf":      func(s Stream) { v := math.Inf(-1); s.SerializeFloat64(&v) },
	"cf NaN":           func(s Stream) { v := float32(math.NaN()); s.SerializeCompressedFloat32(&v, 0, 10, 0.01) },
	"cf +inf":          func(s Stream) { v := float32(math.Inf(1)); s.SerializeCompressedFloat32(&v, 0, 10, 0.01) },
	"cf -inf":          func(s Stream) { v := float32(math.Inf(-1)); s.SerializeCompressedFloat32(&v, 0, 10, 0.01) },
	"cf precomputed NaN": func(s Stream) {
		maxIntegerValue, bits, delta := CompressedFloatParams(0, 10, 0.01)
		v := float32(math.NaN())
		s.(precomputedStream).SerializeCompressedFloat32Precomputed(&v, maxIntegerValue, bits, delta, 0)
	},
	"string too long":   func(s Stream) { v := "abcd"; s.SerializeString(&v, 4) },
	"string bad UTF-8":  func(s Stream) { v := "a\xffb"; s.SerializeString(&v, 16) },
	"string NUL":        func(s Stream) { v := "a\x00b"; s.SerializeString(&v, 16) },
	"int_relative same": func(s Stream) { v := int32(5); s.SerializeIntRelative(5, &v) },
	"int_relative down": func(s Stream) { v := int32(4); s.SerializeIntRelative(5, &v) },
	"reject":            func(s Stream) { Reject(s) },
}

// TestU9WriteMisuseTraps holds U9 on the write stream: every value misuse panics at the
// call, before any bit of the operation is written, and records nothing, so the stream
// reports no error and the next write succeeds and lands where it would have.
func TestU9WriteMisuseTraps(t *testing.T) {
	for name, misuse := range writeMisuse {
		buffer := make([]byte, 64)
		w := NewWriteStream(buffer)
		prefix := uint32(5)
		w.SerializeBits(&prefix, 3)
		if _, ok := trapped(func() { misuse(w) }); !ok {
			t.Errorf("%s: write did not trap", name)
			continue
		}
		if w.Err() != nil {
			t.Errorf("%s: write misuse latched %v", name, w.Err())
		}
		if w.BitsProcessed() != 3 {
			t.Errorf("%s: write misuse moved the stream to bit %d", name, w.BitsProcessed())
		}
		next := uint32(1)
		if err := w.SerializeBits(&next, 1); err != nil {
			t.Errorf("%s: write after misuse failed: %v", name, err)
		}
		w.Flush()
		if got := w.Data(); len(got) != 1 || got[0] != 0x0D {
			t.Errorf("%s: wrote % x, want 0d", name, got)
		}
	}
}

// TestU6MeasureMisuseTraps holds U6 with U9: a measure makes every check a write makes,
// by the same trap, and latches nothing.
func TestU6MeasureMisuseTraps(t *testing.T) {
	for name, misuse := range writeMisuse {
		m := NewMeasureStream()
		prefix := uint32(5)
		m.SerializeBits(&prefix, 3)
		if _, ok := trapped(func() { misuse(m) }); !ok {
			t.Errorf("%s: measure did not trap", name)
			continue
		}
		if m.Err() != nil {
			t.Errorf("%s: measure misuse latched %v", name, m.Err())
		}
		next := uint32(1)
		if err := m.SerializeBits(&next, 1); err != nil || m.BitsProcessed() != 4 {
			t.Errorf("%s: measure after misuse: err %v, bits %d, want nil and 4", name, err, m.BitsProcessed())
		}
	}
}

// TestU9WritePastEndTraps holds U9 with U10 on the write stream: an operation that does
// not fit the room left panics before any of it is written, and records nothing.
func TestU9WritePastEndTraps(t *testing.T) {
	cases := map[string]func(w *WriteStream){
		"bits":    func(w *WriteStream) { v := uint32(1); w.SerializeBits(&v, 2) },
		"bits64":  func(w *WriteStream) { v := uint64(1); w.SerializeBits64(&v, 40) },
		"bool":    func(w *WriteStream) { v := true; w.SerializeBool(&v); w.SerializeBool(&v) },
		"uint8":   func(w *WriteStream) { v := uint8(1); w.SerializeUint8(&v) },
		"uint16":  func(w *WriteStream) { v := uint16(1); w.SerializeUint16(&v) },
		"uint32":  func(w *WriteStream) { v := uint32(1); w.SerializeUint32(&v) },
		"uint64":  func(w *WriteStream) { v := uint64(1); w.SerializeUint64(&v) },
		"uint128": func(w *WriteStream) { v := Uint128From64(1); w.SerializeUint128(&v) },
		"int":     func(w *WriteStream) { v := int32(1); w.SerializeInt(&v, 0, 1000) },
		"int64":   func(w *WriteStream) { v := int64(1); w.SerializeInt64(&v, 0, 1<<40) },
		"int128": func(w *WriteStream) {
			v := Int128From64(1)
			w.SerializeInt128(&v, Int128From64(0), Int128From64(1<<40))
		},
		"fixed64":  func(w *WriteStream) { v := int64(1); w.SerializeFixed64(&v, 16, 16, 0, 100) },
		"fixed128": func(w *WriteStream) { v := Int128From64(1); w.SerializeFixed128(&v, 112, 16, 0, 100) },
		"float":    func(w *WriteStream) { v := float32(1); w.SerializeFloat32(&v) },
		"double":   func(w *WriteStream) { v := float64(1); w.SerializeFloat64(&v) },
		"cf":       func(w *WriteStream) { v := float32(1); w.SerializeCompressedFloat32(&v, 0, 1000, 0.01) },
		"bytes":    func(w *WriteStream) { w.SerializeBytes([]byte{1}) },
		"string":   func(w *WriteStream) { v := "a"; w.SerializeString(&v, 2) },
		"int_relative": func(w *WriteStream) {
			v := int32(1000)
			w.SerializeIntRelative(0, &v)
		},
	}
	for name, overflow := range cases {
		buffer := make([]byte, 8)
		w := NewWriteStream(buffer)
		fill := uint64(0x5A5A5A5A5A5A5A)
		w.SerializeBits64(&fill, 63)
		if _, ok := trapped(func() { overflow(w) }); !ok {
			t.Errorf("%s: write past the end did not trap", name)
			continue
		}
		if w.Err() != nil {
			t.Errorf("%s: write past the end latched %v", name, w.Err())
		}
		if w.BitsProcessed() > 64 {
			t.Errorf("%s: wrote past the end, bit %d", name, w.BitsProcessed())
		}
	}
	// the whole operation's room comes first: no length or align bit is left behind
	w := NewWriteStream(make([]byte, 8))
	v := "abcdefgh"
	if _, ok := trapped(func() { w.SerializeString(&v, 64) }); !ok {
		t.Fatal("string past the end did not trap")
	}
	if w.BitsProcessed() != 0 {
		t.Errorf("string past the end wrote %d bits ahead of its payload", w.BitsProcessed())
	}
}

// TestU9FailOnWriteAndMeasure holds U9's fail: on a write or a measure it returns its
// false, a non-nil error, never traps, and records nothing.
func TestU9FailOnWriteAndMeasure(t *testing.T) {
	for _, s := range []Stream{NewWriteStream(make([]byte, 8)), NewMeasureStream()} {
		var err error
		if _, ok := trapped(func() { err = Fail(s, ErrValueOutOfRange) }); ok {
			t.Fatalf("%T: Fail trapped", s)
		}
		if !errors.Is(err, ErrValueOutOfRange) {
			t.Errorf("%T: Fail returned %v", s, err)
		}
		if err := Fail(s, 0); !errors.Is(err, ErrRejected) {
			t.Errorf("%T: Fail(none) returned %v, want rejected", s, err)
		}
		if s.Err() != nil {
			t.Errorf("%T: Fail latched %v", s, s.Err())
		}
		v := uint32(1)
		if err := s.SerializeBits(&v, 1); err != nil {
			t.Errorf("%T: write after Fail failed: %v", s, err)
		}
	}
}

type trapFailingObject struct{ err error }

func (o trapFailingObject) Serialize(stream Stream) error {
	v := uint32(1)
	if err := stream.SerializeBits(&v, 1); err != nil {
		return err
	}
	return o.err
}

// TestU9ObjectFailureOnWriteAndMeasure holds U9's nested object: a Serialize method that
// returns an error on a write or a measure makes SerializeObject return it, and records
// nothing on the outer stream.
func TestU9ObjectFailureOnWriteAndMeasure(t *testing.T) {
	cause := errors.New("caller's own")
	for _, s := range []Stream{NewWriteStream(make([]byte, 8)), NewMeasureStream()} {
		if err := s.SerializeObject(trapFailingObject{cause}); !errors.Is(err, cause) {
			t.Errorf("%T: object returned %v, want the caller's error", s, err)
		}
		if s.Err() != nil {
			t.Errorf("%T: object failure latched %v", s, s.Err())
		}
		if err := s.SerializeObject(trapFailingObject{}); err != nil {
			t.Errorf("%T: object after a failed object: %v", s, err)
		}
		if err := s.SerializeEnd(); err != nil {
			t.Errorf("%T: end after a failed object: %v (U12)", s, err)
		}
		if s.BitsProcessed() != 2 {
			t.Errorf("%T: %d bits, want 2", s, s.BitsProcessed())
		}
	}
}

// TestU8BitWriterTraps holds U8 on the exported BitWriter, the reference's own helper:
// a value wider than its field panics, as the reference's BitWriter asserts.
func TestU8BitWriterTraps(t *testing.T) {
	w := NewBitWriter(make([]byte, 8))
	if _, ok := trapped(func() { w.WriteBits(0x1FF, 8) }); !ok {
		t.Error("BitWriter.WriteBits(0x1FF, 8) did not trap")
	}
	if w.BitsWritten() != 0 {
		t.Errorf("BitWriter wrote %d bits on misuse", w.BitsWritten())
	}
	w.WriteBits(0xFF, 8)
	w.WriteBits(0xFFFFFFFF, 32)
	if w.BitsWritten() != 40 {
		t.Errorf("BitWriter wrote %d bits, want 40", w.BitsWritten())
	}
}

// TestU15CopyString holds U15's serialize_copy_string with the reference's
// test_copy_string vectors. A Go string is a byte string, so the byte rule applies to
// any input, ill-formed text included.
func TestU15CopyString(t *testing.T) {
	cases := []struct {
		source   string
		destSize int
		want     string
	}{
		{"abc", 8, "abc"},
		{"a\x00bc", 8, "a"},
		{"abcdefghij", 4, "abc"},
		{"\xC3\xA9\xC3\xA9", 4, "\xC3\xA9"},
		{"\xC3\xA9\xC3\xA9", 3, "\xC3\xA9"},
		{"a\xF0\x9F\x98\x80", 3, "a"},
		{"a\xF0\x9F\x98\x80", 4, "a"},
		{"a\xF0\x9F\x98\x80", 5, "a"},
		{"a\xF0\x9F\x98\x80", 6, "a\xF0\x9F\x98\x80"},
		{"ab\x80\x80", 4, "a"},
		{"\xC3\xA9\xA9\xA9", 4, ""},
		{"abc", 1, ""},
		{"", 1, ""},
		{"abc", 4, "abc"},
	}
	for _, c := range cases {
		if got := CopyString(c.source, c.destSize); got != c.want {
			t.Errorf("CopyString(%q, %d) = %q, want %q", c.source, c.destSize, got, c.want)
		}
	}
	// the result of well-formed text is what serialize_string with buffer_size =
	// dest_size accepts
	for destSize := 1; destSize <= 12; destSize++ {
		s := CopyString("x\xC3\xA9\xF0\x9F\x98\x80\xE2\x82\xACy", destSize)
		w := NewWriteStream(make([]byte, 32))
		if _, ok := trapped(func() { w.SerializeString(&s, destSize) }); ok {
			t.Errorf("dest size %d: serialize_string refused the copy %q", destSize, s)
		}
	}
	for _, destSize := range []int{0, -1} {
		if _, ok := trapped(func() { CopyString("abc", destSize) }); !ok {
			t.Errorf("CopyString with dest size %d did not trap (U9)", destSize)
		}
	}
}
