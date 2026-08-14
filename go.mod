module github.com/mas-bandwidth/serialize.go

go 1.23

// v1.0.0 was replaced immediately after release: Stream.Error was renamed to
// Stream.Err and Serializable to Serializer, following Go naming conventions.
retract v1.0.0

// v1.8.0 writes divergent compressed_float bytes on arm64: the writer's
// quantization compiled to a fused multiply-add (one rounding where the wire
// format requires two), so Apple Silicon builds disagree with the C, C++, C#
// and Rust ports for values near a half-quantum boundary. Fixed in v1.8.1.
retract v1.8.0
