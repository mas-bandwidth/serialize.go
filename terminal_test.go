package serialize

import (
	"errors"
	"math"
	"testing"
)

// STANDARD.md, Reader Obligations: a failed read is TERMINAL for the stream, and the
// stream must enforce that rather than the caller's discipline. Nothing after a failing
// operation has a defined position, so nothing after it is interpretable.
//
// This port satisfies the rule by latch: the first failure sets ReadStream.err, every
// public read returns it before touching the buffer, and only Reset — the operation
// that points the stream at a new buffer — clears it.
//
// Each case below provokes one shape of failure, then performs a read that is valid in
// itself. The follow-on read must fail with the latched error, consume nothing and
// write nothing; after Reset the identical read must succeed, which is what proves the
// follow-on read was refused by the latch and not by the bytes.
func TestFailureIsTerminal(t *testing.T) {
	cases := []struct {
		name    string
		buffer  func() []byte
		provoke func(t *testing.T, r *ReadStream) error
		want    error
	}{
		{
			name:   "before consumption",
			buffer: func() []byte { return []byte{0xAB, 0xCD} },
			provoke: func(t *testing.T, r *ReadStream) error {
				var v uint64
				return r.SerializeBits64(&v, 33) // 33 bits wanted, 16 in the stream
			},
			want: ErrOverflow,
		},
		{
			name:   "after partial consumption",
			buffer: func() []byte { return []byte{0xAB, 0xCD, 0xEF} },
			provoke: func(t *testing.T, r *ReadStream) error {
				var consumed uint32
				if err := r.SerializeBits(&consumed, 8); err != nil {
					t.Fatalf("the first read must succeed: %v", err)
				}
				var v uint64
				return r.SerializeBits64(&v, 33) // 33 bits wanted, 16 left
			},
			want: ErrOverflow,
		},
		{
			name: "range headroom",
			buffer: func() []byte {
				// 7 in the three bit field of the range [0,5]: a value smuggled into
				// the bit headroom the range leaves behind
				return writeTerminalBuffer(t, func(w *WriteStream) {
					smuggled := uint32(7)
					w.SerializeBits(&smuggled, 3)
				})
			},
			provoke: func(t *testing.T, r *ReadStream) error {
				var v int32
				return r.SerializeInt(&v, 0, 5)
			},
			want: ErrValueOutOfRange,
		},
		{
			name: "alignment",
			buffer: func() []byte {
				return writeTerminalBuffer(t, func(w *WriteStream) {
					lead := uint32(0)
					w.SerializeBits(&lead, 3)
					padding := uint32(0x1F) // the align's five pad bits, every one set
					w.SerializeBits(&padding, 5)
				})
			},
			provoke: func(t *testing.T, r *ReadStream) error {
				var lead uint32
				if err := r.SerializeBits(&lead, 3); err != nil {
					t.Fatalf("the first read must succeed: %v", err)
				}
				return r.SerializeAlign()
			},
			want: ErrAlign,
		},
		{
			name: "malformed string",
			buffer: func() []byte {
				return writeTerminalBuffer(t, func(w *WriteStream) {
					length := uint32(2) // the four bit length field of a 16 byte buffer
					w.SerializeBits(&length, 4)
					w.SerializeAlign()
					payload := uint32(0xFEFF) // the bytes FF FE: no valid UTF-8 sequence starts with FF
					w.SerializeBits(&payload, 16)
				})
			},
			provoke: func(t *testing.T, r *ReadStream) error {
				var s string
				return r.SerializeString(&s, 16)
			},
			want: ErrValueOutOfRange,
		},
		{
			name:   "int relative",
			buffer: func() []byte { return []byte{0x01, 0xFF, 0xFF, 0xFF} },
			provoke: func(t *testing.T, r *ReadStream) error {
				// the one bit tier off the top of the domain: 2^31 - 1 plus one
				var current int32
				return r.SerializeIntRelative(math.MaxInt32, &current)
			},
			want: ErrValueOutOfRange,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buffer := c.buffer()
			r := NewReadStream(buffer)

			if err := c.provoke(t, r); !errors.Is(err, c.want) {
				t.Fatalf("provoking the failure gave %v, want %v", err, c.want)
			}
			if !errors.Is(r.Err(), c.want) {
				t.Fatalf("the stream latched %v, want %v", r.Err(), c.want)
			}

			// a read that is valid in itself: the follow-on read must be refused by the
			// latch, leave its destination alone and consume nothing
			position := r.BitsProcessed()
			followOn := uint32(0xABCDEF)
			if err := r.SerializeBits(&followOn, 8); !errors.Is(err, c.want) {
				t.Fatalf("the read after the failure gave %v, want the latched %v", err, c.want)
			}
			if followOn != 0xABCDEF {
				t.Fatalf("the read after the failure wrote its destination: %#x", followOn)
			}
			if r.BitsProcessed() != position {
				t.Fatalf("the read after the failure consumed %d bits", r.BitsProcessed()-position)
			}

			// re-initialization is the only thing that clears the failure, and the
			// identical read then succeeds: the bytes were never the reason it failed
			r.Reset(buffer)
			if r.Err() != nil {
				t.Fatalf("Reset left %v latched", r.Err())
			}
			if err := r.SerializeBits(&followOn, 8); err != nil {
				t.Fatalf("after Reset the same read failed: %v", err)
			}
			if followOn != uint32(buffer[0]) {
				t.Fatalf("after Reset the read gave %#x, want %#x", followOn, buffer[0])
			}
		})
	}
}

// writeTerminalBuffer builds a buffer with the given prefix and a trailing 0xFF byte, so
// every case has readable bits past the point where the failure lands.
func writeTerminalBuffer(t *testing.T, prefix func(w *WriteStream)) []byte {
	t.Helper()
	w := NewWriteStream(make([]byte, 32))
	prefix(w)
	w.SerializeAlign()
	trailing := uint32(0xFF)
	if err := w.SerializeBits(&trailing, 8); err != nil {
		t.Fatalf("building the buffer: %v", err)
	}
	w.Flush()
	return w.Data()
}
