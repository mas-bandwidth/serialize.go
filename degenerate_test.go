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
