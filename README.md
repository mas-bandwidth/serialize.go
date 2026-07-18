# Introduction

[![CI](https://github.com/mas-bandwidth/serialize.go/actions/workflows/ci.yml/badge.svg)](https://github.com/mas-bandwidth/serialize.go/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/mas-bandwidth/serialize.go.svg)](https://pkg.go.dev/github.com/mas-bandwidth/serialize.go)

**serialize.go** is a simple bitpacking serializer for Go.

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
go get github.com/mas-bandwidth/serialize.go
```

The package name is `serialize`, so no import alias is needed:

```go
import "github.com/mas-bandwidth/serialize.go"
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
    return stream.Err()
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
    return stream.Err()
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

That last property implies one rule: **a value that controls how much more work your serialize function does — a loop count or a continuation bit — must have its error checked before you use it.** A loop that waits for a serialized value to change will wait forever on a truncated packet, because failed reads never update values. That is a denial of service vector.

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

For count-driven loops, check the error on the count before looping:

```go
if err := stream.SerializeInt(&numItems, 0, MaxItems); err != nil {
    return err
}
```

The failure modes, why both sentinel polarities exist, and why loops that follow these rules are always bounded by the packet size are covered in [docs/reading_untrusted_data.md](docs/reading_untrusted_data.md).

# Performance

All serialization paths are zero allocation. On an Apple M3 Ultra the bitpacker writes and reads mixed width values at around 2.1ns per value, and a representative 133 byte game network packet serializes at around 10 million packets per second per core. Interface dispatch through `Stream` costs only 6-8% over the concrete stream types.

The C++ serialize library is 2-6× faster on the same benchmarks, mostly because its release builds compile away the safety checks that serialize.go deliberately keeps on in every build. Full benchmark numbers and the cross language comparison are in [docs/performance.md](docs/performance.md).

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

[MBSL](LICENSE).

## Crediting

This library is licensed under the [Más Bandwidth Source License (MBSL)](LICENSE),
which is BSD 3-Clause plus one clause: products that incorporate it must include
this credit in their product credits, or in their documentation:

> **Más Bandwidth LLC**
> serialize.go by Glenn Fiedler

Free to use, source open, credit required. Fair credit keeps open source honest.

<!-- CAA gate pilot test, safe to delete -->
