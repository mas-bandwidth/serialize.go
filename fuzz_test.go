package serialize

import (
	"bytes"
	"math"
	"testing"
)

// FuzzReadStream feeds arbitrary bytes to a read stream and exercises every serialize
// method. Reads of malicious packet data must fail with errors, never panic.
func FuzzReadStream(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF})
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Add(goldenWireBytes)

	f.Fuzz(func(t *testing.T, data []byte) {
		stream := NewReadStream(data)
		var golden goldenWireData
		_ = goldenWireSerialize(stream, &golden)

		stream.Reset(data)
		var u32 uint32
		_ = stream.SerializeBits(&u32, 7)
		var u64 uint64
		_ = stream.SerializeBits64(&u64, 33)
		var i32 int32
		_ = stream.SerializeInt(&i32, -5, 100)
		var i64 int64
		_ = stream.SerializeInt64(&i64, math.MinInt64, math.MaxInt64)
		var relative int32
		_ = stream.SerializeIntRelative(-10, &relative)
		var str string
		_ = stream.SerializeString(&str, 300)
		var wstr string
		_ = stream.SerializeWideString(&wstr, 300)
		_ = stream.SerializeAlign()
		buffer := make([]byte, 33)
		_ = stream.SerializeBytes(buffer)
		var f32 float32
		_ = stream.SerializeCompressedFloat32(&f32, -1, 1, 0.001)
		var f64 float64
		_ = stream.SerializeFloat64(&f64)
	})
}

// FuzzRoundTrip writes fuzzed values and reads them back, verifying they survive the
// trip exactly.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint32(0), uint8(0), uint64(0), uint8(0), int32(0), int32(0), "", []byte{}, false, float32(0), 0.0)
	f.Add(uint32(0xDEADBEEF), uint8(31), uint64(0x123456789ABCDEF0), uint8(63), int32(-1000), int32(1000),
		"hello world", []byte{1, 2, 3}, true, float32(3.14159), 1.0/3.0)

	f.Fuzz(func(t *testing.T, a uint32, aBits uint8, b uint64, bBits uint8, c int32, d int32,
		str string, bulk []byte, flag bool, f32 float32, f64 float64) {

		abits := int(aBits)%32 + 1
		bbits := int(bBits)%64 + 1

		min, max := c, d
		if min > max {
			min, max = max, min
		}
		if min == max {
			if max == math.MaxInt32 {
				min--
			} else {
				max++
			}
		}

		if len(str) > 255 {
			str = str[:255]
		}
		if len(bulk) > 512 {
			bulk = bulk[:512]
		}

		buffer := make([]byte, 2048)

		writeStream := NewWriteStream(buffer)
		writeStream.SerializeBits(&a, abits)
		writeStream.SerializeBits64(&b, bbits)
		writeStream.SerializeInt(&c, min, max)
		writeStream.SerializeBool(&flag)
		writeStream.SerializeFloat32(&f32)
		writeStream.SerializeFloat64(&f64)
		writeStream.SerializeString(&str, 256)
		writeStream.SerializeBytes(bulk)
		if err := writeStream.Error(); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		writeStream.Flush()

		readStream := NewReadStream(writeStream.Data())
		var ra uint32
		var rb uint64
		var rc int32
		var rstr string
		rbulk := make([]byte, len(bulk))
		var rflag bool
		var rf32 float32
		var rf64 float64
		readStream.SerializeBits(&ra, abits)
		readStream.SerializeBits64(&rb, bbits)
		readStream.SerializeInt(&rc, min, max)
		readStream.SerializeBool(&rflag)
		readStream.SerializeFloat32(&rf32)
		readStream.SerializeFloat64(&rf64)
		readStream.SerializeString(&rstr, 256)
		readStream.SerializeBytes(rbulk)
		if err := readStream.Error(); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		if ra != a&uint32(uint64(1)<<abits-1) {
			t.Fatalf("bits mismatch: wrote %#x (%d bits), read %#x", a, abits, ra)
		}
		if rb != b&(uint64(1)<<bbits-1) {
			t.Fatalf("bits64 mismatch: wrote %#x (%d bits), read %#x", b, bbits, rb)
		}
		if rc != c {
			t.Fatalf("int mismatch: wrote %d in [%d,%d], read %d", c, min, max, rc)
		}
		if rflag != flag {
			t.Fatal("bool mismatch")
		}
		if math.Float32bits(rf32) != math.Float32bits(f32) {
			t.Fatalf("float32 mismatch: wrote %x, read %x", math.Float32bits(f32), math.Float32bits(rf32))
		}
		if math.Float64bits(rf64) != math.Float64bits(f64) {
			t.Fatalf("float64 mismatch: wrote %x, read %x", math.Float64bits(f64), math.Float64bits(rf64))
		}
		if rstr != str {
			t.Fatalf("string mismatch: wrote %q, read %q", str, rstr)
		}
		if !bytes.Equal(rbulk, bulk) {
			t.Fatalf("bytes mismatch: wrote %x, read %x", bulk, rbulk)
		}
	})
}
