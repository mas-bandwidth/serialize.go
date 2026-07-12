package serialize_test

import (
	"fmt"

	"github.com/mas-bandwidth/serialize.go"
)

// Use the bitpacker directly when you want full control over each bit.
func ExampleBitWriter() {
	buffer := make([]byte, 256)

	writer := serialize.NewBitWriter(buffer)
	writer.WriteBits(0, 1)
	writer.WriteBits(1, 1)
	writer.WriteBits(10, 8)
	writer.WriteBits(255, 8)
	writer.WriteBits(1000, 10)
	writer.WriteBits(50000, 16)
	writer.WriteBits(9999999, 32)
	writer.FlushBits()

	fmt.Printf("wrote %d bytes\n", writer.BytesWritten())

	reader := serialize.NewBitReader(writer.Data())
	fmt.Println(reader.ReadBits(1))
	fmt.Println(reader.ReadBits(1))
	fmt.Println(reader.ReadBits(8))
	fmt.Println(reader.ReadBits(8))
	fmt.Println(reader.ReadBits(10))
	fmt.Println(reader.ReadBits(16))
	fmt.Println(reader.ReadBits(32))

	// Output:
	// wrote 10 bytes
	// 0
	// 1
	// 10
	// 255
	// 1000
	// 50000
	// 9999999
}

type Vector struct {
	X, Y, Z float32
}

func (v *Vector) Serialize(stream serialize.Stream) error {
	stream.SerializeFloat32(&v.X)
	stream.SerializeFloat32(&v.Y)
	stream.SerializeFloat32(&v.Z)
	return stream.Err()
}

type RigidBody struct {
	Position       Vector
	LinearVelocity Vector
	AtRest         bool
}

// Serialize is a unified serialize function: the same code writes, reads and measures.
func (b *RigidBody) Serialize(stream serialize.Stream) error {
	stream.SerializeObject(&b.Position)
	stream.SerializeBool(&b.AtRest)
	if !b.AtRest {
		stream.SerializeObject(&b.LinearVelocity)
	} else if stream.IsReading() {
		b.LinearVelocity = Vector{}
	}
	return stream.Err()
}

// An Address is a discriminated union: the type field decides which fields follow on
// the wire. Ported from example.cpp in the C++ serialize library.
const (
	addressNone = iota
	addressIPv4
	addressIPv6
	numAddressTypes
)

type Address struct {
	Type int32
	IPv4 [4]uint8
	IPv6 [8]uint16
	Port uint16
}

func (a *Address) Serialize(stream serialize.Stream) error {
	// Type controls the branches below, so its error is checked before use
	if err := stream.SerializeInt(&a.Type, addressNone, numAddressTypes-1); err != nil {
		return err
	}
	switch a.Type {
	case addressIPv4:
		for i := range a.IPv4 {
			stream.SerializeUint8(&a.IPv4[i])
		}
	case addressIPv6:
		for i := range a.IPv6 {
			stream.SerializeUint16(&a.IPv6[i])
		}
	}
	stream.SerializeUint16(&a.Port)
	return stream.Err()
}

// Serialize a discriminated union: a type field followed by fields that depend on it.
func Example_variantSerialization() {
	buffer := make([]byte, 64)

	address := Address{Type: addressIPv4, IPv4: [4]uint8{127, 0, 0, 1}, Port: 40000}

	writeStream := serialize.NewWriteStream(buffer)
	if err := address.Serialize(writeStream); err != nil {
		panic(err)
	}
	writeStream.Flush()

	var received Address
	readStream := serialize.NewReadStream(writeStream.Data())
	if err := received.Serialize(readStream); err != nil {
		panic(err)
	}

	fmt.Printf("%d.%d.%d.%d:%d\n", received.IPv4[0], received.IPv4[1], received.IPv4[2], received.IPv4[3], received.Port)

	// Output:
	// 127.0.0.1:40000
}

// Measure how many bytes an object needs before allocating a buffer and writing it.
func ExampleMeasureStream() {
	body := RigidBody{
		Position:       Vector{X: 10, Y: 20, Z: 30},
		LinearVelocity: Vector{X: 1, Y: 2, Z: 3},
	}

	measureStream := serialize.NewMeasureStream()
	if err := body.Serialize(measureStream); err != nil {
		panic(err)
	}

	// round up to a multiple of 8 bytes, as the write stream requires
	bufferSize := int(measureStream.BytesProcessed()+7) / 8 * 8

	writeStream := serialize.NewWriteStream(make([]byte, bufferSize))
	if err := body.Serialize(writeStream); err != nil {
		panic(err)
	}
	writeStream.Flush()

	fmt.Printf("measured %d bytes, wrote %d bytes\n", measureStream.BytesProcessed(), writeStream.BytesProcessed())

	// Output:
	// measured 25 bytes, wrote 25 bytes
}

// Serialize a variable length sequence with a continuation bit per element. Continue
// folds the stream error state into the loop condition, so the loop always terminates
// on truncated or malicious data.
func ExampleContinue() {
	buffer := make([]byte, 64)
	items := []uint32{10, 20, 30}

	writeStream := serialize.NewWriteStream(buffer)
	i := 0
	hasNext := len(items) > 0
	for serialize.Continue(writeStream, &hasNext) {
		writeStream.SerializeBits(&items[i], 8)
		i++
		hasNext = i < len(items)
	}
	writeStream.Flush()

	readStream := serialize.NewReadStream(writeStream.Data())
	var received []uint32
	hasNext = true
	for serialize.Continue(readStream, &hasNext) {
		var item uint32
		readStream.SerializeBits(&item, 8)
		received = append(received, item)
	}
	if err := readStream.Err(); err != nil {
		panic(err)
	}

	fmt.Println(received)

	// Output:
	// [10 20 30]
}

// Write one serialize function per type and use it for write, read and measure.
func Example_unifiedSerialization() {
	buffer := make([]byte, 256)

	body := RigidBody{
		Position:       Vector{X: 10, Y: 20, Z: 30},
		LinearVelocity: Vector{X: 1, Y: 2, Z: 3},
	}

	writeStream := serialize.NewWriteStream(buffer)
	if err := body.Serialize(writeStream); err != nil {
		panic(err)
	}
	writeStream.Flush()

	packet := writeStream.Data()
	fmt.Printf("wrote %d bytes\n", len(packet))

	var received RigidBody
	readStream := serialize.NewReadStream(packet)
	if err := received.Serialize(readStream); err != nil {
		panic(err)
	}

	fmt.Println(received.Position, received.AtRest)

	// Output:
	// wrote 25 bytes
	// {10 20 30} false
}
