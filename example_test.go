package serialize_test

import (
	"fmt"

	serialize "github.com/mas-bandwidth/goserialize"
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
