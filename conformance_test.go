package serialize

import (
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The shared conformance corpus is the instrument STANDARD.md names: the vendored
// conformance/ directory, one file per operation, holding the accepted and refused
// vectors the standard's rules require. Every vector runs through this port's reader
// here. Nothing in this file regenerates an expectation — the bytes, the value and the
// bit count all come from the corpus — because a suite that computes its own answers
// proves only that the port agrees with itself.
//
// The corpus is vendored the way STANDARD.md is, and the corpus-sync CI job fails when
// the two copies diverge.

// conformanceVector is one record of the vector format specified in STANDARD.md,
// "The vector format".
type conformanceVector struct {
	file      string
	line      int
	operation string
	name      string
	params    map[string]string
	bytes     []byte
	refused   bool
	value     string
	consumed  int64
}

func (v conformanceVector) param(t *testing.T, key string) string {
	t.Helper()
	value, ok := v.params[key]
	if !ok {
		t.Fatalf("%s:%d: vector %q has no %s parameter", v.file, v.line, v.name, key)
	}
	return value
}

// parseConformanceFile parses one vector file. A record is a run of non-blank lines;
// '#' begins a comment.
func parseConformanceFile(t *testing.T, path string) []conformanceVector {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var vectors []conformanceVector
	current := conformanceVector{file: filepath.Base(path), params: map[string]string{}}
	started := false

	flush := func() {
		if started {
			vectors = append(vectors, current)
			current = conformanceVector{file: filepath.Base(path), params: map[string]string{}}
			started = false
		}
	}

	for i, raw := range strings.Split(string(content), "\n") {
		line := raw
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			line = line[:hash]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if !started {
			started = true
			current.line = i + 1
		}
		key, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		switch key {
		case "operation":
			current.operation = rest
		case "name":
			current.name = rest
		case "param":
			paramName, paramValue, ok := strings.Cut(rest, "=")
			if !ok {
				t.Fatalf("%s:%d: param needs `name = value`, got %q", current.file, i+1, rest)
			}
			current.params[strings.TrimSpace(paramName)] = strings.TrimSpace(paramValue)
		case "bytes":
			for _, pair := range strings.Fields(rest) {
				b, err := strconv.ParseUint(pair, 16, 8)
				if err != nil {
					t.Fatalf("%s:%d: bad hex byte %q: %v", current.file, i+1, pair, err)
				}
				current.bytes = append(current.bytes, byte(b))
			}
		case "expect":
			if rest == "refused" {
				current.refused = true
				break
			}
			expectKey, expectValue, ok := strings.Cut(rest, "=")
			if !ok || strings.TrimSpace(expectKey) != "value" {
				t.Fatalf("%s:%d: expect needs `refused` or `value = ...`, got %q", current.file, i+1, rest)
			}
			current.value = strings.TrimSpace(expectValue)
		case "consumed":
			consumed, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				t.Fatalf("%s:%d: bad consumed count %q: %v", current.file, i+1, rest, err)
			}
			current.consumed = consumed
		default:
			t.Fatalf("%s:%d: unknown key %q", current.file, i+1, key)
		}
	}
	flush()
	return vectors
}

// int128FromDecimal parses a 128 bit decimal literal from a vector. Values outside the
// signed range are taken modulo 2^128, which is the two's complement bit pattern the
// pair stores.
func int128FromDecimal(t *testing.T, text string) Int128 {
	t.Helper()
	v, ok := new(big.Int).SetString(text, 10)
	if !ok {
		t.Fatalf("not a decimal integer: %q", text)
	}
	return uint128FromBig(v).Int128()
}

// TestConformanceCorpus runs every vector in the vendored corpus through the read
// stream. Accepted vectors must yield the stated value and consume the stated number of
// bits; refused vectors must be refused, and must leave the destination unwritten
// (STANDARD.md, Reader Obligations).
//
// Each vector runs twice: once over a tight slice, which drives the reader's zero padded
// tail window, and once with backing slack, which drives the branchless window loads.
// The two paths are separate code in bitpacker.go and must agree on every vector.
func TestConformanceCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("conformance", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no conformance vectors found: the vendored conformance/ directory is missing")
	}

	total := 0
	for _, file := range files {
		for _, vector := range parseConformanceFile(t, file) {
			if vector.operation == "" || vector.name == "" {
				t.Fatalf("%s:%d: vector needs both an operation and a name", vector.file, vector.line)
			}
			total++
			t.Run(vector.operation+"/"+vector.name, func(t *testing.T) {
				for _, slack := range []int{0, 16} {
					name := "tight"
					if slack > 0 {
						name = "slack"
					}
					t.Run(name, func(t *testing.T) {
						buffer := make([]byte, len(vector.bytes), len(vector.bytes)+slack)
						copy(buffer, vector.bytes)
						runConformanceVector(t, vector, NewReadStream(buffer))
					})
				}
			})
		}
	}
	t.Logf("ran %d conformance vectors from %d files", total, len(files))
}

func runConformanceVector(t *testing.T, vector conformanceVector, stream *ReadStream) {
	t.Helper()
	switch vector.operation {
	case "int_relative":
		previous, err := strconv.ParseInt(vector.param(t, "previous"), 10, 32)
		if err != nil {
			t.Fatalf("bad previous parameter: %v", err)
		}
		// a sentinel no vector expects: a refused read must leave it in place
		current := int32(-424242)
		readErr := stream.SerializeIntRelative(int32(previous), &current)
		if vector.refused {
			checkRefused(t, vector, readErr, current == -424242)
			return
		}
		want, err := strconv.ParseInt(vector.value, 10, 32)
		if err != nil {
			t.Fatalf("bad expected value: %v", err)
		}
		checkAccepted(t, vector, readErr, stream, int64(current) == want, strconv.FormatInt(int64(current), 10))

	case "int128":
		min := int128FromDecimal(t, vector.param(t, "min"))
		max := int128FromDecimal(t, vector.param(t, "max"))
		sentinel := Int128{Lo: 0xDEADBEEFDEADBEEF, Hi: 0xDEADBEEFDEADBEEF}
		current := sentinel
		readErr := stream.SerializeInt128(&current, min, max)
		if vector.refused {
			checkRefused(t, vector, readErr, current == sentinel)
			return
		}
		want := int128FromDecimal(t, vector.value)
		checkAccepted(t, vector, readErr, stream, current == want, bigFromInt128(current).String())

	default:
		t.Fatalf("%s:%d: no runner for operation %q — the corpus gained an operation this port does not exercise",
			vector.file, vector.line, vector.operation)
	}
}

func checkRefused(t *testing.T, vector conformanceVector, readErr error, unwritten bool) {
	t.Helper()
	if readErr == nil {
		t.Fatalf("%s:%d: vector %q was accepted, the corpus says it must be refused",
			vector.file, vector.line, vector.name)
	}
	if !unwritten {
		t.Fatalf("%s:%d: vector %q was refused but wrote its destination",
			vector.file, vector.line, vector.name)
	}
}

func checkAccepted(t *testing.T, vector conformanceVector, readErr error, stream *ReadStream, matched bool, got string) {
	t.Helper()
	if readErr != nil {
		t.Fatalf("%s:%d: vector %q was refused (%v), the corpus says it must be accepted",
			vector.file, vector.line, vector.name, readErr)
	}
	if !matched {
		t.Fatalf("%s:%d: vector %q decoded to %s, the corpus says %s",
			vector.file, vector.line, vector.name, got, vector.value)
	}
	if stream.BitsProcessed() != vector.consumed {
		t.Fatalf("%s:%d: vector %q consumed %d bits, the corpus says %d",
			vector.file, vector.line, vector.name, stream.BitsProcessed(), vector.consumed)
	}
}
