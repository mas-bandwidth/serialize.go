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

[bench/cpp/bench.cpp](../bench/cpp/bench.cpp) mirrors the Go benchmark suite one for one against the C++ serialize library (v1.4.3), including a check that the benchmark packet is byte identical between the two implementations. Same machine, Apple clang 21 at `-O3 -DNDEBUG`, best mean of 5 repetitions, with optimization barriers so the compiler cannot delete the work:

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
