# goserialize

Pure Go port of the C++ serialize library (github.com/mas-bandwidth/serialize).
Module `github.com/mas-bandwidth/goserialize`, package name `serialize` (deliberate:
short call sites, matches the C++ namespace; the import alias is documented in the
README). Zero dependencies, no cgo, BSD-3.

## Invariants — never break these

1. **The wire format is frozen and bit-identical to the C++ library.**
   `TestGoldenWireFormat` pins 72 golden bytes copied verbatim from the C++ test
   suite, and `bench/cpp/bench.cpp` asserts the benchmark packet is byte identical.
   Never change any encoding without coordinating with the C++ library. When adding
   serialization features, port them from serialize.h and mirror its tests.
2. **Malicious packet data never panics.** Every stream read is bounds checked and
   range validated and fails with an error. Panics are reserved for API misuse only
   (bits out of [1,32]/[1,64], min >= max, write buffer not a multiple of 8 bytes).
   The fuzz targets enforce this — keep them passing.
3. **Sticky errors and the control flow rule.** The first error latches on the
   stream; later serialize calls are no-ops that leave values unmodified. Therefore
   any serialized value that controls a loop (count, sentinel bit) must have its
   error checked before use, or the loop spins forever on truncated packets — a DoS.
   `Continue` (continuation-bit polarity) and `Until` (termination-bit polarity) are
   the safe sentinel loop primitives; both polarities are needed because the polarity
   is part of the wire format. See the README "Reading untrusted data" section.
4. **Zero allocations on all serialization paths.** Benchmarks assert with
   ReportAllocs.
5. **Write buffers must be multiples of 8 bytes** (the writer stores qwords). The
   reader accepts any length and detects backing-array slack via cap() to use fully
   branchless 64 bit window loads; slack bytes are loaded but never interpreted.

## Layout

- `serialize.go` — package doc, sentinel errors, panic messages, BitsRequired(64), zigzag
- `bitpacker.go` — BitWriter (64 bit scratch, LE qword stores), BitReader (branchless
  64 bit windows at byte granularity), generic `writeBytes[~[]byte|~string]`
- `stream.go` — Stream interface, Serializer, Continue/Until, compressed float params,
  int-relative buckets
- `write_stream.go` / `read_stream.go` / `measure_stream.go` — the three concrete
  streams; methods are implemented per stream (no shared dispatch) for speed
- `serialize_test.go` — ported C++ test suite + golden wire test + DoS termination tests
- `fuzz_test.go`, `bench_test.go`, `example_test.go` (examples ported from example.cpp)
- `bench/cpp/bench.cpp` — C++ comparison benchmark (results + analysis in README)

## Commands

- `go test ./...` — full suite (includes a 320MB large-buffer test; `-short` skips it)
- `go test -short -race ./...`
- `go test -fuzz=FuzzReadStream -fuzztime=30s .` (also FuzzRoundTrip)
- `go test -run=NONE -bench=. -count=10 -benchtime=0.5s .` then benchstat
- `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run` —
  config in `.golangci.yml`; errcheck is deliberately excluded from `_test.go`
  (the documented sticky-error pattern leaves calls unchecked there)
- `go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./...`
- C++ comparison: see the header comment in `bench/cpp/bench.cpp`

## CI (.github/workflows/ci.yml)

Push/PR: 3-OS test matrix (race + full + bench smoke), lint (golangci-lint +
modernize + `go mod tidy -diff`), vuln (govulncheck), cross (linux/386 full tests —
32 bit `int` coverage for the int64 bit counts — plus s390x and wasm build checks),
fuzz (30s per target). PR-only: benchmark (benchstat base vs head in the job
summary) and apicompat (gorelease vs the latest tag in the job summary).

## Conventions

- Method names mirror the C++ API (SerializeBits, SerializeInt, SerializeAlign...);
  accessors follow Go stdlib conventions (`Err()` not `Error()`, no Get prefixes);
  single-method interfaces use -er (`Serializer`).
- Errors are the sentinels ErrOverflow, ErrValueOutOfRange, ErrAlign — no allocation.
- On read failure values are left unmodified (matches C++; documented).
- Comment style follows the C++ library's voice; doc comments explain contracts.

## Releases

Semver tags; the apicompat CI job (gorelease) suggests the next version. New exported
API = minor bump. v1.0.0 is retracted in go.mod (immediate post-release renames).
History: v1.0.1 naming review, v1.0.2 dead code audit, v1.1.0 Continue, v1.2.0 Until,
v1.2.1 C++ comparison, v1.2.2 examples/badge/CLAUDE.md. After tagging, prime the
module proxy: `curl https://proxy.golang.org/github.com/mas-bandwidth/goserialize/@v/<tag>.info`.
Update README benchmark numbers only from fresh runs on the stated hardware
(Apple M3 Ultra) with the stated methodology.
