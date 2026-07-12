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

All serialization paths are zero allocation. On an Apple M3 Ultra (mean of 10 runs via benchstat, variance within ±2%):

```
BenchmarkBitWriterWriteBits         2134 ns/op     989.7 MB/s    0 allocs/op
BenchmarkBitReaderReadBits          2167 ns/op     974.6 MB/s    0 allocs/op
BenchmarkWriteStreamPacket         108.0 ns/op    1231.5 MB/s    0 allocs/op
BenchmarkReadStreamPacket           99.1 ns/op    1342.1 MB/s    0 allocs/op
BenchmarkWriteStreamPacketDirect   101.8 ns/op    1306.5 MB/s    0 allocs/op
BenchmarkReadStreamPacketDirect     91.3 ns/op    1457.5 MB/s    0 allocs/op
BenchmarkMeasureStreamPacket        41.5 ns/op    3206.4 MB/s    0 allocs/op
BenchmarkWriteBytes                 26.0 ns/op   46189.4 MB/s    0 allocs/op
BenchmarkReadBytes                  18.7 ns/op   64034.2 MB/s    0 allocs/op
```

The bitpacker benchmarks write and read 1024 values of mixed widths from 1 to 32 bits, around 2.1ns per value. The packet benchmarks serialize a representative 133 byte game network packet — quantized transform, compressed floats, ranged integers, events and a bulk payload — around 10 million packets per second per core. The `Direct` variants use the concrete `*WriteStream` and `*ReadStream` types instead of the `Stream` interface: interface dispatch costs only 6-8%.

The writer fills a 64 bit scratch and stores a qword at a time. The reader is effectively branchless: each read loads a 64 bit little endian window at the current byte position and shifts by the bit remainder, carrying no state between reads other than the bit index.

# How does it compare to the C++ library?

[bench/cpp/bench.cpp](bench/cpp/bench.cpp) mirrors the Go benchmark suite one for one against the C++ serialize library (v1.4.3), including a check that the benchmark packet is byte identical between the two implementations. Same machine, Apple clang 21 at `-O3 -DNDEBUG`, best mean of 5 repetitions, with optimization barriers so the compiler cannot delete the work:

| Benchmark                  | Go (interface) | Go (concrete) | C++ (`-O3 -DNDEBUG`) | C++ advantage |
|----------------------------|---------------:|--------------:|---------------------:|--------------:|
| Bitpacker write, per value |        2.08 ns |             — |               0.54 ns |          3.9× |
| Bitpacker read, per value  |        2.12 ns |             — |               0.54 ns |          3.9× |
| Packet write (133 bytes)   |       108.0 ns |      101.8 ns |               36.3 ns |          2.8× |
| Packet read (133 bytes)    |        99.1 ns |       91.3 ns |               14.9 ns |          6.1× |
| Packet measure             |        41.5 ns |             — |              ~0.3 ns¹ |     see note¹ |
| Bulk write (1200 bytes)    |        26.0 ns |             — |               13.9 ns |          1.9× |
| Bulk read (1200 bytes)     |        18.7 ns |             — |               12.6 ns |          1.5× |

¹ clang constant folds the entire measurement through the templates and inlined stream, reducing it to almost nothing. Go computes it at runtime.

C++ is 3-4× faster at raw bitpacking, ~3× faster writing packets, ~6× faster reading them, and the gap nearly closes on bulk bytes where both sides are memcpy bound. In my opinion the gap breaks down into three causes, in order of importance:

1. **Always-on safety.** A `-DNDEBUG` C++ build compiles away every check: the C++ reader is genuinely branchless and trusts the caller's buffer contract. The Go implementation deliberately keeps bounds checks, range validation and the sticky error check on every operation in every build, because its contract is that malicious packet data can never panic, corrupt memory, or smuggle out of range values. Most of the 6× read gap is the price of that contract. This is a different default, not a missing optimization: the C++ library makes the same checks available in debug builds and removes them in release; Go release builds keep them.

2. **Inlining depth.** clang inlines the entire templated serialize function into straight line code and optimizes across field boundaries. Go's compiler inlines the hot bitpacker internals but keeps one function call per field, and cannot fold constants across the stream the way clang folds `MeasureStream` down to ~0.3ns.

3. **Interface dispatch is nearly irrelevant.** The measured cost of Go's `Stream` interface versus concrete stream types is 6-8% — folklore says dynamic dispatch is the expensive part of designs like this, but the numbers say the safety checks and inlining dominate.

Practical read: at ~10 million packets per second per core, goserialize is unlikely to be the bottleneck in any real workload — encryption, syscalls and game logic will dominate long before serialization does. If a Go codebase is where your service lives, the safety-checked speed here is more than enough. If you need the last 3-6× and accept unchecked release builds, the C++ library is the same wire format — and because the two are bit identical on the wire, mixing them (C++ game server, Go backend services and tools) works seamlessly.

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
