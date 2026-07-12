package serialize

import "testing"

var benchSink uint32

func BenchmarkBitWriterWriteBits(b *testing.B) {
	buffer := make([]byte, 1<<16)
	writer := NewBitWriter(buffer)

	const numValues = 1024
	totalBits := 0
	for i := range numValues {
		totalBits += i%32 + 1
	}

	b.SetBytes(int64(totalBits / 8))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		writer.Reset(buffer)
		for i := range numValues {
			writer.WriteBits(uint32(i)*2654435761, i%32+1)
		}
		writer.FlushBits()
	}
}

func BenchmarkBitReaderReadBits(b *testing.B) {
	buffer := make([]byte, 1<<16)
	writer := NewBitWriter(buffer)

	const numValues = 1024
	totalBits := 0
	for i := range numValues {
		writer.WriteBits(uint32(i)*2654435761, i%32+1)
		totalBits += i%32 + 1
	}
	writer.FlushBits()

	reader := NewBitReader(writer.Data())

	b.SetBytes(int64(totalBits / 8))
	b.ReportAllocs()
	b.ResetTimer()

	var sum uint32
	for n := 0; n < b.N; n++ {
		reader.Reset(writer.Data())
		for i := range numValues {
			sum += reader.ReadBits(i%32 + 1)
		}
	}
	benchSink = sum
}

// benchPacket is a representative game network packet: quantized transform, velocities,
// events and a bulk payload, serialized with a single unified function.
type benchPacket struct {
	sequence    uint64
	position    [3]float32
	orientation [4]float32
	health      int32
	weapon      uint32
	ammo        [8]int32
	firing      bool
	events      int32
	eventIDs    [16]uint32
	payload     [64]byte
}

func (p *benchPacket) init() {
	p.sequence = 0x123456789ABCDEF0
	p.position = [3]float32{102.4, -55.3, 12.75}
	p.orientation = [4]float32{0.1, 0.2, 0.3, 0.9}
	p.health = 731
	p.weapon = 11
	for i := range p.ammo {
		p.ammo[i] = int32(i * 13 % 200)
	}
	p.firing = true
	p.events = 9
	for i := range p.eventIDs {
		p.eventIDs[i] = uint32(i) * 2654435761
	}
	for i := range p.payload {
		p.payload[i] = byte(i * 47)
	}
}

func (p *benchPacket) Serialize(stream Stream) error {
	stream.SerializeUint64(&p.sequence)
	for i := range p.position {
		stream.SerializeCompressedFloat32(&p.position[i], -1024, 1024, 0.01)
	}
	for i := range p.orientation {
		stream.SerializeCompressedFloat32(&p.orientation[i], -1, 1, 0.0001)
	}
	stream.SerializeInt(&p.health, 0, 1000)
	stream.SerializeBits(&p.weapon, 4)
	for i := range p.ammo {
		stream.SerializeInt(&p.ammo[i], 0, 200)
	}
	stream.SerializeBool(&p.firing)
	stream.SerializeInt(&p.events, 0, 16)
	for i := int32(0); i < p.events; i++ {
		stream.SerializeBits(&p.eventIDs[i], 32)
	}
	stream.SerializeBytes(p.payload[:])
	return stream.Err()
}

func BenchmarkWriteStreamPacket(b *testing.B) {
	buffer := make([]byte, 1024)
	packet := &benchPacket{}
	packet.init()

	stream := NewWriteStream(buffer)
	if err := packet.Serialize(stream); err != nil {
		b.Fatal(err)
	}
	stream.Flush()

	b.SetBytes(stream.BytesProcessed())
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := packet.Serialize(stream); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

func BenchmarkReadStreamPacket(b *testing.B) {
	buffer := make([]byte, 1024)
	packet := &benchPacket{}
	packet.init()

	writeStream := NewWriteStream(buffer)
	if err := packet.Serialize(writeStream); err != nil {
		b.Fatal(err)
	}
	writeStream.Flush()
	data := writeStream.Data()

	stream := NewReadStream(data)

	b.SetBytes(writeStream.BytesProcessed())
	b.ReportAllocs()
	b.ResetTimer()

	readPacket := &benchPacket{}
	for n := 0; n < b.N; n++ {
		stream.Reset(data)
		if err := readPacket.Serialize(stream); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMeasureStreamPacket(b *testing.B) {
	packet := &benchPacket{}
	packet.init()

	stream := NewMeasureStream()
	if err := packet.Serialize(stream); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(stream.BytesProcessed())
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset()
		if err := packet.Serialize(stream); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteBytes(b *testing.B) {
	const payloadSize = 1200 // typical MTU-sized packet payload
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	buffer := make([]byte, 2048)
	writer := NewBitWriter(buffer)

	b.SetBytes(payloadSize)
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		writer.Reset(buffer)
		writer.WriteBits(1, 3) // start unaligned so head/tail paths are exercised
		writer.WriteAlign()
		writer.WriteBytes(payload)
		writer.FlushBits()
	}
}

func BenchmarkReadBytes(b *testing.B) {
	const payloadSize = 1200
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	buffer := make([]byte, 2048)
	writer := NewBitWriter(buffer)
	writer.WriteBits(1, 3)
	writer.WriteAlign()
	writer.WriteBytes(payload)
	writer.FlushBits()
	data := writer.Data()

	reader := NewBitReader(data)
	output := make([]byte, payloadSize)

	b.SetBytes(payloadSize)
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		reader.Reset(data)
		benchSink += reader.ReadBits(3)
		reader.ReadAlign()
		reader.ReadBytes(output)
	}
}
