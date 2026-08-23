package serialize

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestBitpacker(t *testing.T) {
	const bufferSize = 256

	buffer := make([]byte, bufferSize)

	writer := NewBitWriter(buffer)

	if writer.BitsWritten() != 0 || writer.BytesWritten() != 0 || writer.BitsAvailable() != bufferSize*8 {
		t.Fatal("bad initial writer state")
	}

	writer.WriteBits(0, 1)
	writer.WriteBits(1, 1)
	writer.WriteBits(10, 8)
	writer.WriteBits(255, 8)
	writer.WriteBits(1000, 10)
	writer.WriteBits(50000, 16)
	writer.WriteBits(9999999, 32)
	writer.FlushBits()

	const bitsWritten = 1 + 1 + 8 + 8 + 10 + 16 + 32

	if writer.BytesWritten() != 10 {
		t.Fatalf("expected 10 bytes written, got %d", writer.BytesWritten())
	}
	if writer.BitsWritten() != bitsWritten {
		t.Fatalf("expected %d bits written, got %d", bitsWritten, writer.BitsWritten())
	}
	if writer.BitsAvailable() != bufferSize*8-bitsWritten {
		t.Fatal("bad bits available")
	}

	bytesWritten := writer.BytesWritten()

	// read twice: once with slack past the data (branchless window loads), once from an
	// exact size buffer with no slack (tail path)
	packets := [][]byte{
		writer.Data(),                         // cap extends into the original buffer
		append([]byte(nil), writer.Data()...), // exact allocation
	}

	for _, packet := range packets {
		reader := NewBitReader(packet)

		if reader.BitsRead() != 0 || reader.BitsRemaining() != bytesWritten*8 {
			t.Fatal("bad initial reader state")
		}

		a := reader.ReadBits(1)
		b := reader.ReadBits(1)
		c := reader.ReadBits(8)
		d := reader.ReadBits(8)
		e := reader.ReadBits(10)
		f := reader.ReadBits(16)
		g := reader.ReadBits(32)

		if a != 0 || b != 1 || c != 10 || d != 255 || e != 1000 || f != 50000 || g != 9999999 {
			t.Fatalf("read values do not match written values: %d %d %d %d %d %d %d", a, b, c, d, e, f, g)
		}

		if reader.BitsRead() != bitsWritten {
			t.Fatal("bad bits read")
		}
		if reader.BitsRemaining() != bytesWritten*8-bitsWritten {
			t.Fatal("bad bits remaining")
		}
		if reader.WouldReadPastEnd(int(reader.BitsRemaining())) {
			t.Fatal("reading the remaining bits must not read past the end")
		}
		if !reader.WouldReadPastEnd(int(reader.BitsRemaining()) + 1) {
			t.Fatal("reading past the remaining bits must report past the end")
		}
	}
}

func TestBitsRequired(t *testing.T) {
	cases := []struct {
		min, max uint32
		expected int
	}{
		{0, 0, 0}, {0, 1, 1}, {0, 2, 2}, {0, 3, 2}, {0, 4, 3}, {0, 5, 3}, {0, 6, 3},
		{0, 7, 3}, {0, 8, 4}, {0, 255, 8}, {0, 65535, 16}, {0, 4294967295, 32},
	}
	for _, c := range cases {
		if got := BitsRequired(c.min, c.max); got != c.expected {
			t.Fatalf("BitsRequired(%d,%d) = %d, expected %d", c.min, c.max, got, c.expected)
		}
	}
}

func TestBitsRequired64(t *testing.T) {
	minInt64 := int64(math.MinInt64)
	maxInt64 := int64(math.MaxInt64)
	negFiveBillion := int64(-5000000000)
	posFiveBillion := int64(+5000000000)

	cases := []struct {
		min, max uint64
		expected int
	}{
		{0, 0, 0}, {0, 1, 1}, {0, 255, 8}, {0, 4294967295, 32}, {0, 4294967296, 33},
		{0, 1 << 40, 41}, {0, 0xFFFFFFFFFFFFFFFF, 64},
		{uint64(minInt64), uint64(maxInt64), 64},
		{uint64(negFiveBillion), uint64(posFiveBillion), 34},
	}
	for _, c := range cases {
		if got := BitsRequired64(c.min, c.max); got != c.expected {
			t.Fatalf("BitsRequired64(%d,%d) = %d, expected %d", c.min, c.max, got, c.expected)
		}
	}
}

func TestZigZag(t *testing.T) {
	encoded := []struct {
		signed   int32
		unsigned uint32
	}{
		{0, 0}, {-1, 1}, {+1, 2}, {-2, 3}, {+2, 4},
		{math.MaxInt32, 0xFFFFFFFE}, {math.MinInt32, 0xFFFFFFFF},
	}
	for _, c := range encoded {
		if SignedToUnsigned(c.signed) != c.unsigned {
			t.Fatalf("SignedToUnsigned(%d) != %d", c.signed, c.unsigned)
		}
		if UnsignedToSigned(c.unsigned) != c.signed {
			t.Fatalf("UnsignedToSigned(%d) != %d", c.unsigned, c.signed)
		}
	}

	values := []int32{0, -1, +1, -2, +2, 12345, -12345, math.MaxInt32, math.MinInt32}
	for _, v := range values {
		if UnsignedToSigned(SignedToUnsigned(v)) != v {
			t.Fatalf("zigzag round trip failed for %d", v)
		}
	}
}

const maxItems = 11

type testContext struct {
	min, max int32
}

type testData struct {
	a, b, c              int32
	d, e, f              uint32
	g                    bool
	numItems             int32
	items                [maxItems]int32
	floatValue           float32
	compressedFloatValue float32
	doubleValue          float64
	uint8Value           uint8
	uint16Value          uint16
	uint32Value          uint32
	uint64Value          uint64
	intRelative          int32
	int64Full            int64
	int64Range           int64
	bytes                [17]byte
	str                  string
	wstr                 string
}

type testObject struct {
	data testData
}

func (o *testObject) init() {
	o.data.a = 1
	o.data.b = -2
	o.data.c = 150
	o.data.d = 55
	o.data.e = 255
	o.data.f = 127
	o.data.g = true

	o.data.numItems = maxItems / 2
	for i := int32(0); i < o.data.numItems; i++ {
		o.data.items[i] = i + 10
	}

	o.data.compressedFloatValue = 2.13
	o.data.floatValue = 3.1415926
	o.data.doubleValue = 1.0 / 3.0
	o.data.uint8Value = 123
	o.data.uint16Value = 0x1234
	o.data.uint32Value = 0x12345678
	o.data.uint64Value = 0x1234567898765432
	o.data.intRelative = 5
	o.data.int64Full = -123456789012345
	o.data.int64Range = 4123456789

	for i := range o.data.bytes {
		o.data.bytes[i] = byte((i + 5) * 13)
	}

	o.data.str = "hello world!"
	o.data.wstr = "привіт, світ!"
}

func (o *testObject) Serialize(stream Stream) error {
	context := stream.Context().(*testContext)

	stream.SerializeInt(&o.data.a, context.min, context.max)
	stream.SerializeInt(&o.data.b, context.min, context.max)

	stream.SerializeInt(&o.data.c, -100, 10000)

	stream.SerializeBits(&o.data.d, 6)
	stream.SerializeBits(&o.data.e, 8)
	stream.SerializeBits(&o.data.f, 7)

	stream.SerializeAlign()

	stream.SerializeBool(&o.data.g)

	// numItems controls the loop below, so its error must be checked before the loop:
	// on a truncated packet the failed read leaves the previous value in place
	if err := stream.SerializeInt(&o.data.numItems, 0, maxItems-1); err != nil {
		return err
	}
	for i := int32(0); i < o.data.numItems; i++ {
		item := uint32(o.data.items[i])
		stream.SerializeBits(&item, 8)
		o.data.items[i] = int32(item)
	}

	stream.SerializeFloat32(&o.data.floatValue)

	stream.SerializeCompressedFloat32(&o.data.compressedFloatValue, 0, 10, 0.01)

	stream.SerializeFloat64(&o.data.doubleValue)

	stream.SerializeUint8(&o.data.uint8Value)
	stream.SerializeUint16(&o.data.uint16Value)
	stream.SerializeUint32(&o.data.uint32Value)
	stream.SerializeUint64(&o.data.uint64Value)

	stream.SerializeIntRelative(o.data.a, &o.data.intRelative)

	stream.SerializeInt64(&o.data.int64Full, math.MinInt64, math.MaxInt64)
	stream.SerializeInt64(&o.data.int64Range, -5000000000, +5000000000)

	stream.SerializeBytes(o.data.bytes[:])

	stream.SerializeString(&o.data.str, 256)
	stream.SerializeWideString(&o.data.wstr, 256)

	return stream.Err()
}

func TestSerialize(t *testing.T) {
	buffer := make([]byte, 1024)

	context := &testContext{min: -10, max: +10}

	writeStream := NewWriteStream(buffer)
	writeStream.SetContext(context)

	writeObject := &testObject{}
	writeObject.init()
	if err := writeObject.Serialize(writeStream); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	writeStream.Flush()

	readObject := &testObject{}
	readStream := NewReadStream(writeStream.Data())
	readStream.SetContext(context)
	if err := readObject.Serialize(readStream); err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if readObject.data != writeObject.data {
		t.Fatalf("read object does not match written object:\nwrote %+v\nread  %+v", writeObject.data, readObject.data)
	}
}

// readFunction reads with a concrete *ReadStream, checking each value as it is read.
// This is the Go equivalent of writing separate read and write functions with the
// read_* and write_* macros in the C++ library.
func readFunction(t *testing.T, readStream *ReadStream) {
	t.Helper()

	fatalIf := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	var u32 uint32
	fatalIf(readStream.SerializeBits(&u32, 4))
	if u32 != 13 {
		t.Fatalf("expected 13, got %d", u32)
	}

	var flag bool
	fatalIf(readStream.SerializeBool(&flag))
	if !flag {
		t.Fatal("expected true")
	}

	var u8 uint8
	fatalIf(readStream.SerializeUint8(&u8))
	if u8 != 255 {
		t.Fatalf("expected 255, got %d", u8)
	}

	var u16 uint16
	fatalIf(readStream.SerializeUint16(&u16))
	if u16 != 65535 {
		t.Fatalf("expected 65535, got %d", u16)
	}

	fatalIf(readStream.SerializeUint32(&u32))
	if u32 != 0xFFFFFFFF {
		t.Fatalf("expected 0xFFFFFFFF, got %#x", u32)
	}

	var u64 uint64
	fatalIf(readStream.SerializeUint64(&u64))
	if u64 != 0xFFFFFFFFFFFFFFFF {
		t.Fatalf("expected 0xFFFFFFFFFFFFFFFF, got %#x", u64) // i am very full
	}

	var i32 int32
	fatalIf(readStream.SerializeInt(&i32, 10, 90))
	if i32 != 55 {
		t.Fatalf("expected 55, got %d", i32)
	}

	var i64 int64
	fatalIf(readStream.SerializeInt64(&i64, -60000000000, 60000000000))
	if i64 != -50000000001 {
		t.Fatalf("expected -50000000001, got %d", i64)
	}

	var f32 float32
	fatalIf(readStream.SerializeFloat32(&f32))
	if f32 != 100.0 {
		t.Fatalf("expected 100.0, got %f", f32)
	}

	var f64 float64
	fatalIf(readStream.SerializeFloat64(&f64))
	if f64 != 1000000000.0 {
		t.Fatalf("expected 1000000000.0, got %f", f64)
	}

	data := make([]byte, 5)
	fatalIf(readStream.SerializeBytes(data))
	if !bytes.Equal(data, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("expected {1,2,3,4,5}, got %v", data)
	}

	var str string
	fatalIf(readStream.SerializeString(&str, 10))
	if str != "hello" {
		t.Fatalf("expected \"hello\", got %q", str)
	}

	var wstr string
	fatalIf(readStream.SerializeWideString(&wstr, 20))
	if wstr != "привіт" {
		t.Fatalf("expected \"привіт\", got %q", wstr)
	}

	fatalIf(readStream.SerializeAlign())

	context := &testContext{min: -10, max: +10}
	readStream.SetContext(context)

	expectedObject := &testObject{}
	expectedObject.init()

	readObject := &testObject{}
	fatalIf(readStream.SerializeObject(readObject))
	if readObject.data != expectedObject.data {
		t.Fatal("read object does not match expected object")
	}

	var relative int32
	fatalIf(readStream.SerializeIntRelative(100, &relative))
	if relative != 105 {
		t.Fatalf("expected 105, got %d", relative)
	}
}

func TestReadWrite(t *testing.T) {
	buffer := make([]byte, 10*1024)

	// write to the buffer with a concrete *WriteStream
	writeStream := NewWriteStream(buffer)

	u32 := uint32(13)
	writeStream.SerializeBits(&u32, 4)
	flag := true
	writeStream.SerializeBool(&flag)
	u8 := uint8(255)
	writeStream.SerializeUint8(&u8)
	u16 := uint16(65535)
	writeStream.SerializeUint16(&u16)
	u32 = 0xFFFFFFFF
	writeStream.SerializeUint32(&u32)
	u64 := uint64(0xFFFFFFFFFFFFFFFF)
	writeStream.SerializeUint64(&u64)
	i32 := int32(55)
	writeStream.SerializeInt(&i32, 10, 90)
	i64 := int64(-50000000001)
	writeStream.SerializeInt64(&i64, -60000000000, 60000000000)
	f32 := float32(100.0)
	writeStream.SerializeFloat32(&f32)
	f64 := float64(1000000000.0)
	writeStream.SerializeFloat64(&f64)

	writeStream.SerializeBytes([]byte{1, 2, 3, 4, 5})

	str := "hello"
	writeStream.SerializeString(&str, 10)

	wstr := "привіт"
	writeStream.SerializeWideString(&wstr, 20)

	writeStream.SerializeAlign()

	context := &testContext{min: -10, max: +10}
	writeStream.SetContext(context)

	object := &testObject{}
	object.init()
	writeStream.SerializeObject(object)

	relative := int32(105)
	writeStream.SerializeIntRelative(100, &relative)

	if err := writeStream.Err(); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	writeStream.Flush()

	// read back from the buffer
	readStream := NewReadStream(writeStream.Data())
	readFunction(t, readStream)
}

func TestSerializeIntegerValidation(t *testing.T) {
	// BitsRequired(0,5) is 3 bits, so a malicious packet can encode 6 or 7.
	// reads must reject values above max.
	buffer := make([]byte, 8)

	writeStream := NewWriteStream(buffer)
	outOfRange := uint32(7)
	writeStream.SerializeBits(&outOfRange, 3)
	writeStream.Flush()

	readStream := NewReadStream(buffer[:4])
	var value int32
	if err := readStream.SerializeInt(&value, 0, 5); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("expected ErrValueOutOfRange, got %v", err)
	}
	if !errors.Is(readStream.Err(), ErrValueOutOfRange) {
		t.Fatal("expected the error to latch on the stream")
	}
}

func TestSerializeIntegerFullRange(t *testing.T) {
	// ranges wider than 2^31 overflow if [min,max] arithmetic is done signed
	values := []int32{math.MinInt32, math.MinInt32 + 1, -1, 0, +1, math.MaxInt32 - 1, math.MaxInt32}

	for _, expected := range values {
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		v := expected
		if err := writeStream.SerializeInt(&v, math.MinInt32, math.MaxInt32); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value int32
		if err := readStream.SerializeInt(&value, math.MinInt32, math.MaxInt32); err != nil {
			t.Fatal(err)
		}
		if value != expected {
			t.Fatalf("expected %d, got %d", expected, value)
		}
	}

	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		v := int32(1000000000)
		if err := writeStream.SerializeInt(&v, -2000000000, 2000000000); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value int32
		if err := readStream.SerializeInt(&value, -2000000000, 2000000000); err != nil {
			t.Fatal(err)
		}
		if value != 1000000000 {
			t.Fatalf("expected 1000000000, got %d", value)
		}
	}
}

func TestSerializeInt64FullRange(t *testing.T) {
	// ranges wider than 2^63 overflow if [min,max] arithmetic is done signed
	{
		values := []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, +1, math.MaxInt64 - 1, math.MaxInt64}

		for _, expected := range values {
			buffer := make([]byte, 16)

			writeStream := NewWriteStream(buffer)
			v := expected
			if err := writeStream.SerializeInt64(&v, math.MinInt64, math.MaxInt64); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()

			readStream := NewReadStream(buffer)
			var value int64
			if err := readStream.SerializeInt64(&value, math.MinInt64, math.MaxInt64); err != nil {
				t.Fatal(err)
			}
			if value != expected {
				t.Fatalf("expected %d, got %d", expected, value)
			}
		}
	}

	// ranges spanning more than 32 bits use the two dword path
	{
		const min, max = -5000000000, +5000000000
		values := []int64{min, min + 1, -1, 0, +1, 4123456789, max - 1, max}

		for _, expected := range values {
			buffer := make([]byte, 16)

			writeStream := NewWriteStream(buffer)
			v := expected
			if err := writeStream.SerializeInt64(&v, min, max); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()

			readStream := NewReadStream(buffer)
			var value int64
			if err := readStream.SerializeInt64(&value, min, max); err != nil {
				t.Fatal(err)
			}
			if value != expected {
				t.Fatalf("expected %d, got %d", expected, value)
			}
		}
	}

	// small ranges use the single dword path and the minimal number of bits
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		v := int64(55)
		if err := writeStream.SerializeInt64(&v, -100, +100); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		if writeStream.BitsProcessed() != 8 { // BitsRequired64(-100,100) == 8, same as the 32 bit path
			t.Fatalf("expected 8 bits processed, got %d", writeStream.BitsProcessed())
		}

		readStream := NewReadStream(buffer)
		var value int64
		if err := readStream.SerializeInt64(&value, -100, +100); err != nil {
			t.Fatal(err)
		}
		if value != 55 {
			t.Fatalf("expected 55, got %d", value)
		}
	}
}

func TestSerializeInt64Validation(t *testing.T) {
	// a malicious packet can smuggle an out of range value into the bit headroom of the
	// two dword path. reads must reject it.
	{
		buffer := make([]byte, 16)

		writeStream := NewWriteStream(buffer)
		const outOfRange = uint64(1)<<34 + 5 // range [0, 2^34] is 35 bits, so values above 2^34 fit in the headroom
		lo := uint32(outOfRange & 0xFFFFFFFF)
		hi := uint32(outOfRange >> 32)
		writeStream.SerializeBits(&lo, 32)
		writeStream.SerializeBits(&hi, 3)
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value int64
		if err := readStream.SerializeInt64(&value, 0, 1<<34); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}

	// reads past the end of the buffer must fail cleanly
	{
		buffer := make([]byte, 4)

		readStream := NewReadStream(buffer)
		var value int64
		if err := readStream.SerializeInt64(&value, math.MinInt64, math.MaxInt64); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}
}

func TestSerializeBytesValidation(t *testing.T) {
	// byte counts past the end of the stream must be rejected
	buffer := make([]byte, 16)

	{
		readStream := NewReadStream(buffer)
		data := make([]byte, 17)
		if err := readStream.SerializeBytes(data); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}

	{
		readStream := NewReadStream(buffer)
		data := make([]byte, 1<<20)
		if err := readStream.SerializeBytes(data); !errors.Is(err, ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", err)
		}
	}
}

func TestIntRelativeValidation(t *testing.T) {
	// the 32 bit fallback must reject values that violate the previous < current contract
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		sixFalseBools := uint32(0)
		writeStream.SerializeBits(&sixFalseBools, 6)
		badCurrent := uint32(50)
		writeStream.SerializeBits(&badCurrent, 32)
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var current int32
		if err := readStream.SerializeIntRelative(100, &current); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}

	// a legitimate fallback round trip must still succeed
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := int32(100000)
		if err := writeStream.SerializeIntRelative(100, &written); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var current int32
		if err := readStream.SerializeIntRelative(100, &current); err != nil {
			t.Fatal(err)
		}
		if current != written {
			t.Fatalf("expected %d, got %d", written, current)
		}
	}

	// gaps wider than 2^31 overflow if the difference is computed in signed arithmetic
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := int32(math.MaxInt32)
		if err := writeStream.SerializeIntRelative(-1000, &written); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var current int32
		if err := readStream.SerializeIntRelative(-1000, &current); err != nil {
			t.Fatal(err)
		}
		if current != written {
			t.Fatalf("expected %d, got %d", written, current)
		}
	}

	// read side reconstructs current = previous + difference; a large previous must wrap
	// in the unsigned domain rather than overflow
	{
		differences := []int32{1, 5} // 1 exercises the one bit branch, 5 exercises a bucket branch

		for _, difference := range differences {
			buffer := make([]byte, 8)

			writeStream := NewWriteStream(buffer)
			written := 10 + difference
			if err := writeStream.SerializeIntRelative(10, &written); err != nil {
				t.Fatal(err)
			}
			writeStream.Flush()

			readStream := NewReadStream(buffer)
			var current int32
			if err := readStream.SerializeIntRelative(math.MaxInt32, &current); err != nil {
				t.Fatal(err)
			}
			expected := int32(uint32(math.MaxInt32) + uint32(difference))
			if current != expected {
				t.Fatalf("expected %d, got %d", expected, current)
			}
		}
	}
}

func TestCompressedFloatValidation(t *testing.T) {
	// a malicious packet can encode integer values above maxIntegerValue in the bit
	// headroom. reads must reject them.
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		outOfRange := uint32(1023) // maxIntegerValue is 1000 for [0,10] at res 0.01 -> 10 bits
		writeStream.SerializeBits(&outOfRange, 10)
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32(&value, 0, 10, 0.01); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}

	// huge delta / resolution ratios must not overflow the uint32 quantization range
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := float32(5000000000.0)
		if err := writeStream.SerializeCompressedFloat32(&written, 0, 10000000000.0, 1.0); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32(&value, 0, 10000000000.0, 1.0); err != nil {
			t.Fatal(err)
		}
		if math.Abs(float64(value-written)) > 4096.0 {
			t.Fatalf("expected %f within 4096, got %f", written, value)
		}
	}

	// a NaN value must quantize into range rather than corrupt the stream
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := math.Float32frombits(0x7fc00000) // quiet NaN
		if err := writeStream.SerializeCompressedFloat32(&written, 0, 10, 0.01); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		value := float32(-1.0)
		if err := readStream.SerializeCompressedFloat32(&value, 0, 10, 0.01); err != nil {
			t.Fatal(err)
		}
		if !(value >= 0.0 && value <= 10.0) { // NaN clamps to the low end of the range
			t.Fatalf("expected value in [0,10], got %f", value)
		}
	}
}

// TestCompressedFloatTopOfRangeClamp mirrors the C++
// test_compressed_float_top_of_range_clamp (mas-bandwidth/serialize#88):
// STANDARD.md's normative integer clamp (2026-08-23, schema#109; ruling: Glenn,
// live). In [2^23, 2^24) the float32 ulp is 1, so scaled + 0.5 lands on a tie and
// round-to-even can push the quantized code past maxIntegerValue. Before the clamp,
// witness A wrote a top-of-range code its own reader rejected, and witness B wrote
// a code one bit wider than the field.
func TestCompressedFloatTopOfRangeClamp(t *testing.T) {
	// witness A: [0, 8388609] at resolution 1 -> maxIntegerValue 2^23+1, 24 bits.
	// unclamped, writing max rounds to 8388610 and the reader rejects it.
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := float32(8388609.0)
		if err := writeStream.SerializeCompressedFloat32(&written, 0, 8388609.0, 1.0); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32(&value, 0, 8388609.0, 1.0); err != nil {
			t.Fatal(err)
		}
		if value != 8388609.0 {
			t.Fatalf("expected 8388609, got %f", value)
		}
	}

	// witness B: [0, 16777215] at resolution 1 -> maxIntegerValue 2^24-1, 24 bits.
	// unclamped, writing max rounds to 2^24 — one bit wider than the field.
	{
		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := float32(16777215.0)
		if err := writeStream.SerializeCompressedFloat32(&written, 0, 16777215.0, 1.0); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32(&value, 0, 16777215.0, 1.0); err != nil {
			t.Fatal(err)
		}
		if value != 16777215.0 {
			t.Fatalf("expected 16777215, got %f", value)
		}
	}

	// both witnesses again through the precomputed entry point: this port's second
	// writer entry point reaches the same audited home, so it must clamp too
	for _, max := range []float32{8388609.0, 16777215.0} {
		maxIntegerValue, bits, delta := CompressedFloatParams(0, max, 1.0)

		buffer := make([]byte, 8)

		writeStream := NewWriteStream(buffer)
		written := max
		if err := writeStream.SerializeCompressedFloat32Precomputed(&written, maxIntegerValue, bits, delta, 0); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value float32
		if err := readStream.SerializeCompressedFloat32Precomputed(&value, maxIntegerValue, bits, delta, 0); err != nil {
			t.Fatal(err)
		}
		if value != max {
			t.Fatalf("expected %f, got %f", max, value)
		}
	}
}

// Golden wire format test. The exact bytes produced by the serializer are pinned down
// here and must never change. They are copied verbatim from the C++ serialize library
// test suite, so this test also proves the Go implementation is wire compatible with
// the C++ implementation.

type goldenWireData struct {
	bits4                uint32
	bits11               uint32
	bits24               uint32
	bits32               uint32
	intSmall             int32
	intFull              int32
	flag                 bool
	floatValue           float32
	compressedFloatValue float32
	doubleValue          float64
	uint8Value           uint8
	uint16Value          uint16
	uint32Value          uint32
	uint64Value          uint64
	relativeNear         int32
	relativeFar          int32
	bytes                [7]byte
	str                  string
	wstr                 string
	fixedQ8_8            int64
	fixedQ16_16          int64
	fixedQ48_16          int64
	fixedQ16_16Unsigned  int64
	fixedQ112_16Wide     Int128
	fixedQ64_64Wide      Int128
}

func goldenWireInit() goldenWireData {
	return goldenWireData{
		bits4:                13,
		bits11:               1445,
		bits24:               11259375,
		bits32:               0xDEADBEEF,
		intSmall:             -37,
		intFull:              -123456789,
		flag:                 true,
		floatValue:           3.1415926,
		compressedFloatValue: 5.0, // 5.0 in [0,10] normalizes to exactly 0.5: quantizes identically everywhere
		doubleValue:          1.0 / 3.0,
		uint8Value:           0x7F,
		uint16Value:          0x1234,
		uint32Value:          0x12345678,
		uint64Value:          0x123456789ABCDEF0,
		relativeNear:         101,  // difference of 1 from the base: exercises the one bit branch
		relativeFar:          2100, // difference of 2000 from the base: exercises the twelve bit bucket
		bytes:                [7]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0x01},
		str:                  "golden",
		wstr:                 "мир",                                     // cyrillic, BMP only
		fixedQ8_8:            -(3*256 + 64),                             // -3.25 in Q8.8
		fixedQ16_16:          1234*65536 + 32768,                        // 1234.5 in Q16.16
		fixedQ48_16:          -(54321*65536 + 12345),                    // -54321.1883... in Q48.16
		fixedQ16_16Unsigned:  29999*65536 + 65535,                       // 29999.99998... in Q16.16: every fraction bit set
		fixedQ112_16Wide:     Int128From64(-(98765432109*65536 + 4321)), // -98765432109.066 in Q112.16: 75 bits on the wire, three groups
		fixedQ64_64Wide: Int128From64(0x0123456789ABCDEF).Lsh(64).
			Add(Int128From64(0x0FEDCBA987654321)), // Q64.64 over the full unit range: 128 bits, four groups, every group distinct
	}
}

func goldenWireSerialize(stream Stream, data *goldenWireData) error {
	const relativeBase = 100
	stream.SerializeBits(&data.bits4, 4)
	stream.SerializeBits(&data.bits11, 11)
	stream.SerializeBits(&data.bits24, 24)
	stream.SerializeBits(&data.bits32, 32)
	stream.SerializeInt(&data.intSmall, -100, +100)
	stream.SerializeInt(&data.intFull, math.MinInt32, math.MaxInt32)
	stream.SerializeBool(&data.flag)
	stream.SerializeFloat32(&data.floatValue)
	stream.SerializeCompressedFloat32(&data.compressedFloatValue, 0.0, 10.0, 0.01)
	stream.SerializeFloat64(&data.doubleValue)
	stream.SerializeUint8(&data.uint8Value)
	stream.SerializeUint16(&data.uint16Value)
	stream.SerializeUint32(&data.uint32Value)
	stream.SerializeUint64(&data.uint64Value)
	stream.SerializeIntRelative(relativeBase, &data.relativeNear)
	stream.SerializeIntRelative(relativeBase, &data.relativeFar)
	stream.SerializeAlign()
	stream.SerializeBytes(data.bytes[:])
	stream.SerializeString(&data.str, 16)
	stream.SerializeWideString(&data.wstr, 8)
	stream.SerializeAlign() // the fixed point section starts byte aligned, so every byte pinned above it stays put
	stream.SerializeFixed64(&data.fixedQ8_8, 8, 8, -100, +100)
	stream.SerializeFixed64(&data.fixedQ16_16, 16, 16, -2000, +2000)
	stream.SerializeFixed64(&data.fixedQ48_16, 48, 16, -100000, +100000)
	stream.SerializeFixed64(&data.fixedQ16_16Unsigned, 16, 16, 0, 30000)
	stream.SerializeAlign()                                                                             // the wide fixed section starts byte aligned, so every byte pinned above it stays put
	stream.SerializeFixed128(&data.fixedQ112_16Wide, 112, 16, -144115188075855872, +144115188075855872) // ±2^57 units: 75 bits, the three group structure
	stream.SerializeFixed128(&data.fixedQ64_64Wide, 64, 64, math.MinInt64, math.MaxInt64)               // full unit range: 128 bits, the four group structure
	return stream.Err()
}

// goldenWireBytes are copied verbatim from the C++ serialize library test suite.
// The fixed point tail (the 40 bytes from offset 72) was additionally re-derived
// from STANDARD.md's stated rules alone — offset encoding over the raw (scaled)
// bounds, 32 bit groups from least significant upward — and the derivation agrees
// with the C++ bytes exactly, so this pin is Go, C++ and the document agreeing.
var goldenWireBytes = []byte{
	0x5D, 0xDA, 0xF7, 0xE6, 0xD5, 0x77, 0xDF, 0x56, 0xEF, 0x9F, 0x75, 0x19,
	0x52, 0xBC, 0xDA, 0x0F, 0x49, 0x40, 0xF4, 0x55, 0x55, 0x55, 0x55, 0x55,
	0x55, 0x55, 0xFF, 0xFC, 0xD1, 0x48, 0xE0, 0x59, 0xD1, 0x48, 0xC0, 0x7B,
	0xF3, 0x6A, 0xE2, 0x59, 0xD1, 0x48, 0x84, 0xB7, 0x06, 0xDE, 0xAD, 0xBE,
	0xEF, 0xCA, 0xFE, 0x01, 0x06, 0x67, 0x6F, 0x6C, 0x64, 0x65, 0x6E, 0xE3,
	0x21, 0x00, 0x00, 0xC0, 0x21, 0x00, 0x00, 0x00, 0x22, 0x00, 0x00, 0x00,
	0xC0, 0x60, 0x00, 0x80, 0xA2, 0x7C, 0xFC, 0xEC, 0x26, 0xCB, 0xFF, 0xFF,
	0x4B, 0x1D, 0x1F, 0xEF, 0xD2, 0x1A, 0x1F, 0x01, 0xE9, 0xFF, 0xFF, 0x09,
	0x19, 0x2A, 0x3B, 0x4C, 0x5D, 0x6E, 0x7F, 0x78, 0x6F, 0x5E, 0x4D, 0x3C,
	0x2B, 0x1A, 0x09, 0x04,
}

func TestGoldenWireFormat(t *testing.T) {
	// write side: serializing the golden values must produce exactly the golden bytes
	{
		buffer := make([]byte, 256)
		stream := NewWriteStream(buffer)
		data := goldenWireInit()
		if err := goldenWireSerialize(stream, &data); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		stream.Flush()
		if stream.BytesProcessed() != int64(len(goldenWireBytes)) {
			t.Fatalf("expected %d bytes, got %d", len(goldenWireBytes), stream.BytesProcessed())
		}
		if !bytes.Equal(stream.Data(), goldenWireBytes) {
			t.Fatalf("golden bytes mismatch:\nexpected %x\ngot      %x", goldenWireBytes, stream.Data())
		}
	}

	// read side: the golden bytes must decode to the expected values, on every platform, forever
	{
		stream := NewReadStream(goldenWireBytes)
		var data goldenWireData
		if err := goldenWireSerialize(stream, &data); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		expected := goldenWireInit()
		if math.Abs(float64(data.compressedFloatValue-expected.compressedFloatValue)) > 0.01 {
			t.Fatalf("compressed float mismatch: expected %f, got %f", expected.compressedFloatValue, data.compressedFloatValue)
		}
		data.compressedFloatValue = expected.compressedFloatValue
		if data != expected {
			t.Fatalf("golden decode mismatch:\nexpected %+v\ngot      %+v", expected, data)
		}
	}
}

func TestUnalignedWriter(t *testing.T) {
	// buffers do not need any particular alignment: exercise every offset within a qword,
	// covering the WriteBits, WriteBytes and FlushBits store paths
	storage := make([]byte, 256+8)

	for offset := range 8 {
		for i := range storage {
			storage[i] = 0
		}

		buffer := storage[offset : offset+256]

		data := make([]byte, 13)
		for i := range data {
			data[i] = byte(i*47 + offset)
		}

		writeStream := NewWriteStream(buffer)
		a := uint32(0x12345678)
		writeStream.SerializeBits(&a, 32)
		b := uint32(123)
		writeStream.SerializeBits(&b, 7)
		writeStream.SerializeBytes(data)
		c := uint32(0xDEADBEEF)
		writeStream.SerializeBits(&c, 32)
		if err := writeStream.Err(); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(writeStream.Data())
		var ra, rb, rc uint32
		readData := make([]byte, len(data))
		if err := readStream.SerializeBits(&ra, 32); err != nil {
			t.Fatal(err)
		}
		if err := readStream.SerializeBits(&rb, 7); err != nil {
			t.Fatal(err)
		}
		if err := readStream.SerializeBytes(readData); err != nil {
			t.Fatal(err)
		}
		if err := readStream.SerializeBits(&rc, 32); err != nil {
			t.Fatal(err)
		}
		if ra != a || rb != b || rc != c || !bytes.Equal(readData, data) {
			t.Fatalf("offset %d: read values do not match written values", offset)
		}
	}
}

func TestLargeBuffer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large buffer test in short mode")
	}

	// bit counts are 64 bit, so buffers larger than 256 MB work. write a bulk block that
	// carries the stream past the 2^31 bit boundary, then verify that bitpacked values
	// round trip on the far side of it.
	const bufferSize = 320 * 1024 * 1024
	buffer := make([]byte, bufferSize)

	chunk := make([]byte, 1024*1024)
	for i := range chunk {
		chunk[i] = byte(i * 37)
	}

	const numChunks = 300 // 300 MB of bulk data: past the 256 MB boundary

	writeStream := NewWriteStream(buffer)
	for range numChunks {
		if err := writeStream.SerializeBytes(chunk); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := uint32(0xDEADBEEF)
	if err := writeStream.SerializeBits(&sentinel, 32); err != nil {
		t.Fatal(err)
	}
	value := int32(-12345)
	if err := writeStream.SerializeInt(&value, -100000, +100000); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()
	if writeStream.BitsProcessed() <= 1<<31 {
		t.Fatal("expected the bit count to cross the 2^31 boundary")
	}

	readStream := NewReadStream(writeStream.Data())
	readChunk := make([]byte, len(chunk))
	for range numChunks {
		if err := readStream.SerializeBytes(readChunk); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(readChunk, chunk) {
		t.Fatal("the final chunk did not round trip")
	}
	var readSentinel uint32
	if err := readStream.SerializeBits(&readSentinel, 32); err != nil {
		t.Fatal(err)
	}
	if readSentinel != sentinel {
		t.Fatalf("expected sentinel %#x, got %#x", sentinel, readSentinel)
	}
	var readValue int32
	if err := readStream.SerializeInt(&readValue, -100000, +100000); err != nil {
		t.Fatal(err)
	}
	if readValue != value {
		t.Fatalf("expected %d, got %d", value, readValue)
	}
	if readStream.BitsProcessed() <= 1<<31 {
		t.Fatal("expected the read bit count to cross the 2^31 boundary")
	}

	// measuring a single block of 256 MB or more crosses 2^31 bits in one call: the
	// multiply must happen in the 64 bit domain even where int is 32 bits
	const measureBytes = 270 * 1024 * 1024
	measureStream := NewMeasureStream()
	if err := measureStream.SerializeBytes(buffer[:measureBytes]); err != nil {
		t.Fatal(err)
	}
	if expected := int64(7) + int64(measureBytes)*8; measureStream.BitsProcessed() != expected {
		t.Fatalf("expected %d measured bits, got %d", expected, measureStream.BitsProcessed())
	}
}

func TestWriteOverflow(t *testing.T) {
	buffer := make([]byte, 8)

	stream := NewWriteStream(buffer)
	v := uint32(1)
	if err := stream.SerializeBits(&v, 32); err != nil {
		t.Fatal(err)
	}
	if err := stream.SerializeBits(&v, 32); err != nil {
		t.Fatal(err)
	}
	if err := stream.SerializeBits(&v, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}

	// the error is sticky: every later call reports it without touching the stream
	flag := true
	if err := stream.SerializeBool(&flag); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected sticky ErrOverflow, got %v", err)
	}
	if !errors.Is(stream.Err(), ErrOverflow) {
		t.Fatal("expected the error to latch on the stream")
	}
	if stream.BitsProcessed() != 64 {
		t.Fatal("failed serialize calls must not advance the stream")
	}
}

func TestAlignValidation(t *testing.T) {
	// nonzero padding bits mean the read and write serialize functions don't match
	buffer := make([]byte, 8)
	buffer[0] = 0xFF

	stream := NewReadStream(buffer)
	var v uint32
	if err := stream.SerializeBits(&v, 3); err != nil {
		t.Fatal(err)
	}
	if err := stream.SerializeAlign(); !errors.Is(err, ErrAlign) {
		t.Fatalf("expected ErrAlign, got %v", err)
	}
}

func TestSerializeStringRoundTrip(t *testing.T) {
	// the max length vector was 255 NUL bytes before the reader-validation ruling;
	// interior NUL is now refused on read, so max length is exercised with a
	// conforming payload of the same length
	values := []string{"", "a", "hello world!", "héllo wörld 😀", strings.Repeat("x", 255)}

	for _, expected := range values {
		buffer := make([]byte, 512)

		writeStream := NewWriteStream(buffer)
		v := expected
		if err := writeStream.SerializeString(&v, 256); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(writeStream.Data())
		var value string
		if err := readStream.SerializeString(&value, 256); err != nil {
			t.Fatal(err)
		}
		if value != expected {
			t.Fatalf("expected %q, got %q", expected, value)
		}
	}

	// strings that don't fit in the buffer size must be rejected on write
	{
		buffer := make([]byte, 512)
		writeStream := NewWriteStream(buffer)
		tooLong := string(make([]byte, 256))
		if err := writeStream.SerializeString(&tooLong, 256); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("expected ErrValueOutOfRange, got %v", err)
		}
	}
}

// A string produced by a read must never alias the stream's buffer: overwriting the
// buffer and reading again must not retroactively change a previously returned string.
func TestSerializeStringReadDoesNotAliasBuffer(t *testing.T) {
	buffer := make([]byte, 64)

	writeStream := NewWriteStream(buffer)
	first := "first message"
	if err := writeStream.SerializeString(&first, 32); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()
	data := writeStream.Data()

	readStream := NewReadStream(data)
	var value string
	if err := readStream.SerializeString(&value, 32); err != nil {
		t.Fatal(err)
	}
	saved := value

	// overwrite the wire bytes in place (same length, different content) and read again
	writeStream.Reset(buffer)
	second := "FIRST MESSAGE"
	if err := writeStream.SerializeString(&second, 32); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()

	readStream.Reset(data)
	if err := readStream.SerializeString(&value, 32); err != nil {
		t.Fatal(err)
	}

	if saved != "first message" {
		t.Fatalf("previously returned string was mutated by a later read: %q", saved)
	}
	if value != "FIRST MESSAGE" {
		t.Fatalf("expected %q, got %q", "FIRST MESSAGE", value)
	}
}

// Re-reading content equal to the string already in *value must keep it without
// allocating: chat style reads into reused values are allocation free in steady state.
func TestSerializeStringReadEqualContentDoesNotAllocate(t *testing.T) {
	buffer := make([]byte, 64)

	writeStream := NewWriteStream(buffer)
	v := "player one"
	if err := writeStream.SerializeString(&v, 32); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()
	data := writeStream.Data()

	readStream := NewReadStream(data)
	var value string
	if err := readStream.SerializeString(&value, 32); err != nil { // first read pays the copy
		t.Fatal(err)
	}

	allocs := testing.AllocsPerRun(100, func() {
		readStream.Reset(data)
		if err := readStream.SerializeString(&value, 32); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocations re-reading equal content, got %v", allocs)
	}
	if value != "player one" {
		t.Fatalf("expected %q, got %q", "player one", value)
	}
}

// A read of different content with the same length must replace the value: equality is
// decided on the bytes, never on the length alone.
func TestSerializeStringReadChangedContentSameLength(t *testing.T) {
	writeString := func(v string) []byte {
		buffer := make([]byte, 64)
		writeStream := NewWriteStream(buffer)
		if err := writeStream.SerializeString(&v, 32); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()
		return writeStream.Data()
	}
	dataA := writeString("channel-a")
	dataB := writeString("channel-b") // same length, last byte differs

	readStream := NewReadStream(dataA)
	var value string
	if err := readStream.SerializeString(&value, 32); err != nil {
		t.Fatal(err)
	}
	readStream.Reset(dataB)
	if err := readStream.SerializeString(&value, 32); err != nil {
		t.Fatal(err)
	}
	if value != "channel-b" {
		t.Fatalf("expected %q, got %q", "channel-b", value)
	}
}

func TestSerializeWideStringRoundTrip(t *testing.T) {
	values := []string{"", "мир", "привіт, світ!", "😀🚀"} // BMP and astral code points

	for _, expected := range values {
		buffer := make([]byte, 512)

		writeStream := NewWriteStream(buffer)
		v := expected
		if err := writeStream.SerializeWideString(&v, 64); err != nil {
			t.Fatal(err)
		}
		writeStream.Flush()

		readStream := NewReadStream(writeStream.Data())
		var value string
		if err := readStream.SerializeWideString(&value, 64); err != nil {
			t.Fatal(err)
		}
		if value != expected {
			t.Fatalf("expected %q, got %q", expected, value)
		}
	}

	// groups a Go string cannot hold must be rejected on read: values above 0xFFFF
	// are not UTF-16 code units (0x1F600 is the OLD astral wire — one code point per
	// group — and must not decode), and unpaired surrogates have no UTF-8
	// representation. Each vector is a full group sequence, malformed as described.
	invalidGroupSequences := []struct {
		name   string
		groups []uint32
	}{
		{"lone high surrogate", []uint32{0xD800}},
		{"low surrogate first", []uint32{0xDFFF}},
		{"high surrogate not followed by a low surrogate", []uint32{0xD83D, 0x0041}},
		{"high surrogate followed by a high surrogate", []uint32{0xD83D, 0xD83D}},
		{"raw astral code point: the old wire", []uint32{0x1F600}},
		{"above the code point space", []uint32{0x110000}},
		{"all bits set", []uint32{0xFFFFFFFF}},
	}
	for _, sequence := range invalidGroupSequences {
		buffer := make([]byte, 24)

		writeStream := NewWriteStream(buffer)
		length := int32(len(sequence.groups))
		writeStream.SerializeInt(&length, 0, 63) // length prefix for bufferSize 64
		for _, group := range sequence.groups {
			g := group
			writeStream.SerializeBits(&g, 32)
		}
		writeStream.Flush()

		readStream := NewReadStream(buffer)
		var value string
		if err := readStream.SerializeWideString(&value, 64); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("%s: expected ErrValueOutOfRange, got %v", sequence.name, err)
		}
		if value != "" {
			t.Fatalf("%s: value must be left unmodified on failure, got %q", sequence.name, value)
		}
	}
}

func TestSerializeWideStringUTF16Wire(t *testing.T) {
	// STANDARD.md: each 32 bit group carries one UTF-16 code unit, so an astral code
	// point is TWO groups — a surrogate pair — and the length prefix counts code
	// units, not code points. U+1F600 encodes as 0xD83D 0xDE00: this pins the exact
	// wire, the bytes a 2 byte wchar_t C++ platform has always produced.
	buffer := make([]byte, 32)

	writeStream := NewWriteStream(buffer)
	v := "a\U0001F600" // one basic-plane code point + one astral: three code units
	if err := writeStream.SerializeWideString(&v, 64); err != nil {
		t.Fatal(err)
	}
	if got := writeStream.BitsProcessed(); got != 6+3*32 {
		t.Fatalf("expected %d bits (6 bit length prefix + three 32 bit groups), got %d", 6+3*32, got)
	}
	writeStream.Flush()

	readStream := NewReadStream(writeStream.Data())
	var length int32
	readStream.SerializeInt(&length, 0, 63)
	if length != 3 {
		t.Fatalf("length prefix must count UTF-16 code units: expected 3, got %d", length)
	}
	expectedGroups := []uint32{'a', 0xD83D, 0xDE00} // 'a', then the U+1F600 surrogate pair
	for i, expected := range expectedGroups {
		var group uint32
		readStream.SerializeBits(&group, 32)
		if group != expected {
			t.Fatalf("group %d: expected %#06x, got %#06x", i, expected, group)
		}
	}
	if err := readStream.Err(); err != nil {
		t.Fatal(err)
	}

	// and the pair recombines on read
	readStream = NewReadStream(writeStream.Data())
	var value string
	if err := readStream.SerializeWideString(&value, 64); err != nil {
		t.Fatal(err)
	}
	if value != "a\U0001F600" {
		t.Fatalf("expected %q, got %q", "a\U0001F600", value)
	}

	// the measure stream agrees with the write stream on the unit count
	measureStream := NewMeasureStream()
	m := "a\U0001F600"
	if err := measureStream.SerializeWideString(&m, 64); err != nil {
		t.Fatal(err)
	}
	if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
		t.Fatalf("measure disagrees with write: %d vs %d bits", measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}

	// bufferSize bounds the length in code units: two astral code points are four
	// units, one too many for bufferSize 4 (string + null must fit, C++ convention)
	writeStream = NewWriteStream(make([]byte, 32))
	overflow := "\U0001F600\U0001F680"
	if err := writeStream.SerializeWideString(&overflow, 4); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("expected ErrValueOutOfRange for four code units against bufferSize 4, got %v", err)
	}
}

// TestSerializeWideStringFamilyPin is the cross-family conformance vector: U+1F600
// then U+0041 through an 8 unit buffer. serialize pins it in
// test_wstring_utf16_code_units and serialize.c in test/roundtrip.c — every port
// must produce these exact bytes. The pin is spelled three ways and all three must
// agree: the literal bytes, the wire built from raw bit operations (a 3 bit [0,7]
// length field carrying 3, then one 32 bit group per code unit), and the wstring
// path itself. Go strings hold code points, so the local value is "\U0001F600A"
// and the write path splits the astral code point into the surrogate pair
// 0xD83D 0xDE00 — the same bytes a 2 byte wchar_t platform produces from the pair
// it already holds.
func TestSerializeWideStringFamilyPin(t *testing.T) {
	// the family's pinned bytes: length 3 in a 3 bit field, then 0xD83D, 0xDE00,
	// 0x0041 as 32 bit groups — 99 bits, 13 bytes
	pinnedBytes := []byte{
		0xEB, 0xC1, 0x06, 0x00, 0x00, 0xF0, 0x06, 0x00, 0x08, 0x02, 0x00, 0x00, 0x00,
	}

	// the wire spelled out with raw bit operations must reproduce the literal
	rawStream := NewWriteStream(make([]byte, 64))
	length := int32(3)
	rawStream.SerializeInt(&length, 0, 7)
	for _, group := range []uint32{0xD83D, 0xDE00, 0x0041} {
		g := group
		rawStream.SerializeBits(&g, 32)
	}
	rawStream.Flush()
	if err := rawStream.Err(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawStream.Data(), pinnedBytes) {
		t.Fatalf("raw bit operations disagree with the pinned bytes:\nexpected %x\ngot      %x", pinnedBytes, rawStream.Data())
	}

	// write side: the wstring path must produce exactly the pinned bytes
	writeStream := NewWriteStream(make([]byte, 64))
	v := "\U0001F600A" // one astral code point + one basic-plane: three code units
	if err := writeStream.SerializeWideString(&v, 8); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()
	if got := writeStream.BitsProcessed(); got != 3+3*32 {
		t.Fatalf("expected %d bits (3 bit length prefix + three 32 bit groups), got %d", 3+3*32, got)
	}
	if !bytes.Equal(writeStream.Data(), pinnedBytes) {
		t.Fatalf("wstring path disagrees with the pinned bytes:\nexpected %x\ngot      %x", pinnedBytes, writeStream.Data())
	}

	// the measure stream agrees with the write stream on the unit count
	measureStream := NewMeasureStream()
	m := "\U0001F600A"
	if err := measureStream.SerializeWideString(&m, 8); err != nil {
		t.Fatal(err)
	}
	if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
		t.Fatalf("measure disagrees with write: %d vs %d bits", measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}

	// read side: the pinned bytes decode back to the local representation — the
	// surrogate pair recombines, because Go strings hold code points
	readStream := NewReadStream(pinnedBytes)
	var value string
	if err := readStream.SerializeWideString(&value, 8); err != nil {
		t.Fatal(err)
	}
	if value != "\U0001F600A" {
		t.Fatalf("expected %q, got %q", "\U0001F600A", value)
	}
}

// TestReaderContentValidation pins the reader-validation ruling (2026-08-15): the
// well-formedness rules under string and wstring bind the READ side, because readers
// face untrusted bytes and refusal rules are format. Four doctored streams, one per
// refusal — none producible by a conforming writer: interior NUL cannot occur when
// the length derives from the NUL-terminated form (and accepting one hands C interop
// two disagreeing lengths for the same payload), and well-formed UTF-8/UTF-16 is the
// writer's obligation. The write path is untouched: writes are trusted, per doctrine.
//
// Red-then-green: each stream below was shown ACCEPTED by the reader with its check
// temporarily inverted or removed (streams (a), (b) and (d) predate their checks and
// were accepted by the unmodified reader; stream (c) was accepted with the surrogate
// arm of the group check commented out), so each check is what refuses its stream.
func TestReaderContentValidation(t *testing.T) {
	// narrow doctored streams: length prefix + align + raw payload bytes, the exact
	// wire shape of serialize_string against bufferSize 64
	narrowStream := func(payload []byte) []byte {
		buffer := make([]byte, 72)
		writeStream := NewWriteStream(buffer)
		length := int32(len(payload))
		writeStream.SerializeInt(&length, 0, 63)
		writeStream.SerializeAlign()
		writeStream.SerializeBytes(payload)
		writeStream.Flush()
		return writeStream.Data()
	}
	// wide doctored streams: length prefix + one 32 bit group per unit, no align
	wideStream := func(groups []uint32) []byte {
		buffer := make([]byte, 72)
		writeStream := NewWriteStream(buffer)
		length := int32(len(groups))
		writeStream.SerializeInt(&length, 0, 63)
		for _, group := range groups {
			g := group
			writeStream.SerializeBits(&g, 32)
		}
		writeStream.Flush()
		return writeStream.Data()
	}

	refusals := []struct {
		name string
		data []byte
		read func(s *ReadStream, value *string) error
	}{
		{"(a) string: invalid UTF-8", narrowStream([]byte{'h', 0xFF, 'i'}),
			func(s *ReadStream, value *string) error { return s.SerializeString(value, 64) }},
		// 0x00 is a valid UTF-8 byte, so this stream isolates the NUL check from (a)
		{"(b) string: interior NUL", narrowStream([]byte{'h', 0x00, 'i'}),
			func(s *ReadStream, value *string) error { return s.SerializeString(value, 64) }},
		{"(c) wstring: unpaired surrogate", wideStream([]uint32{'A', 0xD800, 'B'}),
			func(s *ReadStream, value *string) error { return s.SerializeWideString(value, 64) }},
		// 0x0000 is a valid UTF-16 code unit and no surrogate, isolating this from (c)
		{"(d) wstring: interior NUL", wideStream([]uint32{'A', 0x0000, 'B'}),
			func(s *ReadStream, value *string) error { return s.SerializeWideString(value, 64) }},
	}
	for _, refusal := range refusals {
		readStream := NewReadStream(refusal.data)
		value := "unmodified"
		if err := refusal.read(readStream, &value); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("%s: expected ErrValueOutOfRange, got %v", refusal.name, err)
		}
		if value != "unmodified" {
			t.Fatalf("%s: value must be left unmodified on refusal, got %q", refusal.name, value)
		}
		// the refusal is terminal: the error latches and later reads are no-ops
		if err := readStream.Err(); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("%s: refusal must latch on the stream, got %v", refusal.name, err)
		}
		var next uint32
		if err := readStream.SerializeBits(&next, 8); !errors.Is(err, ErrValueOutOfRange) {
			t.Fatalf("%s: reads after refusal must return the latched error, got %v", refusal.name, err)
		}
	}

	// control: conforming payloads still round trip through the same call sites —
	// multibyte UTF-8 on the narrow path, a surrogate pair on the wide path
	buffer := make([]byte, 72)
	writeStream := NewWriteStream(buffer)
	narrow := "héllo"
	wide := "a\U0001F600"
	if err := writeStream.SerializeString(&narrow, 64); err != nil {
		t.Fatal(err)
	}
	if err := writeStream.SerializeWideString(&wide, 64); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()
	readStream := NewReadStream(writeStream.Data())
	var readNarrow, readWide string
	if err := readStream.SerializeString(&readNarrow, 64); err != nil {
		t.Fatal(err)
	}
	if err := readStream.SerializeWideString(&readWide, 64); err != nil {
		t.Fatal(err)
	}
	if readNarrow != narrow || readWide != wide {
		t.Fatalf("control round trip changed: %q %q", readNarrow, readWide)
	}
}

func TestMeasureStream(t *testing.T) {
	// measuring an object must never underestimate the bits required to write it
	context := &testContext{min: -10, max: +10}

	writeStream := NewWriteStream(make([]byte, 1024))
	writeStream.SetContext(context)
	writeObject := &testObject{}
	writeObject.init()
	if err := writeObject.Serialize(writeStream); err != nil {
		t.Fatal(err)
	}

	measureStream := NewMeasureStream()
	measureStream.SetContext(context)
	measureObject := &testObject{}
	measureObject.init()
	if err := measureObject.Serialize(measureStream); err != nil {
		t.Fatal(err)
	}

	if measureStream.BitsProcessed() < writeStream.BitsProcessed() {
		t.Fatalf("measure underestimated: measured %d bits, wrote %d bits",
			measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}

	// without aligns the measurement is exact
	measureStream.Reset()
	writeStream.Reset(make([]byte, 1024))

	for _, stream := range []Stream{measureStream, writeStream} {
		v := uint32(123)
		stream.SerializeBits(&v, 23)
		i := int32(-55)
		stream.SerializeInt(&i, -100, +100)
		u := uint64(0x123456789ABCDEF0)
		stream.SerializeUint64(&u)
		f := float32(1.5)
		stream.SerializeCompressedFloat32(&f, 0, 10, 0.01)
		relative := int32(105)
		stream.SerializeIntRelative(100, &relative)
		if err := stream.Err(); err != nil {
			t.Fatal(err)
		}
	}

	if measureStream.BitsProcessed() != writeStream.BitsProcessed() {
		t.Fatalf("expected exact measurement: measured %d bits, wrote %d bits",
			measureStream.BitsProcessed(), writeStream.BitsProcessed())
	}
}

func TestContinue(t *testing.T) {
	// round trip a variable length sequence with a continuation bit per element
	buffer := make([]byte, 64)
	items := []uint32{10, 20, 30, 40, 50}

	writeStream := NewWriteStream(buffer)
	{
		i := 0
		hasNext := len(items) > 0
		for Continue(writeStream, &hasNext) {
			writeStream.SerializeBits(&items[i], 8)
			i++
			hasNext = i < len(items)
		}
	}
	if err := writeStream.Err(); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()

	readStream := NewReadStream(writeStream.Data())
	var read []uint32
	hasNext := true
	for Continue(readStream, &hasNext) {
		var item uint32
		readStream.SerializeBits(&item, 8)
		read = append(read, item)
	}
	if err := readStream.Err(); err != nil {
		t.Fatal(err)
	}
	if len(read) != len(items) {
		t.Fatalf("expected %d items, got %d", len(items), len(read))
	}
	for i := range items {
		if read[i] != items[i] {
			t.Fatalf("item %d: expected %d, got %d", i, items[i], read[i])
		}
	}
}

func TestUntil(t *testing.T) {
	// round trip a variable length sequence terminated by a done bit: a false bit
	// before each element and a true bit at the end
	buffer := make([]byte, 64)
	items := []uint32{10, 20, 30, 40, 50}

	writeStream := NewWriteStream(buffer)
	{
		i := 0
		done := len(items) == 0
		for Until(writeStream, &done) {
			writeStream.SerializeBits(&items[i], 8)
			i++
			done = i == len(items)
		}
	}
	if err := writeStream.Err(); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()

	// one sentinel bit per element plus the terminating bit
	expectedBits := int64(len(items))*9 + 1
	if writeStream.BitsProcessed() != expectedBits {
		t.Fatalf("expected %d bits, got %d", expectedBits, writeStream.BitsProcessed())
	}

	readStream := NewReadStream(writeStream.Data())
	var read []uint32
	done := false
	for Until(readStream, &done) {
		var item uint32
		readStream.SerializeBits(&item, 8)
		read = append(read, item)
	}
	if err := readStream.Err(); err != nil {
		t.Fatal(err)
	}
	if len(read) != len(items) {
		t.Fatalf("expected %d items, got %d", len(items), len(read))
	}
	for i := range items {
		if read[i] != items[i] {
			t.Fatalf("item %d: expected %d, got %d", i, items[i], read[i])
		}
	}

	// an empty sequence is a single terminating bit
	writeStream.Reset(buffer)
	done = true
	for Until(writeStream, &done) {
		t.Fatal("loop body must not run for an empty sequence")
	}
	if writeStream.BitsProcessed() != 1 {
		t.Fatalf("expected 1 bit for an empty sequence, got %d", writeStream.BitsProcessed())
	}
}

func TestSentinelLoopTermination(t *testing.T) {
	// a malicious packet of 0xFF bytes claims "another element follows" forever.
	// because every successful serialize call consumes at least one bit, a Continue
	// loop is bounded by the bit count of the packet and terminates with ErrOverflow.
	{
		malicious := bytes.Repeat([]byte{0xFF}, 32)

		stream := NewReadStream(malicious)
		iterations := 0
		hasNext := true
		for Continue(stream, &hasNext) {
			var item uint32
			stream.SerializeBits(&item, 8)
			iterations++
		}
		if !errors.Is(stream.Err(), ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", stream.Err())
		}
		if iterations > len(malicious)*8 {
			t.Fatalf("loop ran %d iterations, more than the bit count of the packet", iterations)
		}
	}

	// a packet truncated in the middle of a sequence also terminates with an error
	{
		buffer := make([]byte, 64)
		writeStream := NewWriteStream(buffer)
		items := []uint32{1, 2, 3, 4, 5}
		i := 0
		hasNext := len(items) > 0
		for Continue(writeStream, &hasNext) {
			writeStream.SerializeBits(&items[i], 32)
			i++
			hasNext = i < len(items)
		}
		writeStream.Flush()

		truncated := writeStream.Data()[:2]

		stream := NewReadStream(truncated)
		hasNext = true
		for Continue(stream, &hasNext) {
			var item uint32
			stream.SerializeBits(&item, 32)
		}
		if !errors.Is(stream.Err(), ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", stream.Err())
		}
	}

	// a malicious packet of zero bytes claims "not done" forever. an Until loop is
	// bounded by the bit count of the packet and terminates with ErrOverflow.
	{
		malicious := make([]byte, 32)

		stream := NewReadStream(malicious)
		iterations := 0
		done := false
		for Until(stream, &done) {
			var item uint32
			stream.SerializeBits(&item, 8)
			iterations++
		}
		if !errors.Is(stream.Err(), ErrOverflow) {
			t.Fatalf("expected ErrOverflow, got %v", stream.Err())
		}
		if iterations > len(malicious)*8 {
			t.Fatalf("loop ran %d iterations, more than the bit count of the packet", iterations)
		}
	}

	// the unguarded patterns documented as WRONG in docs/reading_untrusted_data.md
	// really do spin, in both polarities: after the first failure the no-op reads
	// never update the sentinel.
	// capped here to keep the demonstration finite. if a future change makes these
	// loops terminate on their own, this test will fail and the documentation should
	// be revisited.
	{
		stream := NewReadStream([]byte{})
		hasNext := true
		spins := 0
		for hasNext && spins < 10000 {
			stream.SerializeBool(&hasNext) // no-op after the first failure
			spins++
		}
		if spins != 10000 {
			t.Fatal("expected the unguarded continuation bit loop to spin until the cap")
		}
	}
	{
		stream := NewReadStream([]byte{})
		done := false
		spins := 0
		for !done && spins < 10000 {
			stream.SerializeBool(&done) // no-op after the first failure
			spins++
		}
		if spins != 10000 {
			t.Fatal("expected the unguarded termination bit loop to spin until the cap")
		}
	}
}

type failingObject struct{}

var errCustomValidation = errors.New("custom validation failed")

func (o *failingObject) Serialize(stream Stream) error {
	return errCustomValidation
}

func TestSerializeObjectErrorPropagation(t *testing.T) {
	stream := NewWriteStream(make([]byte, 8))
	if err := stream.SerializeObject(&failingObject{}); !errors.Is(err, errCustomValidation) {
		t.Fatalf("expected custom error, got %v", err)
	}
	if !errors.Is(stream.Err(), errCustomValidation) {
		t.Fatal("expected the custom error to latch on the stream")
	}
}

func TestStreamReset(t *testing.T) {
	// streams are reusable without allocation
	buffer := make([]byte, 16)

	writeStream := NewWriteStream(buffer)
	v := uint32(0xABCD)
	writeStream.SerializeBits(&v, 16)
	writeStream.Flush()

	writeStream.Reset(buffer)
	v = 0x1234
	if err := writeStream.SerializeBits(&v, 16); err != nil {
		t.Fatal(err)
	}
	writeStream.Flush()

	readStream := NewReadStream(writeStream.Data())
	var value uint32
	if err := readStream.SerializeBits(&value, 16); err != nil {
		t.Fatal(err)
	}
	if value != 0x1234 {
		t.Fatalf("expected 0x1234, got %#x", value)
	}

	// reset clears a latched error
	readStream.Reset(buffer[:0])
	if err := readStream.SerializeBits(&value, 1); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected ErrOverflow on empty buffer")
	}
	readStream.Reset(writeStream.Data())
	if readStream.Err() != nil {
		t.Fatal("expected Reset to clear the error")
	}
	if err := readStream.SerializeBits(&value, 16); err != nil || value != 0x1234 {
		t.Fatalf("expected 0x1234 after reset, got %#x (err %v)", value, err)
	}
}
