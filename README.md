# Introduction

[![CI](https://github.com/mas-bandwidth/serialize.go/actions/workflows/ci.yml/badge.svg)](https://github.com/mas-bandwidth/serialize.go/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/mas-bandwidth/serialize.go/v2.svg)](https://pkg.go.dev/github.com/mas-bandwidth/serialize.go/v2)

![serialize.go logo with a packed row of colored blocks](images/serialize.png)

**serialize.go** is a simple bitpacking serializer for Go.

It is a pure Go port of the C++ [serialize](https://github.com/mas-bandwidth/serialize) library, with no native code. The two libraries produce bit-for-bit identical output, so streams written by one language can be read by the other. This is pinned down by a golden wire format test whose bytes are the whole message STANDARD.md prints, the same bytes the C++ test suite pins.

The wire format is specified in [STANDARD.md](STANDARD.md), vendored here verbatim from the [serialize](https://github.com/mas-bandwidth/serialize) repository along with the shared conformance corpus in [conformance/](conformance); CI fails if any copy drifts from upstream. **This library implements format version 2.0**: the wire carries only integers and bytes, and every value has exactly one encoding. Every vector in the corpus runs through the read stream in `TestConformanceCorpus`, which discovers the directory rather than naming its files: a vector whose operation it cannot drive fails the run, and a refused vector must be refused for the reason it states. Vectors marked `writer = canonical` are re-emitted through the write stream and compared byte for byte, vectors carrying `writer_input` hand the writer that input and compare its bytes, and vectors carrying `measure_at_least` are checked against the measure stream's floor. `TestConformanceControls` proves the runner responds: for each refusal rule it runs a reader with that one rule deleted against the corpus, and requires it to fail exactly the vectors [conformance-controls/](conformance-controls) names.

Format 2.0 is a major version of this module, `github.com/mas-bandwidth/serialize.go/v2`. What changed from 1.1, and what it breaks:

* `float` and `double` carry finite values only. NaN and the infinities are refused on read with `ErrNonFiniteFloat`, and refused on write too. Negative zero and the subnormals travel bit exact.
* A `compressed_float` decode that is not finite is refused with `ErrNonFiniteFloat`, and a declaration whose top decodes to an infinity panics on write.
* `SerializeWideString` is gone. Text travels as `SerializeString`, which is UTF-8.
* `SerializeEnd` is new, on every stream and on the `Stream` interface. A packet reader calls it after the top level object: it refuses a byte left over with `ErrTrailingData` and a set padding bit with `ErrNonzeroPadding`.
* `int_relative`'s absolute tier carrying a difference a bounded tier takes is refused with `ErrNonCanonical`.
* Every refusal names its rule. The errors are values of type `Refusal`, one per reason in STANDARD.md's closed list, and `Refusal.String` spells the reason exactly as the standard does (`past_end`, `value_out_of_range`, `nonzero_padding`, `trailing_data`, `non_finite_float`, `invalid_utf8`, `embedded_nul`, `non_canonical`). `ErrOverflow` is now `ErrPastEnd` and `ErrAlign` is now `ErrNonzeroPadding`; a string with a NUL is `ErrEmbeddedNul` and one that is not UTF-8 is `ErrInvalidUTF8`, where 1.1 said `ErrValueOutOfRange` for both.

Standard 2.1 (still format 2.0, so the wire is unchanged) adds the ninth reason, `rejected` (`ErrRejected`): the caller's own refusal of a value whose bytes were well formed. Call `ReadStream.Reject`, or `serialize.Reject(stream)` from a unified serialize function, to refuse a read; a nested object whose `Serialize` method returns an error with nothing refused inside it latches `ErrRejected` too, wrapping that error so `errors.Is` still finds it. It goes through the same latch as every other refusal, so it is terminal and the first reason wins. On a write or measure stream `Reject` latches nothing and returns `ErrRejected`.

Standard 2.1 also makes the family's uniform rules normative (IMPLEMENTING.md U1 to U16), and this release keeps them, each proved in `uniform_test.go`:

* A read stream never panics, whatever the caller declares. A bad declaration on read (a bit count outside its range, `min > max`, a fixed point bound outside its Q format, a compressed float declaration or precomputed constants that cannot be derived, a string `bufferSize` below 1, a negative `int_relative` `previous`) refuses with `ErrValueOutOfRange`, and a stream that already refused keeps its first reason. The write and measure streams still panic on the same declarations.
* A writer never sends bytes that differ from the caller's value: `SerializeBits` and `SerializeBits64` refuse a value wider than its field with `ErrValueOutOfRange` instead of masking it, and `SerializeString` refuses text with a NUL (`ErrEmbeddedNul`) or that is not well-formed UTF-8 (`ErrInvalidUTF8`) on write and measure, the reasons a reader would give.
* `SerializeBytes` and `SerializeString` check room for the whole operation before writing any of it, so a refused write leaves no length or align bits behind.
* A compressed float declaration whose `delta` or `delta / resolution` is not finite panics on write and measure, and the measure stream panics on a top decode that is not finite, as the writer already did. Precomputed constants must have `maxIntegerValue` in `[1, 4294967040]`.
* `ReadStream.Fail(reason)`, and `serialize.Fail(stream, reason)` from a unified serialize function, refuse a read with a reason of your choosing; a reason outside the closed list latches `ErrRejected`. `RefusalName(err)` gives the family name of any stream error, `"none"` for nil.

It has the following features:

* Serialize a bool with only one bit
* Serialize any integer value from [1,64] bits writing only that number of bits to the buffer
* Serialize signed integer values with [min,max] writing only the required bits to the buffer
* Serialize floats, doubles, compressed floats, strings, byte arrays, and integers relative to another integer, in the non-negative `int32` domain
* Serialize fixed point values with a Q format and [min,max] bounds in whole units, writing only the required bits — round trips are exact, unlike compressed floats. Wide formats like Q112.16 work via the 128 bit pair
* Serialize 128 bit integers: `Uint128` raw at a full 128 bits, `Int128` ranged in only the bits its range needs — and where that range fits 64 bits the bytes are identical to `SerializeInt64`
* Alignment support so you can align your bitstream to a byte boundary whenever you want
* Unified serialization through the `Stream` interface, so you can write one function that handles read, write and measure
* Zero allocations on every serialization path, except a read that constructs a string: `SerializeString` allocates the value it hands back when its content changed
* Every read is bounds checked and range validated, so maliciously crafted packets fail with errors instead of panicking, and every error names the rule that refused the packet. A read stream never panics on a caller's bad declaration either: it refuses it with `ErrValueOutOfRange`

If this library helps you, **[please support it](https://www.patreon.com/MasBandwidth/membership)**

# Usage

The module is published on the Go module proxy under its import path, so it
installs like any other Go dependency:

```
go get github.com/mas-bandwidth/serialize.go/v2
```

That resolves the newest release. Append `@` and a tag to pin a specific one.
The `consumer` CI job builds a fresh module outside this repository, installs
this one from the proxy, and round trips a message through it, so the install
line above is checked on every push and every night rather than claimed.
The package name is `serialize`, so no import alias is needed:

```go
import "github.com/mas-bandwidth/serialize.go/v2"
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

// read: the object, then its end, which refuses trailing bytes and set padding
readStream := serialize.NewReadStream(packet)
if err := body.Serialize(readStream); err != nil {
    // packet is truncated, corrupt or malicious
}
if err := readStream.SerializeEnd(); err != nil {
    // packet carries more than the object
}

// measure
measureStream := serialize.NewMeasureStream()
body.Serialize(measureStream)
bytesRequired := measureStream.BytesProcessed()
```

A packet reader must read the end after the top level object (STANDARD.md, "end"). Without it, trailing bytes and padding go unexamined, and one value no longer has exactly one byte sequence. The end writes nothing and measures nothing, so a unified serialize function for a whole packet can carry it too.

Errors are sticky: the first failure latches on the stream and every later serialize call returns it without touching the stream. That is why serialize functions can simply call one serialize method per field and return `stream.Err()` at the end — or check every call, if you prefer early exits.

If you want separate read and write functions instead of unified ones, use the concrete `*ReadStream` and `*WriteStream` types directly: they have the same methods as the `Stream` interface, with no dynamic dispatch.

# Reading untrusted data

Packets come from the network and can be truncated or maliciously crafted. Every read is bounds checked and range validated, and the first failure latches an error on the stream: from then on every serialize call is a no-op that returns the same error and **leaves values unmodified**. The error is a `serialize.Refusal` naming the rule that refused the packet, and the stream keeps the first one: later refusals are its echoes.

A failure is terminal, as the wire format requires: nothing after a failed read has a defined position, so nothing after it is interpretable. Only `Reset` — pointing the stream at a new buffer — clears the latch. The one value a refused read does not promise to leave alone is a caller-owned byte slice passed to `SerializeBytes`: treat its contents as unspecified after a failure.

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

All serialization paths are zero allocation, with one exception: a read that constructs a string allocates it. `SerializeString` allocates only when the incoming bytes differ from what `*value` already holds, so re-reading stable strings into the same value costs nothing. Everything else — every integer, float, fixed point, 128 bit, byte array and align path, on all three streams — allocates nothing. Benchmarking for the serialize family lives in [mas-bandwidth/schema](https://github.com/mas-bandwidth/schema)'s data-driven bench, which measures the generated codecs across every language on one corpus.

# Limitations

* Write buffer sizes must be a multiple of 8 bytes, because the bit writer flushes qwords to memory. Bytes past the end of the written data are only ever written as zeros. Buffers do not need any particular alignment.
* Read buffers may be any number of bytes. For the fastest reads, keep at least 12 bytes of slack in the backing array beyond the packet data — for example, read packets into a large buffer and slice the packet out of it. The reader detects the slack via `cap()` and takes one branch per window load, selecting the window source: with slack, every load comes straight from the buffer; without slack, reads near the end of the buffer load their window from a small zero padded tail copied at `Reset`. Slack (or padding) bytes are loaded but never interpreted. (This number is 12 since v1.14.0 and was 7 through v1.13.0, because the 64 bit window load now reaches twelve bytes.)
* Buffer sizes are effectively unlimited, because bit counts are stored in 64 bit signed integers.
* `SerializeIntRelative` works in the `int_relative` domain, 0 to 2147483647 (STANDARD.md). `previous` is your own state and never arrives off the wire, so a negative `previous` is API misuse and panics. On read the reconstruction is checked in every tier: a value that leaves the domain, or that does not exceed `previous`, fails with `ErrValueOutOfRange` and leaves the destination unwritten.
* `SerializeIntRelative`'s absolute tier carries only differences above 69914, the last bounded tier's top: one that carries a smaller difference has a second encoding, and fails with `ErrNonCanonical`.
* `SerializeString` carries well-formed UTF-8 with no NUL, and hands a well-formed payload back byte for byte: no normalization, no replacement character, no stripped byte order mark. The writer trusts its caller, as it does for every value: it does not scan the string, so a string that breaks the rule is refused by the reader on the far side (`ErrEmbeddedNul`, `ErrInvalidUTF8`).

# Author

The author of this library is Glenn Fiedler.

Open source libraries by the same author include: [serialize](https://github.com/mas-bandwidth/serialize), [netcode](https://github.com/mas-bandwidth/netcode), [reliable](https://github.com/mas-bandwidth/reliable) and [yojimbo](https://github.com/mas-bandwidth/yojimbo)

If you find this software useful, please consider [becoming a supporter](https://www.patreon.com/MasBandwidth/membership). Thanks!

# License

[BSD 3-Clause](LICENSE).

## Crediting

Licensed [BSD 3-Clause](LICENSE), which asks only that you keep the copyright
notice. Credit is not required — but if you would like to give it:

> serialize.go by Glenn Fiedler and Rowan Claude

Free to use, source open, credit required. Fair credit keeps open source honest.
