package serialize

import "encoding/binary"

// BitWriter bitpacks unsigned integer values to a buffer.
//
// Integer bit values are written to a 64 bit scratch value from right to left. Once the
// scratch fills to 64 bits it is stored to memory as a qword and the handful of bits that
// spilled past 64 carry over into the next scratch. Flushing half as often as a dword
// design makes writes significantly faster.
//
// The bit stream is written to memory in little endian order, which is considered network
// byte order for this library. The output is bit-for-bit identical to the C++ serialize
// library.
//
// IMPORTANT: The buffer size must be a multiple of 8 bytes, because words are stored to
// memory 8 bytes at a time. Bytes past the end of the written data are only ever written
// as zeros. The buffer does not need any particular alignment.
//
// The zero value is not usable: create a BitWriter with NewBitWriter, or Reset one onto
// a buffer before use.
type BitWriter struct {
	data        []byte
	scratch     uint64
	numBits     int64
	bitsWritten int64
	wordIndex   int64
	scratchBits int
}

// NewBitWriter creates a bit writer that fills the given buffer with bitpacked data.
// The buffer size must be a multiple of 8 bytes. Buffer sizes are effectively unlimited,
// because bit counts are stored in 64 bit signed integers.
func NewBitWriter(buffer []byte) *BitWriter {
	w := &BitWriter{}
	w.Reset(buffer)
	return w
}

// Reset points the bit writer at a buffer and clears all write state, allowing a single
// writer to be reused without allocation. The buffer size must be a multiple of 8 bytes.
func (w *BitWriter) Reset(buffer []byte) {
	if len(buffer)%8 != 0 {
		panic(panicBufferBytes)
	}
	w.data = buffer
	w.scratch = 0
	w.numBits = int64(len(buffer)) * 8
	w.bitsWritten = 0
	w.wordIndex = 0
	w.scratchBits = 0
}

// writeBits is the unchecked hot path shared by WriteBits and the write stream, which
// perform their own validation before calling it. bits must be in [1,32] and the write
// must fit in the buffer.
func (w *BitWriter) writeBits(value uint32, bits int) {
	value &= uint32(uint64(1)<<bits - 1)

	w.scratch |= uint64(value) << w.scratchBits

	newScratchBits := w.scratchBits + bits

	if newScratchBits >= 64 {
		binary.LittleEndian.PutUint64(w.data[w.wordIndex*8:], w.scratch)
		w.wordIndex++
		// recover the bits that spilled past 64. newScratchBits >= 64 with bits <= 32
		// implies the shift is in [1,32]
		w.scratch = uint64(value) >> (64 - w.scratchBits)
		w.scratchBits = newScratchBits - 64
	} else {
		w.scratchBits = newScratchBits
	}

	w.bitsWritten += int64(bits)
}

// WriteBits writes the low order bits of value to the buffer, without padding to the
// nearest byte. A boolean value writes just 1 bit, a value in [0,31] can be written with
// just 5 bits and so on. bits must be in [1,32]; bits of value above that count are
// ignored. Panics if the write would go past the end of the buffer.
//
// IMPORTANT: When you have finished writing, call FlushBits, otherwise the last word of
// data will not get flushed to memory!
func (w *BitWriter) WriteBits(value uint32, bits int) {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	if w.bitsWritten+int64(bits) > w.numBits {
		panic(panicWriteOverflow)
	}
	w.writeBits(value, bits)
}

// WriteAlign pads the bit stream with zeros so the bit index becomes a multiple of 8.
// This is useful if you want to write data that should be byte aligned, for example an
// array of bytes or a string. If the current bit index is already a multiple of 8,
// nothing is written.
func (w *BitWriter) WriteAlign() {
	remainderBits := int(w.bitsWritten % 8)
	if remainderBits != 0 {
		w.WriteBits(0, 8-remainderBits)
	}
}

// writeBytes copies a run of bytes into the bit stream from either a byte slice or a
// string, without bitpacking the aligned middle section. The writer must be aligned to a
// byte boundary. Head bytes are bitpacked until the stream reaches a qword boundary, the
// middle is a straight copy, and the tail is bitpacked again.
func writeBytes[T ~[]byte | ~string](w *BitWriter, data T) {
	if w.bitsWritten%8 != 0 {
		panic(panicNotAligned)
	}
	if w.bitsWritten+int64(len(data))*8 > w.numBits {
		panic(panicWriteOverflow)
	}

	n := int64(len(data))

	headBytes := (8 - (w.bitsWritten%64)/8) % 8
	if headBytes > n {
		headBytes = n
	}
	for i := int64(0); i < headBytes; i++ {
		w.writeBits(uint32(data[i]), 8)
	}
	if headBytes == n {
		return
	}

	// the head bytes flushed the scratch exactly at the qword boundary
	numWords := (n - headBytes) / 8
	if numWords > 0 {
		copy(w.data[w.wordIndex*8:], data[headBytes:headBytes+numWords*8])
		w.bitsWritten += numWords * 64
		w.wordIndex += numWords
		w.scratch = 0
	}

	for i := headBytes + numWords*8; i < n; i++ {
		w.writeBits(uint32(data[i]), 8)
	}
}

// WriteBytes writes an array of bytes to the bit stream. Use this when you have to copy
// a large block of data into your bitstream. Faster than writing each byte via
// WriteBits(value, 8), because the aligned middle of the data is block copied into the
// buffer without bitpacking. The writer must be aligned to a byte boundary: call
// WriteAlign first.
func (w *BitWriter) WriteBytes(data []byte) {
	writeBytes(w, data)
}

// WriteString writes the bytes of a string to the bit stream, exactly like WriteBytes
// but without converting the string to a byte slice, so it does not allocate.
func (w *BitWriter) WriteString(data string) {
	writeBytes(w, data)
}

// FlushBits flushes any remaining bits in the scratch to memory. Call this once after
// you have finished writing bits. The flush stores a full qword: the buffer size is a
// multiple of 8 so this stays in bounds, and bytes past the written data are only ever
// written as zeros.
//
// FlushBits ends the write: writing more bits after a mid-stream flush corrupts the
// stream, because the flushed partial word cannot be resumed.
func (w *BitWriter) FlushBits() {
	if w.scratchBits != 0 {
		binary.LittleEndian.PutUint64(w.data[w.wordIndex*8:], w.scratch)
		w.scratch = 0
		w.scratchBits = 0
		w.wordIndex++
	}
}

// AlignBits returns the number of align bits that would be written, if an align was
// written right now. The result is in [0,7], where 0 means the stream is already
// byte aligned.
func (w *BitWriter) AlignBits() int {
	return int((8 - w.bitsWritten%8) % 8)
}

// BitsWritten returns the number of bits written so far.
func (w *BitWriter) BitsWritten() int64 {
	return w.bitsWritten
}

// BitsAvailable returns the number of bits still available to write. For example, if the
// buffer size is 8 bytes and 10 bits have been written, 54 bits are available.
func (w *BitWriter) BitsAvailable() int64 {
	return w.numBits - w.bitsWritten
}

// BytesWritten returns the number of bytes flushed to memory, which is the number of
// bits written rounded up to the next byte. This is effectively the size of the packet
// you should send after you have finished bitpacking values with this writer.
//
// IMPORTANT: Call FlushBits first, otherwise you risk missing the last word of data.
func (w *BitWriter) BytesWritten() int64 {
	return (w.bitsWritten + 7) / 8
}

// Data returns the written portion of the buffer: the first BytesWritten bytes of the
// buffer passed to the writer.
//
// IMPORTANT: Call FlushBits first, otherwise you risk missing the last word of data.
func (w *BitWriter) Data() []byte {
	return w.data[:int(w.BytesWritten())]
}

// BitReader reads bitpacked integer values from a buffer.
//
// The reader relies on the user reconstructing the exact same set of bit reads as bit
// writes when the buffer was written. This is an unattributed bitpacked binary stream!
//
// Reads are effectively branchless: each read loads a 64 bit little endian window at the
// current byte position and shifts by the bit remainder. There is no scratch state and no
// refill loop, so reads carry no dependency between calls other than advancing the bit
// index.
//
// Any buffer size is supported. For the fastest reads, keep at least 7 bytes of slack in
// the backing array beyond the data — for example, read packets into a large buffer and
// slice the packet out of it. The reader detects the slack via cap() and uses the fully
// branchless window load everywhere; without slack, reads near the end of the buffer
// assemble the window from the remaining bytes instead. Slack bytes are loaded but never
// interpreted: bits past the end of the data cannot reach the output of a read.
//
// The zero value is an exhausted reader: create one with NewBitReader, or Reset one onto
// a buffer before use.
type BitReader struct {
	data     []byte
	window   []byte
	numBits  int64
	bitsRead int64
}

// NewBitReader creates a bit reader that reads the bitpacked data in the given slice.
// The slice does not need any particular alignment. Buffer sizes are effectively
// unlimited, because bit counts are stored in 64 bit signed integers.
func NewBitReader(data []byte) *BitReader {
	r := &BitReader{}
	r.Reset(data)
	return r
}

// Reset points the bit reader at a data slice and clears all read state, allowing a
// single reader to be reused without allocation.
func (r *BitReader) Reset(data []byte) {
	r.data = data
	r.window = data[:cap(data)]
	r.numBits = int64(len(data)) * 8
	r.bitsRead = 0
}

// readBits is the unchecked hot path shared by ReadBits and the read stream, which
// perform their own validation before calling it. bits must be in [1,32] and must not
// read past the end of the buffer.
func (r *BitReader) readBits(bits int) uint32 {
	byteIndex := int(r.bitsRead >> 3)

	var window uint64
	if byteIndex+8 <= len(r.window) {
		window = binary.LittleEndian.Uint64(r.window[byteIndex:])
	} else {
		// near the end of a buffer whose backing array has no slack past the data:
		// assemble the window from the remaining bytes
		for i := len(r.window) - byteIndex - 1; i >= 0; i-- {
			window = window<<8 | uint64(r.window[byteIndex+i])
		}
	}

	output := uint32(window>>(r.bitsRead&7)) & uint32(uint64(1)<<bits-1)

	r.bitsRead += int64(bits)

	return output
}

// WouldReadPastEnd returns true if reading the given number of bits would read past the
// end of the buffer.
func (r *BitReader) WouldReadPastEnd(bits int) bool {
	return r.bitsRead+int64(bits) > r.numBits
}

// ReadBits reads bits from the buffer and returns the integer value read, in range
// [0,(1<<bits)-1]. bits must be in [1,32]. Panics if the read would go past the end of
// the buffer: check WouldReadPastEnd first when reading untrusted data, or use ReadStream,
// which performs all checks and returns errors instead.
func (r *BitReader) ReadBits(bits int) uint32 {
	if bits < 1 || bits > 32 {
		panic(panicBitsRange)
	}
	if r.bitsRead+int64(bits) > r.numBits {
		panic(panicReadOverflow)
	}
	return r.readBits(bits)
}

// ReadAlign reads an align, corresponding to a WriteAlign call when the buffer was
// written, and skips ahead to the next byte boundary. As a safety check, it verifies
// that the padding bits are zero and returns false if they are not; this typically
// aborts the packet read.
func (r *BitReader) ReadAlign() bool {
	remainderBits := int(r.bitsRead % 8)
	if remainderBits != 0 {
		value := r.ReadBits(8 - remainderBits)
		if value != 0 {
			return false
		}
	}
	return true
}

// readSlice returns the next n bytes of the underlying data without copying, advancing
// the read position. The reader must be byte aligned and the caller must have bounds
// checked the read.
func (r *BitReader) readSlice(n int) []byte {
	offset := int(r.bitsRead >> 3)
	r.bitsRead += int64(n) * 8
	return r.data[offset : offset+n]
}

// ReadBytes reads len(data) bytes from the bit stream into data, corresponding to a
// WriteBytes call when the buffer was written. The reader must be aligned to a byte
// boundary. Panics if the read would go past the end of the buffer: bounds check with
// BitsRemaining first when reading untrusted data, or use ReadStream.
func (r *BitReader) ReadBytes(data []byte) {
	if r.bitsRead%8 != 0 {
		panic(panicNotAligned)
	}
	if r.bitsRead+int64(len(data))*8 > r.numBits {
		panic(panicReadOverflow)
	}
	copy(data, r.readSlice(len(data)))
}

// AlignBits returns the number of align bits that would be read, if an align was read
// right now. The result is in [0,7], where 0 means the stream is already byte aligned.
func (r *BitReader) AlignBits() int {
	return int((8 - r.bitsRead%8) % 8)
}

// BitsRead returns the number of bits read from the buffer so far.
func (r *BitReader) BitsRead() int64 {
	return r.bitsRead
}

// BitsRemaining returns the number of bits still available to read. For example, if the
// buffer size is 8 bytes and 10 bits have been read, 54 bits remain.
func (r *BitReader) BitsRemaining() int64 {
	return r.numBits - r.bitsRead
}
