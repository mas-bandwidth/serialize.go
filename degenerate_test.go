package serialize

import "testing"

// STANDARD.md: a degenerate range where min == max costs ZERO BITS -- the value
// is known from the range alone and nothing is written.
//
// Every stream used to panic on min >= max, which rejected exactly the case the
// format defines. This was NOT a case of the other ports having already got it
// right: the whole family relaxed the guard in the same sweep, and the C++ header
// pinned here at the time (v1.4.3) asserted min < max and aborted on it too.
//
// State of the family, verified by reading each repo rather than assuming:
// the C port accepts min == max on both the 32 and 64 bit paths, and so do the
// Rust and C# ports. C++ v1.6.2 accepts it through serialize_int, but its
// serialize_int64 macro still asserts min < max -- so TestDegenerateRange64
// below covers a case C++ cannot yet round trip, which is why the cross language
// harness in compat/ exercises the degenerate range on the 32 bit path only.
func TestDegenerateRangeCostsNothing(t *testing.T) {
	buffer := make([]byte, 64)

	w := NewWriteStream(buffer)
	degenerate := int32(5)
	after := int32(3)
	if err := w.SerializeInt(&degenerate, 5, 5); err != nil {
		t.Fatalf("write degenerate: %v", err)
	}
	if got := w.BitsProcessed(); got != 0 {
		t.Errorf("degenerate range wrote %d bits, want 0", got)
	}
	if err := w.SerializeInt(&after, 0, 7); err != nil {
		t.Fatalf("write after: %v", err)
	}
	// the next field must start at bit 0 -- if the degenerate one consumed bit
	// space, everything downstream shifts and the wire stops matching the
	// other ports
	if got := w.BitsProcessed(); got != 3 {
		t.Errorf("after the degenerate range the stream is at %d bits, want 3", got)
	}
	w.Flush()

	r := NewReadStream(buffer[:w.BytesProcessed()])
	var readDegenerate, readAfter int32
	if err := r.SerializeInt(&readDegenerate, 5, 5); err != nil {
		t.Fatalf("read degenerate: %v", err)
	}
	if readDegenerate != 5 {
		t.Errorf("degenerate read back %d, want 5 (recovered from the range)", readDegenerate)
	}
	if got := r.BitsProcessed(); got != 0 {
		t.Errorf("degenerate range read %d bits, want 0", got)
	}
	if err := r.SerializeInt(&readAfter, 0, 7); err != nil {
		t.Fatalf("read after: %v", err)
	}
	if readAfter != 3 {
		t.Errorf("after read back %d, want 3", readAfter)
	}

	m := NewMeasureStream()
	measured := int32(5)
	if err := m.SerializeInt(&measured, 5, 5); err != nil {
		t.Fatalf("measure degenerate: %v", err)
	}
	if got := m.BitsProcessed(); got != 0 {
		t.Errorf("measure says the degenerate range costs %d bits, want 0", got)
	}
}

// min > max is still API misuse and must still panic -- relaxing the guard was
// meant to admit the degenerate case, not to stop validating.
func TestInvertedRangeStillPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("min > max did not panic")
		}
	}()
	buffer := make([]byte, 64)
	w := NewWriteStream(buffer)
	v := int32(0)
	_ = w.SerializeInt(&v, 10, 5)
}

func TestDegenerateRange64(t *testing.T) {
	buffer := make([]byte, 64)
	w := NewWriteStream(buffer)
	v := int64(-42)
	if err := w.SerializeInt64(&v, -42, -42); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.BitsProcessed(); got != 0 {
		t.Errorf("wrote %d bits, want 0", got)
	}
	w.Flush()

	r := NewReadStream(buffer[:8])
	var out int64
	if err := r.SerializeInt64(&out, -42, -42); err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != -42 {
		t.Errorf("read back %d, want -42", out)
	}
}

// A degenerate FIXED range costs zero bits too, on the narrow path. The raw
// value is min << fractionBits, recovered from the range alone.
func TestDegenerateRangeFixed64(t *testing.T) {
	buffer := make([]byte, 64)

	w := NewWriteStream(buffer)
	v := int64(-7) << 16 // -7.0 in Q48.16
	after := int32(3)
	if err := w.SerializeFixed64(&v, 48, 16, -7, -7); err != nil {
		t.Fatalf("write degenerate: %v", err)
	}
	if got := w.BitsProcessed(); got != 0 {
		t.Errorf("degenerate fixed64 wrote %d bits, want 0", got)
	}
	if err := w.SerializeInt(&after, 0, 7); err != nil {
		t.Fatalf("write after: %v", err)
	}
	if got := w.BitsProcessed(); got != 3 {
		t.Errorf("after the degenerate fixed64 the stream is at %d bits, want 3", got)
	}
	w.Flush()

	r := NewReadStream(buffer[:w.BytesProcessed()])
	var out int64
	var readAfter int32
	if err := r.SerializeFixed64(&out, 48, 16, -7, -7); err != nil {
		t.Fatalf("read degenerate: %v", err)
	}
	if want := int64(-7) << 16; out != want {
		t.Errorf("degenerate fixed64 read back %d, want %d", out, want)
	}
	if err := r.SerializeInt(&readAfter, 0, 7); err != nil {
		t.Fatalf("read after: %v", err)
	}
	if readAfter != 3 {
		t.Errorf("after read back %d, want 3", readAfter)
	}

	m := NewMeasureStream()
	measured := int64(-7) << 16
	if err := m.SerializeFixed64(&measured, 48, 16, -7, -7); err != nil {
		t.Fatalf("measure degenerate: %v", err)
	}
	if got := m.BitsProcessed(); got != 0 {
		t.Errorf("measure says the degenerate fixed64 costs %d bits, want 0", got)
	}
}

// ...and the SAME zero bits across the 64/128 storage boundary. This library
// used to disagree with ITSELF here (serialize#54): fixedPointParams64 costs a
// degenerate range zero bits, while fixedPointParams128 computed
// BitsRequired64(min, max) + fractionBits — fractionBits ZEROS when min == max.
// One declared field, two wire encodings, selected by an internal storage-width
// detail: 0 bits for Q48.16 and 16 bits for Q112.16 over identical bounds.
// The ruling (schema enactment, 2026-08-15) pins min == max at zero bits on
// EVERY storage width.
func TestDegenerateRangeFixed128(t *testing.T) {
	configs := []struct {
		name                      string
		integerBits, fractionBits int
		unit                      int64
	}{
		{"Q112.16 positive", 112, 16, 9},
		{"Q112.16 negative", 112, 16, -9},
		{"Q64.64 zero", 64, 64, 0}, // the fraction alone would have cost a full 64 bit field
	}
	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			buffer := make([]byte, 64)
			raw := Int128From64(cfg.unit).Uint128().Lsh(uint(cfg.fractionBits)).Int128()

			w := NewWriteStream(buffer)
			v := raw
			after := int32(3)
			if err := w.SerializeFixed128(&v, cfg.integerBits, cfg.fractionBits, cfg.unit, cfg.unit); err != nil {
				t.Fatalf("write degenerate: %v", err)
			}
			if got := w.BitsProcessed(); got != 0 {
				t.Errorf("degenerate fixed128 wrote %d bits, want 0 (not fractionBits)", got)
			}
			if err := w.SerializeInt(&after, 0, 7); err != nil {
				t.Fatalf("write after: %v", err)
			}
			if got := w.BitsProcessed(); got != 3 {
				t.Errorf("after the degenerate fixed128 the stream is at %d bits, want 3", got)
			}
			w.Flush()

			r := NewReadStream(buffer[:w.BytesProcessed()])
			var out Int128
			var readAfter int32
			if err := r.SerializeFixed128(&out, cfg.integerBits, cfg.fractionBits, cfg.unit, cfg.unit); err != nil {
				t.Fatalf("read degenerate: %v", err)
			}
			if out != raw {
				t.Errorf("degenerate fixed128 read back %+v, want %+v (recovered from the range)", out, raw)
			}
			if got := r.BitsProcessed(); got != 0 {
				t.Errorf("degenerate fixed128 read %d bits, want 0", got)
			}
			if err := r.SerializeInt(&readAfter, 0, 7); err != nil {
				t.Fatalf("read after: %v", err)
			}
			if readAfter != 3 {
				t.Errorf("after read back %d, want 3", readAfter)
			}

			m := NewMeasureStream()
			measured := raw
			if err := m.SerializeFixed128(&measured, cfg.integerBits, cfg.fractionBits, cfg.unit, cfg.unit); err != nil {
				t.Fatalf("measure degenerate: %v", err)
			}
			if got := m.BitsProcessed(); got != 0 {
				t.Errorf("measure says the degenerate fixed128 costs %d bits, want 0", got)
			}
		})
	}
}

// TestDegenerateStringBufferSize: bufferSize 1 is LEGAL — the degenerate string
// buffer. The length range collapses to [0,0], so only the empty string travels and
// the length field costs zero bits, exactly as the C++ library treats buffer_size 1
// (serialize_int over a degenerate range). The old bufferSize >= 2 panic was this
// port's invention — the 2026-08-16 six-language check-model audit, issue #35 — and
// the same floor replicated into other ports from here.
func TestDegenerateStringBufferSize(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		buffer := make([]byte, 64)

		w := NewWriteStream(buffer)
		v := ""
		after := int32(3)
		if err := w.SerializeString(&v, 1); err != nil {
			t.Fatalf("write empty string at bufferSize 1: %v", err)
		}
		if got := w.BitsProcessed(); got != 0 {
			t.Errorf("empty string at bufferSize 1 wrote %d bits, want 0 (degenerate length, empty payload)", got)
		}
		if err := w.SerializeInt(&after, 0, 7); err != nil {
			t.Fatalf("write after: %v", err)
		}
		w.Flush()

		r := NewReadStream(buffer[:w.BytesProcessed()])
		out := "poisoned"
		var readAfter int32
		if err := r.SerializeString(&out, 1); err != nil {
			t.Fatalf("read empty string at bufferSize 1: %v", err)
		}
		if out != "" {
			t.Errorf("read back %q, want the empty string", out)
		}
		if err := r.SerializeInt(&readAfter, 0, 7); err != nil {
			t.Fatalf("read after: %v", err)
		}
		if readAfter != 3 {
			t.Errorf("after read back %d, want 3", readAfter)
		}

		m := NewMeasureStream()
		mv := ""
		if err := m.SerializeString(&mv, 1); err != nil {
			t.Fatalf("measure empty string at bufferSize 1: %v", err)
		}
		if got := m.BitsProcessed(); got > 7 {
			t.Errorf("measure says %d bits, want at most the conservative align worst case 7", got)
		}

		// a non-empty string still cannot fit: the writer's normal rejection
		w2 := NewWriteStream(make([]byte, 64))
		nonEmpty := "x"
		if err := w2.SerializeString(&nonEmpty, 1); err != ErrValueOutOfRange {
			t.Errorf("non-empty string at bufferSize 1: got %v, want ErrValueOutOfRange", err)
		}
	})

	t.Run("wstring", func(t *testing.T) {
		buffer := make([]byte, 64)

		w := NewWriteStream(buffer)
		v := ""
		if err := w.SerializeWideString(&v, 1); err != nil {
			t.Fatalf("write empty wstring at bufferSize 1: %v", err)
		}
		if got := w.BitsProcessed(); got != 0 {
			t.Errorf("empty wstring at bufferSize 1 wrote %d bits, want 0", got)
		}
		w.Flush()

		r := NewReadStream(buffer[:8])
		out := "poisoned"
		if err := r.SerializeWideString(&out, 1); err != nil {
			t.Fatalf("read empty wstring at bufferSize 1: %v", err)
		}
		if out != "" {
			t.Errorf("read back %q, want the empty string", out)
		}

		w2 := NewWriteStream(make([]byte, 64))
		nonEmpty := "x"
		if err := w2.SerializeWideString(&nonEmpty, 1); err != ErrValueOutOfRange {
			t.Errorf("non-empty wstring at bufferSize 1: got %v, want ErrValueOutOfRange", err)
		}
	})

	// 0 stays misuse: it cannot express any length range
	t.Run("zero still panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("bufferSize 0 must panic: no length range exists")
			}
		}()
		w := NewWriteStream(make([]byte, 8))
		v := ""
		_ = w.SerializeString(&v, 0)
	})
}
