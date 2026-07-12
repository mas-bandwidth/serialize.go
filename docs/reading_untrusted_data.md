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
