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

// writePacketDirect serializes the packet against the concrete stream type, with no
// interface dispatch. This is the fastest way to use the library, at the cost of
// separate read and write functions.
func writePacketDirect(s *WriteStream, p *benchPacket) error {
	s.SerializeUint64(&p.sequence)
	for i := range p.position {
		s.SerializeCompressedFloat32(&p.position[i], -1024, 1024, 0.01)
	}
	for i := range p.orientation {
		s.SerializeCompressedFloat32(&p.orientation[i], -1, 1, 0.0001)
	}
	s.SerializeInt(&p.health, 0, 1000)
	s.SerializeBits(&p.weapon, 4)
	for i := range p.ammo {
		s.SerializeInt(&p.ammo[i], 0, 200)
	}
	s.SerializeBool(&p.firing)
	s.SerializeInt(&p.events, 0, 16)
	for i := int32(0); i < p.events; i++ {
		s.SerializeBits(&p.eventIDs[i], 32)
	}
	s.SerializeBytes(p.payload[:])
	return s.Err()
}

func readPacketDirect(s *ReadStream, p *benchPacket) error {
	s.SerializeUint64(&p.sequence)
	for i := range p.position {
		s.SerializeCompressedFloat32(&p.position[i], -1024, 1024, 0.01)
	}
	for i := range p.orientation {
		s.SerializeCompressedFloat32(&p.orientation[i], -1, 1, 0.0001)
	}
	s.SerializeInt(&p.health, 0, 1000)
	s.SerializeBits(&p.weapon, 4)
	for i := range p.ammo {
		s.SerializeInt(&p.ammo[i], 0, 200)
	}
	s.SerializeBool(&p.firing)
	s.SerializeInt(&p.events, 0, 16)
	for i := int32(0); i < p.events; i++ {
		s.SerializeBits(&p.eventIDs[i], 32)
	}
	s.SerializeBytes(p.payload[:])
	return s.Err()
}

func BenchmarkWriteStreamPacketDirect(b *testing.B) {
	buffer := make([]byte, 1024)
	packet := &benchPacket{}
	packet.init()

	stream := NewWriteStream(buffer)
	if err := writePacketDirect(stream, packet); err != nil {
		b.Fatal(err)
	}
	stream.Flush()

	b.SetBytes(stream.BytesProcessed())
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := writePacketDirect(stream, packet); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

func BenchmarkReadStreamPacketDirect(b *testing.B) {
	buffer := make([]byte, 1024)
	packet := &benchPacket{}
	packet.init()

	writeStream := NewWriteStream(buffer)
	if err := writePacketDirect(writeStream, packet); err != nil {
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
		if err := readPacketDirect(stream, readPacket); err != nil {
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

// benchFixed128Data carries one of each new operation's value: a Q48.16 fixed point
// value, a wide Q112.16 fixed point value, a raw uint128 and a ranged int128. Every
// new serialization path must stay zero allocation, like the rest — the values live
// in a struct so the pointers passed through the Stream interface do not force the
// locals to escape on every call.
type benchFixed128Data struct {
	fixed64   int64
	wide      Int128
	value128  Uint128
	ranged128 Int128
}

func (d *benchFixed128Data) init() {
	d.fixed64 = 12345*65536 + 32768
	d.wide = Int128From64(-(98765432109*65536 + 4321))
	d.value128 = Uint128{Lo: 0xFEDCBA9876543210, Hi: 0x0123456789ABCDEF}
	d.ranged128 = Int128From64(-42)
}

func (d *benchFixed128Data) Serialize(stream Stream) error {
	stream.SerializeFixed64(&d.fixed64, 48, 16, -100000000000, +100000000000)
	stream.SerializeFixed128(&d.wide, 112, 16, -1152921504606846976, +1152921504606846976)
	stream.SerializeUint128(&d.value128)
	stream.SerializeInt128(&d.ranged128, Int128From64(1).Lsh(100).Neg(), Int128From64(1).Lsh(100))
	return stream.Err()
}

func BenchmarkWriteStreamFixed128(b *testing.B) {
	buffer := make([]byte, 64)
	stream := NewWriteStream(buffer)
	data := &benchFixed128Data{}
	data.init()

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := data.Serialize(stream); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

func BenchmarkReadStreamFixed128(b *testing.B) {
	buffer := make([]byte, 64)
	writeStream := NewWriteStream(buffer)
	written := &benchFixed128Data{}
	written.init()
	if err := written.Serialize(writeStream); err != nil {
		b.Fatal(err)
	}
	writeStream.Flush()
	packet := writeStream.Data()

	stream := NewReadStream(packet)
	data := &benchFixed128Data{}

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(packet)
		if err := data.Serialize(stream); err != nil {
			b.Fatal(err)
		}
	}
}

// benchWriteString writes a single serialized string and returns the wire bytes.
func benchWriteString(b *testing.B, v string) []byte {
	b.Helper()
	buffer := make([]byte, 64)
	stream := NewWriteStream(buffer)
	if err := stream.SerializeString(&v, 64); err != nil {
		b.Fatal(err)
	}
	stream.Flush()
	return stream.Data()
}

// BenchmarkReadStreamStringStable reads the same chat sized string into a reused value
// every iteration — the steady state of re-reading stable strings (names, channels,
// repeated messages). This path is allocation free.
func BenchmarkReadStreamStringStable(b *testing.B) {
	const message = "did you see that ludicrous display"
	data := benchWriteString(b, message)

	stream := NewReadStream(data)
	var value string

	b.SetBytes(int64(len(message)))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(data)
		if err := stream.SerializeString(&value, 64); err != nil {
			b.Fatal(err)
		}
	}
	benchSink += uint32(len(value))
}

// BenchmarkReadStreamStringChanging alternates between two different chat sized strings
// into a reused value — every read replaces the content, so every read pays the one
// string allocation plus the failed comparison.
func BenchmarkReadStreamStringChanging(b *testing.B) {
	const messageA = "did you see that ludicrous display"
	const messageB = "what was Wenger thinking sending on"
	wire := [2][]byte{benchWriteString(b, messageA), benchWriteString(b, messageB)}

	stream := NewReadStream(wire[0])
	var value string

	b.SetBytes(int64(len(messageA)))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(wire[n&1])
		if err := stream.SerializeString(&value, 64); err != nil {
			b.Fatal(err)
		}
	}
	benchSink += uint32(len(value))
}

// BenchmarkWriteStreamString writes the same chat sized string every iteration — the
// write half of the string rows above, which existed only for the read path until the
// measure-first ruling (Glenn, 2026-08-17: "You can't improve what you don't measure")
// asked for string and wstring rows across the family, before or with any string fix.
// Wire: 6 bit length prefix + 2 align bits + 34 payload bytes = 35 bytes. This path is
// allocation free.
func BenchmarkWriteStreamString(b *testing.B) {
	const message = "did you see that ludicrous display"
	value := message
	buffer := make([]byte, 64)
	stream := NewWriteStream(buffer)

	b.SetBytes(int64(len(message)))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := stream.SerializeString(&value, 64); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

// wideMessage is the wstring rows' payload: the chat sized message plus one astral
// code point, so the surrogate pair split/recombine path is exercised alongside the
// BMP fast path (the family conformance pin uses U+1F600 for the same reason).
// 37 UTF-16 code units; wire = 6 bit length prefix + 37 x 32 bits = 149 bytes —
// SetBytes uses the UTF-8 payload length like the narrow string rows, so MB/s reads
// as content throughput, not wire throughput.
const wideMessage = "did you see that ludicrous display \U0001F600"

// benchWriteWideString writes a single serialized wide string and returns the wire bytes.
func benchWriteWideString(b *testing.B, v string) []byte {
	b.Helper()
	buffer := make([]byte, 256)
	stream := NewWriteStream(buffer)
	if err := stream.SerializeWideString(&v, 64); err != nil {
		b.Fatal(err)
	}
	stream.Flush()
	return stream.Data()
}

// BenchmarkWriteStreamWideString writes the wide message every iteration. The write
// path iterates the string's runes and emits one 32 bit group per UTF-16 code unit —
// allocation free.
func BenchmarkWriteStreamWideString(b *testing.B) {
	value := wideMessage
	buffer := make([]byte, 256)
	stream := NewWriteStream(buffer)

	b.SetBytes(int64(len(wideMessage)))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := stream.SerializeWideString(&value, 64); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

// BenchmarkReadStreamWideString reads the wide message into a reused value every
// iteration. Unlike the narrow read path, which reuses the value when the bytes are
// equal, the wide read path allocates every read today — a []rune scratch plus the
// final string — and this row is what makes that cost a measured number rather than
// a guess (the measure-first ruling: rows land before or with any wstring fix).
func BenchmarkReadStreamWideString(b *testing.B) {
	data := benchWriteWideString(b, wideMessage)

	stream := NewReadStream(data)
	var value string

	b.SetBytes(int64(len(wideMessage)))
	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(data)
		if err := stream.SerializeWideString(&value, 64); err != nil {
			b.Fatal(err)
		}
	}
	benchSink += uint32(len(value))
}

// The compressed float benchmarks isolate one field so the derive-per-call and
// precomputed entry points can be compared directly: the difference IS the per-call
// derivation (a divide, a clamp, a ceil and a BitsRequired). This is the measurement
// schema issue mas-bandwidth/schema#82 names for emitter adoption — the #79 inline
// fold versus a call into the runtime is a measurement per backend, and these numbers
// are the runtime side of it. The declaration is [-100,100] at resolution 0.01: the
// non-zero-min conformance declaration, whose constants are 20000, 15, 200.0.

func BenchmarkWriteStreamCompressedFloat(b *testing.B) {
	buffer := make([]byte, 8)
	stream := NewWriteStream(buffer)
	value := float32(37.5)

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := stream.SerializeCompressedFloat32(&value, -100, 100, 0.01); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

func BenchmarkWriteStreamCompressedFloatPrecomputed(b *testing.B) {
	buffer := make([]byte, 8)
	stream := NewWriteStream(buffer)
	value := float32(37.5)

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(buffer)
		if err := stream.SerializeCompressedFloat32Precomputed(&value, 20000, 15, 200.0, -100.0); err != nil {
			b.Fatal(err)
		}
		stream.Flush()
	}
}

func BenchmarkReadStreamCompressedFloat(b *testing.B) {
	buffer := make([]byte, 8)
	writeStream := NewWriteStream(buffer)
	written := float32(37.5)
	if err := writeStream.SerializeCompressedFloat32(&written, -100, 100, 0.01); err != nil {
		b.Fatal(err)
	}
	writeStream.Flush()
	packet := writeStream.Data()

	stream := NewReadStream(packet)
	var value float32

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(packet)
		if err := stream.SerializeCompressedFloat32(&value, -100, 100, 0.01); err != nil {
			b.Fatal(err)
		}
	}
	benchSink += uint32(value)
}

func BenchmarkReadStreamCompressedFloatPrecomputed(b *testing.B) {
	buffer := make([]byte, 8)
	writeStream := NewWriteStream(buffer)
	written := float32(37.5)
	if err := writeStream.SerializeCompressedFloat32Precomputed(&written, 20000, 15, 200.0, -100.0); err != nil {
		b.Fatal(err)
	}
	writeStream.Flush()
	packet := writeStream.Data()

	stream := NewReadStream(packet)
	var value float32

	b.ReportAllocs()
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		stream.Reset(packet)
		if err := stream.SerializeCompressedFloat32Precomputed(&value, 20000, 15, 200.0, -100.0); err != nil {
			b.Fatal(err)
		}
	}
	benchSink += uint32(value)
}

// benchRealPacket is the local family's realistic snapshot row, the shape
// BENCH-STANDARD §1.7 demands after the corpus composition ruling (Glenn,
// 2026-08-16, verbatim): "we are profiling the REAL use case, which is, lots
// and lots of individual serialize statements" / "maybe a real packet is like
// 1000-2000 individually serialized bits" / "and very rarely is it wstring,
// string or byte array".
//
// benchPacket above is 48.1% bulk by bits — its 64 byte payload is 512 of the
// 1064 wire bits, so its MB/s is half memcpy. Per §1.7 rule 3 it is never
// rebalanced in place (that would silently re-price every historical
// comparison); this row joins beside it under a new name, inside the ruling's
// latitude: hundreds of individually serialized small fields of assorted
// scalar kinds, with one rare, small bulk payload.
//
// Bit arithmetic (§1.7 rule 4 — the bulk share is stated where the shape is
// defined, and the write benchmark asserts the total so this comment cannot
// go stale):
//
//	header:  64 (sequence) + 64 (serverTime) + 32 (ackBits)
//	         + 11 (simRate Q8.8 in [0,4]: raw range 0..1024) + 4 (numEntities in [0,8])   = 175
//	entity:  12 (id) + 3x18 (position) + 4x15 (orientation) + 3x10 (velocity)
//	         + 10 (health) + 4 (weapon) + 3 (state in [0,5]) + 5 (flags) + 1 + 1 (bools)  = 180
//	8 entities                                                                            = 1440
//	individually serialized                                                    175 + 1440 = 1615
//	align to byte boundary                                                                = 1
//	bulk payload: 16 bytes                                                                = 128
//	total                                                                     1744 bits   = 218 bytes
//
// Bulk share by bits: 128/1744 = 7.3% — under the 15% line, against
// benchPacket's 48.1%. Individually serialized bits: 1615, inside the ruling's
// 1000-2000 band.
type benchRealPacket struct {
	sequence    uint64
	serverTime  float64
	ackBits     uint32
	simRate     int64 // Q8.8 fixed point, [0,4] whole units
	numEntities int32
	entities    [8]benchEntity
	payload     [16]byte
}

type benchEntity struct {
	id          uint32
	position    [3]float32
	orientation [4]float32
	velocity    [3]float32
	health      int32
	weapon      uint32
	state       int32
	flags       uint32
	moving      bool
	firing      bool
}

func (p *benchRealPacket) init() {
	p.sequence = 0xFEDCBA9876543210
	p.serverTime = 1234.5678
	p.ackBits = 0xAAAA5555
	p.simRate = 1<<8 + 128 // 1.5 in Q8.8
	p.numEntities = 8
	for i := range p.entities {
		e := &p.entities[i]
		e.id = uint32(100 + i*7)
		e.position = [3]float32{float32(i)*10.5 - 40, float32(i)*-3.25 + 12, float32(i) * 1.75}
		e.orientation = [4]float32{0.1, -0.2, 0.3, 0.9}
		e.velocity = [3]float32{float32(i) - 4, 2.5, float32(i) * 0.5}
		e.health = 100 + int32(i)*100
		e.weapon = uint32(i) & 15
		e.state = int32(i) % 6
		e.flags = uint32(i*5) & 31
		e.moving = i%2 == 0
		e.firing = i%3 == 0
	}
	for i := range p.payload {
		p.payload[i] = byte(i * 59)
	}
}

func (p *benchRealPacket) Serialize(stream Stream) error {
	stream.SerializeUint64(&p.sequence)
	stream.SerializeFloat64(&p.serverTime)
	stream.SerializeBits(&p.ackBits, 32)
	stream.SerializeFixed64(&p.simRate, 8, 8, 0, 4)
	stream.SerializeInt(&p.numEntities, 0, 8)
	for i := int32(0); i < p.numEntities; i++ {
		e := &p.entities[i]
		stream.SerializeBits(&e.id, 12)
		for j := range e.position {
			stream.SerializeCompressedFloat32(&e.position[j], -1024, 1024, 0.01)
		}
		for j := range e.orientation {
			stream.SerializeCompressedFloat32(&e.orientation[j], -1, 1, 0.0001)
		}
		for j := range e.velocity {
			stream.SerializeCompressedFloat32(&e.velocity[j], -64, 64, 0.25)
		}
		stream.SerializeInt(&e.health, 0, 1000)
		stream.SerializeBits(&e.weapon, 4)
		stream.SerializeInt(&e.state, 0, 5)
		stream.SerializeBits(&e.flags, 5)
		stream.SerializeBool(&e.moving)
		stream.SerializeBool(&e.firing)
	}
	stream.SerializeBytes(p.payload[:])
	return stream.Err()
}

func BenchmarkWriteStreamRealPacket(b *testing.B) {
	buffer := make([]byte, 256)
	packet := &benchRealPacket{}
	packet.init()

	stream := NewWriteStream(buffer)
	if err := packet.Serialize(stream); err != nil {
		b.Fatal(err)
	}
	stream.Flush()
	if got := stream.BytesProcessed(); got != 218 {
		b.Fatalf("real packet is %d wire bytes, want 218 — the definition comment's arithmetic is stale", got)
	}

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

func BenchmarkReadStreamRealPacket(b *testing.B) {
	buffer := make([]byte, 256)
	packet := &benchRealPacket{}
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

	readPacket := &benchRealPacket{}
	for n := 0; n < b.N; n++ {
		stream.Reset(data)
		if err := readPacket.Serialize(stream); err != nil {
			b.Fatal(err)
		}
	}
}
