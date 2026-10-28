# Reading untrusted data

Packets come from the network and can be truncated or maliciously crafted. Every read is bounds checked and range validated, and the first failure latches an error on the stream: from then on every serialize call is a no-op that returns the same error and **leaves values unmodified**. The error is a `serialize.Refusal` that names the rule that refused the packet, from the closed list in STANDARD.md, and the stream keeps the first one: the refusals of later calls are its echoes.

A failure is terminal, as the wire format requires: nothing after a failed read has a defined position, so nothing after it is interpretable. Only `Reset` — pointing the stream at a new buffer — clears the latch. The one value a refused read does not promise to leave alone is a caller-owned byte slice passed to `SerializeBytes`: treat its contents as unspecified after a failure.

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

For count-driven loops, bound the count with an application-appropriate `MaxItems`, check the error on the count before looping, and check `stream.Err()` inside the loop body if an iteration can consume zero bits or do substantial work before returning. On success the count is guaranteed to be in `[0, MaxItems]`; on failure it holds whatever value it had before, which matters if you reuse packet objects:

```go
if err := stream.SerializeInt(&numItems, 0, MaxItems); err != nil {
    return err
}
for i := int32(0); i < numItems; i++ {
    // ... serialize element i ...
    if err := stream.Err(); err != nil {
        return err
    }
}
```

A `serialize.Continue` or `serialize.Until` loop is bounded by the bit length of the packet because each successful sentinel read (`SerializeBool`) consumes one bit and stops as soon as the stream latches an error. Do not assume that every other successful serialize call consumes at least one bit: the wire format includes legal zero-bit operations — degenerate ranges where `min == max` (`SerializeInt`, `SerializeInt64`, `SerializeInt128`, `SerializeFixed64`, `SerializeFixed128`), a successful empty `SerializeString(&s, 1)` when the stream is already byte-aligned — its length range `[0, 0]` costs zero bits, but an unaligned stream still consumes padding, and a nonempty writer value is write misuse and panics, `SerializeBytes(nil)` or `SerializeBytes(buf[:0])` when already byte-aligned, `SerializeAlign()` when already at a byte boundary (`AlignBits() == 0`), and `SerializeObject` on an object whose `Serialize` method performs only zero-bit operations. Consequently, a count-driven loop whose body can execute only zero-bit operations (or remain running after a latched error without checking `stream.Err()`) is bounded by `MaxItems`, not by packet size.

Likewise, `SerializeObject` skips calling `object.Serialize(stream)` once an error is already latched on the stream and latches any error that `object.Serialize` returns, but refusal propagation is not a recursion or depth bound: recursive or self-referential object graphs must still bound their own nesting depth (and consume at least one bit or check a caller depth counter before recursing), because `SerializeObject` itself does not track call depth or guarantee bit consumption.

## The end of the packet

A packet is one object and its end. After the last field of the top level object, call `SerializeEnd` on the read stream, which must hold exactly the packet:

```go
readStream := serialize.NewReadStream(packet)
if err := message.Serialize(readStream); err != nil {
    return err
}
if err := readStream.SerializeEnd(); err != nil {
    return err // ErrTrailingData or ErrNonzeroPadding
}
```

A whole byte or more left over is refused with `ErrTrailingData`, even when it is zero, and a set bit in the final byte's unused bits with `ErrNonzeroPadding`. Without the end those bits are never examined, so two different packets would decode to the same value and the format's guarantee, one value with exactly one byte sequence, would not hold for your reads. The end writes nothing and measures nothing, so it can live in a unified serialize function for the whole packet, though never in one that is nested with `SerializeObject`.
