# Introduction

[![CI](https://github.com/mas-bandwidth/goserialize/actions/workflows/ci.yml/badge.svg)](https://github.com/mas-bandwidth/goserialize/actions/workflows/ci.yml)

**goserialize** is a simple bitpacking serializer for Go.

It is a pure Go port of the C++ [serialize](https://github.com/mas-bandwidth/serialize) library, with no native code. The two libraries produce bit-for-bit identical output, so streams written by one language can be read by the other. This is pinned down by a golden wire format test whose bytes are copied verbatim from the C++ test suite.

It has the following features:

* Serialize a bool with only one bit
* Serialize any integer value from [1,64] bits writing only that number of bits to the buffer
* Serialize signed integer values with [min,max] writing only the required bits to the buffer
* Serialize floats, doubles, compressed floats, strings, byte arrays, and integers relative to another integer
* Alignment support so you can align your bitstream to a byte boundary whenever you want
* Unified serialization through the `Stream` interface, so you can write one function that handles read, write and measure
* Zero allocations on every serialization path
* Every read is bounds checked and range validated, so maliciously crafted packets fail with errors instead of panicking

# Usage

```
go get github.com/mas-bandwidth/goserialize
```

The package name is `serialize`:

```go
import serialize "github.com/mas-bandwidth/goserialize"
```

You can use the bitpacker directly:

```go
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

reader := serialize.NewBitReader(writer.Data())

a := reader.ReadBits(1)
b := reader.ReadBits(1)
c := reader.ReadBits(8)
d := reader.ReadBits(8)
e := reader.ReadBits(10)
f := reader.ReadBits(16)
g := reader.ReadBits(32)
```

Or you can write serialize methods for your types:

```go
type Vector struct {
    X, Y, Z float32
}

func (v *Vector) Serialize(stream serialize.Stream) error {
    stream.SerializeFloat32(&v.X)
    stream.SerializeFloat32(&v.Y)
    stream.SerializeFloat32(&v.Z)
    return stream.Error()
}

type RigidBody struct {
    Position        Vector
    Orientation     Quaternion
    LinearVelocity  Vector
    AngularVelocity Vector
    AtRest          bool
}

func (b *RigidBody) Serialize(stream serialize.Stream) error {
    stream.SerializeObject(&b.Position)
    stream.SerializeObject(&b.Orientation)
    stream.SerializeBool(&b.AtRest)
    if !b.AtRest {
        stream.SerializeObject(&b.LinearVelocity)
        stream.SerializeObject(&b.AngularVelocity)
    } else if stream.IsReading() {
        b.LinearVelocity = Vector{}
        b.AngularVelocity = Vector{}
    }
    return stream.Error()
}
```

One serialize function handles write, read and measure:

```go
// write
writeStream := serialize.NewWriteStream(buffer)
if err := body.Serialize(writeStream); err != nil {
    // handle error
}
writeStream.Flush()
packet := writeStream.Data()

// read
readStream := serialize.NewReadStream(packet)
if err := body.Serialize(readStream); err != nil {
    // packet is truncated, corrupt or malicious
}

// measure
measureStream := serialize.NewMeasureStream()
body.Serialize(measureStream)
bytesRequired := measureStream.BytesProcessed()
```

Errors are sticky: the first failure latches on the stream and every later serialize call returns it without touching the stream. That is why serialize functions can simply call one serialize method per field and return `stream.Err()` at the end — or check every call, if you prefer early exits.

If you want separate read and write functions instead of unified ones, use the concrete `*ReadStream` and `*WriteStream` types directly: they have the same methods as the `Stream` interface, with no dynamic dispatch.

# Reading untrusted data

Packets come from the network and can be truncated or maliciously crafted. Every read is bounds checked and range validated, and the first failure latches an error on the stream: from then on every serialize call is a no-op that returns the same error and **leaves values unmodified**.

That last property implies one rule that the C++ library enforces invisibly (its serialize macros `return false` out of the enclosing function on the first failure) but that Go, having no macros, leaves to you:

**A value that controls how much more work your serialize function does — a loop count or a continuation bit — must have its error checked before you use it.**

Straight-line field sequences can defer checking to a single `stream.Err()` at the end. But a loop that waits for a serialized value to change will wait forever on a truncated packet, because failed reads never update values. That is a denial of service vector, and it bites in both bit polarities:

```go
// WRONG: spins forever on a truncated packet.
// The failed read is a no-op, so hasNext never becomes false.
hasNext := true
for hasNext {
    stream.SerializeBool(&hasNext)
    // ... serialize an element ...
}

// WRONG for the same reason: done never becomes true.
done := false
for !done {
    stream.SerializeBool(&done)
    // ... serialize an element ...
}
```

For sentinel-driven loops, use `serialize.Continue` (a true bit before each element) or `serialize.Until` (a true bit terminating the sequence), which fold the stream error state into the loop condition in the style of `bufio.Scanner`:

```go
hasNext := len(items) > 0 // when writing: true if there is a first element
i := 0
for serialize.Continue(stream, &hasNext) {
    // ... serialize element i ...
    i++
    if stream.IsWriting() {
        hasNext = i < len(items)
    }
}
if err := stream.Err(); err != nil {
    return err
}
```

The two helpers exist because the bit polarity is part of the wire format: when porting a protocol that already marks the end of a sequence with a true bit, use `Until` and keep the wire format unchanged. For any other loop whose condition depends on serialized state, include the error state in the condition yourself:

```go
for !done && stream.Err() == nil {
    // ...
}
```

For count-driven loops, check the error on the count before looping. On success the count is guaranteed to be in range; on failure it holds whatever value it had before, which matters if you reuse packet objects:

```go
if err := stream.SerializeInt(&numItems, 0, MaxItems); err != nil {
    return err
}
for i := int32(0); i < numItems; i++ {
    // ... serialize element i ...
}
```

Every successful serialize call consumes at least one bit, so any loop that follows these rules is bounded by the size of the packet. Nested objects are already safe: `SerializeObject` refuses to descend into an object once the stream has an error.

# Performance

All serialization paths are zero allocation. On an Apple M3 Ultra:

```
BenchmarkBitWriterWriteBits      2138 ns/op    987.78 MB/s    0 allocs/op
BenchmarkBitReaderReadBits       2176 ns/op    970.70 MB/s    0 allocs/op
BenchmarkWriteStreamPacket      108.4 ns/op   1226.65 MB/s    0 allocs/op
BenchmarkReadStreamPacket       98.59 ns/op   1349.09 MB/s    0 allocs/op
BenchmarkMeasureStreamPacket    41.77 ns/op   3208.03 MB/s    0 allocs/op
BenchmarkWriteBytes             26.61 ns/op   45091.93 MB/s   0 allocs/op
BenchmarkReadBytes              19.15 ns/op   62649.33 MB/s   0 allocs/op
```

The bitpacker benchmarks write and read 1024 values of mixed widths from 1 to 32 bits, around 2.1ns per value. The packet benchmarks serialize a representative 133 byte game network packet — quantized transform, compressed floats, ranged integers, events and a bulk payload — through the `Stream` interface, around 10 million packets per second per core.

The writer fills a 64 bit scratch and stores a qword at a time. The reader is effectively branchless: each read loads a 64 bit little endian window at the current byte position and shifts by the bit remainder, carrying no state between reads other than the bit index.

# Limitations

* Write buffer sizes must be a multiple of 8 bytes, because the bit writer flushes qwords to memory. Bytes past the end of the written data are only ever written as zeros. Buffers do not need any particular alignment.
* Read buffers may be any number of bytes. For the fastest reads, keep at least 7 bytes of slack in the backing array beyond the packet data — for example, read packets into a large buffer and slice the packet out of it. The reader detects the slack via `cap()` and uses fully branchless window loads; without slack, reads near the end of the buffer assemble the window from the remaining bytes instead. Slack bytes are loaded but never interpreted.
* Buffer sizes are effectively unlimited, because bit counts are stored in 64 bit signed integers.
* `SerializeWideString` stores 32 bits per code point and is wire compatible with `serialize_wstring` in the C++ library. Code points that are not valid (surrogates or values above 0x10FFFF) fail on read.

# Author

The author of this library is Glenn Fiedler.

Open source libraries by the same author include: [serialize](https://github.com/mas-bandwidth/serialize), [netcode](https://github.com/mas-bandwidth/netcode), [reliable](https://github.com/mas-bandwidth/reliable) and [yojimbo](https://github.com/mas-bandwidth/yojimbo)

If you find this software useful, [please consider sponsoring it](https://github.com/sponsors/mas-bandwidth). Thanks!

# License

[BSD 3-Clause license](https://opensource.org/licenses/BSD-3-Clause).
