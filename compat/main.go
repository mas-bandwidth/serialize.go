// Command compat is the Go half of the cross language wire compatibility harness.
// Its C++ twin is cpp/compat.cpp; the cppcompat CI job runs both against each other on
// every push and PR: each side writes its stream to a file, the two files must be
// byte identical, and each side must read the other's file back to the exact values.
//
// The value sequence starts as the golden wire format sequence from serialize_test.go
// (pinned to bytes copied verbatim from the C++ test suite) and extends it with the
// 64 bit paths the golden test does not cover. Any change here must be mirrored in
// cpp/compat.cpp, and never changes the wire format: see CLAUDE.md invariant 1.
package main

import (
	"fmt"
	"math"
	"os"

	"github.com/mas-bandwidth/serialize.go"
)

type compatData struct {
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
	bits33               uint64
	int64Full            int64
	int64Range           int64
}

func initCompatData() compatData {
	return compatData{
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
		wstr:                 "мир", // cyrillic, BMP only: representable on 2 byte wchar_t platforms too
		bits33:               0x1DEADBEEF,
		int64Full:            -123456789012345,
		int64Range:           4123456789,
	}
}

func serializeCompat(stream serialize.Stream, data *compatData) error {
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
	stream.SerializeBits64(&data.bits33, 33)
	stream.SerializeInt64(&data.int64Full, math.MinInt64, math.MaxInt64)
	stream.SerializeInt64(&data.int64Range, -5000000000, +5000000000)
	return stream.Err()
}

func write(path string) error {
	buffer := make([]byte, 256)
	stream := serialize.NewWriteStream(buffer)
	data := initCompatData()
	if err := serializeCompat(stream, &data); err != nil {
		return fmt.Errorf("write stream failed: %w", err)
	}
	stream.Flush()
	return os.WriteFile(path, stream.Data(), 0o644)
}

func read(path string) error {
	packet, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	stream := serialize.NewReadStream(packet)
	var data compatData
	if err := serializeCompat(stream, &data); err != nil {
		return fmt.Errorf("read stream failed: %w", err)
	}
	if expected := initCompatData(); data != expected {
		return fmt.Errorf("decoded values do not match:\nexpected %+v\ngot      %+v", expected, data)
	}
	return nil
}

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "write" && os.Args[1] != "read") {
		fmt.Fprintln(os.Stderr, "usage: compat write|read <file>")
		os.Exit(2)
	}
	var err error
	if os.Args[1] == "write" {
		err = write(os.Args[2])
	} else {
		err = read(os.Args[2])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat go %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	fmt.Printf("compat go %s ok\n", os.Args[1])
}
