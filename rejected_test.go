package serialize

import (
	"errors"
	"testing"
)

// The caller's own refusal, rejected (STANDARD.md 2.1, "Refusal reasons", and
// IMPLEMENTING.md U2): Reject and a nested object whose Serialize returns an error both
// latch ErrRejected through the stream's one latch, so the refusal is terminal and the
// first reason wins. The corpus pins the read side (sequence-reject-*, object-callback-*,
// held by the object-latch-rejected and reject-reason controls); these pin the API shape
// the corpus cannot see: the caller's error kept behind the reason, a returned Refusal,
// and Reject on a writer and a measure.

var errCallerRefuses = errors.New("caller refuses")

type refusingObject struct{ err error }

func (o refusingObject) Serialize(stream Stream) error {
	var v uint32
	if err := stream.SerializeBits(&v, 4); err != nil {
		return err
	}
	return o.err
}

func TestRejectLatchesRejected(t *testing.T) {
	r := NewReadStream([]byte{0x05, 0xAB})
	var v uint32
	if err := r.SerializeBits(&v, 4); err != nil {
		t.Fatal(err)
	}
	if err := r.Reject(); !errors.Is(err, ErrRejected) {
		t.Fatalf("Reject returned %v, want rejected", err)
	}
	if !errors.Is(r.Err(), ErrRejected) {
		t.Fatalf("the stream latched %v, want rejected", r.Err())
	}
	// terminal: a readable byte follows, and the read refuses with the first reason
	after := uint32(0xFFFFFFFF)
	if err := r.SerializeBits(&after, 4); !errors.Is(err, ErrRejected) || after != 0xFFFFFFFF {
		t.Fatalf("a read after Reject returned %v and wrote %#x", err, after)
	}
	// Reset clears it, which proves the follow-on read was refused by the latch
	r.Reset([]byte{0x05, 0xAB})
	if err := r.SerializeBits(&v, 8); err != nil {
		t.Fatalf("a read after Reset refused: %v", err)
	}
}

func TestRejectKeepsTheFirstReason(t *testing.T) {
	r := NewReadStream([]byte{0xFD, 0xAB})
	var v uint32
	r.SerializeBits(&v, 3)
	if err := r.SerializeAlign(); !errors.Is(err, ErrNonzeroPadding) {
		t.Fatalf("align returned %v", err)
	}
	if err := r.Reject(); !errors.Is(err, ErrNonzeroPadding) {
		t.Fatalf("Reject on a refused stream returned %v, want the wire reason", err)
	}
	if err := Reject(r); !errors.Is(err, ErrNonzeroPadding) {
		t.Fatalf("the package Reject on a refused stream returned %v, want the wire reason", err)
	}
}

func TestObjectLatchesRejectedAndKeepsTheCallersError(t *testing.T) {
	r := NewReadStream([]byte{0x05, 0xAB})
	err := r.SerializeObject(refusingObject{err: errCallerRefuses})
	if !errors.Is(err, ErrRejected) || !errors.Is(r.Err(), ErrRejected) {
		t.Fatalf("the object latched %v, want rejected", r.Err())
	}
	if !errors.Is(r.Err(), errCallerRefuses) {
		t.Fatalf("the latch lost the caller's error: %v", r.Err())
	}
	var refusal Refusal
	if !errors.As(r.Err(), &refusal) || refusal != ErrRejected {
		t.Fatalf("errors.As recovered %v, want rejected", refusal)
	}
	var v uint32
	if err := r.SerializeBits(&v, 8); !errors.Is(err, ErrRejected) {
		t.Fatalf("a read after the object refused returned %v", err)
	}
}

func TestObjectReturnedRefusal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Refusal
	}{
		{"a reason from the list latches as itself", ErrValueOutOfRange, ErrValueOutOfRange},
		{"rejected", ErrRejected, ErrRejected},
		{"the zero Refusal is no reason, so rejected", Refusal(0), ErrRejected},
		{"a Refusal outside the list is no reason, so rejected", Refusal(200), ErrRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewReadStream([]byte{0x05})
			r.SerializeObject(refusingObject{err: c.err})
			if !errors.Is(r.Err(), c.want) || r.Err() == nil {
				t.Fatalf("the object latched %v, want %v", r.Err(), c.want)
			}
		})
	}
}

func TestObjectInnerRefusalWins(t *testing.T) {
	r := NewReadStream([]byte{})
	err := r.SerializeObject(refusingObject{err: errCallerRefuses})
	if !errors.Is(err, ErrPastEnd) || !errors.Is(r.Err(), ErrPastEnd) {
		t.Fatalf("the object latched %v, want the inner past_end", r.Err())
	}
}

// IMPLEMENTING.md U2 and U9: reject on a writer or a measure has nothing to refuse. It
// returns ErrRejected, latches nothing and never panics.
func TestRejectOnWriteAndMeasure(t *testing.T) {
	w := NewWriteStream(make([]byte, 8))
	if err := Reject(w); !errors.Is(err, ErrRejected) {
		t.Fatalf("Reject on a writer returned %v", err)
	}
	if w.Err() != nil {
		t.Fatalf("Reject on a writer latched %v", w.Err())
	}
	m := NewMeasureStream()
	if err := Reject(m); !errors.Is(err, ErrRejected) {
		t.Fatalf("Reject on a measure returned %v", err)
	}
	if m.Err() != nil {
		t.Fatalf("Reject on a measure latched %v", m.Err())
	}
	r := NewReadStream([]byte{0x05})
	if err := Reject(r); !errors.Is(err, ErrRejected) || !errors.Is(r.Err(), ErrRejected) {
		t.Fatalf("the package Reject on a reader returned %v and latched %v", err, r.Err())
	}
}
