module github.com/mas-bandwidth/goserialize

go 1.23

// v1.0.0 was replaced immediately after release: Stream.Error was renamed to
// Stream.Err and Serializable to Serializer, following Go naming conventions.
retract v1.0.0
