package serialize

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// The shared conformance corpus is the instrument STANDARD.md names: the vendored
// conformance/ directory, one file per covered operation, holding the accepted and refused
// vectors the standard's rules require. Every vector runs through this port's reader here,
// and the vectors that carry a writer or a measure obligation run through the write stream
// and the measure stream too. Nothing in this file regenerates an expectation, because a
// suite that computes its own answers proves only that the port agrees with itself.
//
// The runner DISCOVERS the directory rather than naming its files, so a newly vendored
// vector file runs without anyone editing a list, an empty directory is a failure rather
// than a pass, and a vector whose operation this runner cannot drive is a failure rather
// than a skip.
//
// The corpus is vendored the way STANDARD.md is, and the corpus-sync CI job fails when the
// two copies diverge.

type expectKind int

const (
	expectValue expectKind = iota
	expectBits
	expectRefused
)

// conformanceVector is one record of the vector format specified in STANDARD.md,
// "The vector format".
type conformanceVector struct {
	file            string
	line            int
	operation       string
	name            string
	paramNames      []string
	params          map[string]string
	stepText        []string
	bytes           []byte
	expectKind      expectKind
	expect          string
	consumed        int64
	hasConsumed     bool
	measureAtLeast  int64
	hasMeasure      bool
	writerCanonical bool
}

func (v conformanceVector) where() string {
	return fmt.Sprintf("%s:%d: vector %q", v.file, v.line, v.name)
}

// parseConformanceFile parses one vector file. A record is a run of non-blank lines, and
// '#' begins a comment at the start of a line and nowhere else.
func parseConformanceFile(t *testing.T, path string) []conformanceVector {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var vectors []conformanceVector
	base := filepath.Base(path)
	current := conformanceVector{file: base, params: map[string]string{}}
	started := false

	flush := func() {
		if started {
			vectors = append(vectors, current)
			current = conformanceVector{file: base, params: map[string]string{}}
			started = false
		}
	}

	for i, raw := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(raw, "#") {
			continue
		}
		line := strings.TrimSpace(raw)
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
				t.Fatalf("%s:%d: param needs `name = value`, got %q", base, i+1, rest)
			}
			paramName = strings.TrimSpace(paramName)
			paramValue = strings.TrimSpace(paramValue)
			if paramName == "step" {
				current.stepText = append(current.stepText, paramValue)
				break
			}
			current.paramNames = append(current.paramNames, paramName)
			current.params[paramName] = paramValue
		case "bytes":
			for _, pair := range strings.Fields(rest) {
				b, err := strconv.ParseUint(pair, 16, 8)
				if err != nil {
					t.Fatalf("%s:%d: bad hex byte %q: %v", base, i+1, pair, err)
				}
				current.bytes = append(current.bytes, byte(b))
			}
		case "expect":
			if rest == "refused" {
				current.expectKind = expectRefused
				break
			}
			kind, value, ok := strings.Cut(rest, "=")
			if !ok {
				t.Fatalf("%s:%d: expect needs `refused`, `value = ...` or `bits = ...`, got %q", base, i+1, rest)
			}
			switch strings.TrimSpace(kind) {
			case "value":
				current.expectKind = expectValue
			case "bits":
				current.expectKind = expectBits
			default:
				t.Fatalf("%s:%d: unknown expect kind %q", base, i+1, strings.TrimSpace(kind))
			}
			current.expect = strings.TrimSpace(value)
		case "consumed":
			consumed, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				t.Fatalf("%s:%d: bad consumed count %q: %v", base, i+1, rest, err)
			}
			current.consumed = consumed
			current.hasConsumed = true
		case "measure_at_least":
			floor, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				t.Fatalf("%s:%d: bad measure_at_least count %q: %v", base, i+1, rest, err)
			}
			current.measureAtLeast = floor
			current.hasMeasure = true
		case "writer":
			if rest != "canonical" {
				t.Fatalf("%s:%d: unknown writer mode %q", base, i+1, rest)
			}
			current.writerCanonical = true
		default:
			t.Fatalf("%s:%d: unknown key %q", base, i+1, key)
		}
	}
	flush()
	return vectors
}

// parseVectorNumber reads a vector's number, which STANDARD.md's lexical rules make signed
// decimal or 0x hexadecimal, up to 128 bits wide. The result is the value's two's
// complement 128 bit pattern, so a hexadecimal expectation and its decimal twin are one
// expectation.
func parseVectorNumber(text string) (Uint128, bool) {
	text = strings.TrimSpace(text)
	negative := false
	switch {
	case strings.HasPrefix(text, "-"):
		negative = true
		text = text[1:]
	case strings.HasPrefix(text, "+"):
		text = text[1:]
	}
	if text == "" {
		return Uint128{}, false
	}
	base := 10
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		base = 16
		text = text[2:]
		if text == "" {
			return Uint128{}, false
		}
	}
	value, ok := new(big.Int).SetString(text, base)
	if !ok || value.Sign() < 0 {
		return Uint128{}, false
	}
	if value.BitLen() > 128 {
		return Uint128{}, false
	}
	pattern := uint128FromBig(value)
	if negative {
		pattern = Uint128{}.Sub(pattern)
	}
	return pattern, true
}

// --------------------------------------------------------------------------------------
// steps
//
// The step machine drives both the single operation files and the sequence files: a single
// operation vector is a one or two step sequence built from the record's own parameters, so
// there is exactly one execution path and the sequence files cannot drift away from the
// operation files.

type stepKind int

const (
	stepBits stepKind = iota
	stepBool
	stepUint128
	stepAlign
	stepInt
	stepInt64
	stepInt128
	stepIntRelative
	stepFloat
	stepDouble
	stepCompressedFloat
	stepBytes
	stepString
	stepWideString
	stepFixed
	stepObject // opens a nested object over the steps that follow
)

type step struct {
	kind         stepKind
	width        int64 // bits, count, buffer_size, or the number of steps an object wraps
	min, max     Uint128
	integerBits  int
	fractionBits int
	fmin         float32
	fmax         float32
	fres         float32
	previous     int32

	// outputs
	pattern Uint128 // every numeric kind's value, as its two's complement 128 bit pattern
	boolean bool
	data    []byte
	text    string
}

// valueIsNumeric reports whether a step's value is one of the numeric kinds, which are the
// scalars "a refused primitive read must leave its destination unwritten" reaches and the
// ones compared as 128 bit patterns.
func (s *step) valueIsNumeric() bool {
	switch s.kind {
	case stepBits, stepUint128, stepFloat, stepDouble, stepCompressedFloat,
		stepInt, stepInt64, stepInt128, stepIntRelative, stepFixed:
		return true
	}
	return false
}

func (s *step) int128() Int128 { return s.pattern.Int128() }

// run drives one step against any stream: the read stream, the write stream or the measure
// stream. The value travels in and out through the step, so the writer and the measure legs
// re-serialize exactly what the reader decoded.
func (s *step) run(stream Stream) error {
	switch s.kind {
	case stepBits:
		value := s.pattern.Lo
		err := stream.SerializeBits64(&value, int(s.width))
		s.pattern = Uint128{Lo: value}
		return err

	case stepBool:
		return stream.SerializeBool(&s.boolean)

	case stepUint128:
		value := s.pattern
		err := stream.SerializeUint128(&value)
		s.pattern = value
		return err

	case stepAlign:
		return stream.SerializeAlign()

	case stepInt:
		value := int32(s.pattern.Lo)
		err := stream.SerializeInt(&value, int32(s.min.Lo), int32(s.max.Lo))
		s.pattern = Int128From64(int64(value)).Uint128()
		return err

	case stepInt64:
		value := int64(s.pattern.Lo)
		err := stream.SerializeInt64(&value, int64(s.min.Lo), int64(s.max.Lo))
		s.pattern = Int128From64(value).Uint128()
		return err

	case stepInt128:
		value := s.int128()
		err := stream.SerializeInt128(&value, s.min.Int128(), s.max.Int128())
		s.pattern = value.Uint128()
		return err

	case stepIntRelative:
		value := int32(s.pattern.Lo)
		err := stream.SerializeIntRelative(s.previous, &value)
		s.pattern = Int128From64(int64(value)).Uint128()
		return err

	case stepFloat:
		value := math.Float32frombits(uint32(s.pattern.Lo))
		err := stream.SerializeFloat32(&value)
		s.pattern = Uint128{Lo: uint64(math.Float32bits(value))}
		return err

	case stepDouble:
		value := math.Float64frombits(s.pattern.Lo)
		err := stream.SerializeFloat64(&value)
		s.pattern = Uint128{Lo: math.Float64bits(value)}
		return err

	case stepCompressedFloat:
		value := math.Float32frombits(uint32(s.pattern.Lo))
		err := stream.SerializeCompressedFloat32(&value, s.fmin, s.fmax, s.fres)
		s.pattern = Uint128{Lo: uint64(math.Float32bits(value))}
		return err

	case stepBytes:
		if int64(len(s.data)) != s.width {
			s.data = make([]byte, s.width)
		}
		return stream.SerializeBytes(s.data)

	case stepString:
		return stream.SerializeString(&s.text, int(s.width))

	case stepWideString:
		return stream.SerializeWideString(&s.text, int(s.width))

	case stepFixed:
		if s.integerBits+s.fractionBits <= 64 {
			value := int64(s.pattern.Lo)
			err := stream.SerializeFixed64(&value, s.integerBits, s.fractionBits, int64(s.min.Lo), int64(s.max.Lo))
			s.pattern = Int128From64(value).Uint128()
			return err
		}
		value := s.int128()
		err := stream.SerializeFixed128(&value, s.integerBits, s.fractionBits, int64(s.min.Lo), int64(s.max.Lo))
		s.pattern = value.Uint128()
		return err
	}
	return fmt.Errorf("no runner for step kind %d", s.kind)
}

// stepSpan advances past the steps a nested object owns, so a walk over the top level sees
// one step per object.
func stepSpan(steps []step, i int) int {
	if steps[i].kind == stepObject {
		return 1 + int(steps[i].width)
	}
	return 1
}

// nestedObject is the Serializer a `object <n>` step wraps its successors in. STANDARD.md:
// serialize_object invokes the object's own serialize function inline and contributes no
// bytes of its own, so the nested vectors and their flat twins must produce identical
// bytes.
type nestedObject struct {
	steps []step
	run   *runState
}

func (o *nestedObject) Serialize(stream Stream) error {
	_, err := o.run.runSteps(stream, o.steps)
	return err
}

// runState carries the step a run stopped on, which is what the destination check needs.
type runState struct {
	failed *step
}

// runSteps runs a step list against a stream, returning the index of the top level step the
// run stopped on, or len(steps) when every step succeeded.
func (r *runState) runSteps(stream Stream, steps []step) (int, error) {
	for i := 0; i < len(steps); i += stepSpan(steps, i) {
		if steps[i].kind == stepObject {
			object := &nestedObject{steps: steps[i+1 : i+1+int(steps[i].width)], run: r}
			if err := stream.SerializeObject(object); err != nil {
				return i, err
			}
			continue
		}
		if err := steps[i].run(stream); err != nil {
			r.failed = &steps[i]
			return i, err
		}
	}
	return len(steps), nil
}

// --------------------------------------------------------------------------------------
// building steps

// stepFromWords builds one step from a sequence file's `param step = ` spelling.
func stepFromWords(text string) (step, error) {
	var s step
	words := strings.Fields(text)
	if len(words) == 0 {
		return s, fmt.Errorf("empty step")
	}
	number := func(i int) (Uint128, error) {
		value, ok := parseVectorNumber(words[i])
		if !ok {
			return Uint128{}, fmt.Errorf("step %q: %q is not a number", text, words[i])
		}
		return value, nil
	}
	switch {
	case words[0] == "bool" && len(words) == 1:
		s.kind = stepBool
	case words[0] == "align" && len(words) == 1:
		s.kind = stepAlign
	case words[0] == "float" && len(words) == 1:
		s.kind = stepFloat
	case words[0] == "double" && len(words) == 1:
		s.kind = stepDouble
	case words[0] == "uint128" && len(words) == 1:
		s.kind = stepUint128
	case words[0] == "int_relative" && len(words) == 2:
		previous, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.previous = stepIntRelative, int32(previous.Lo)
	case words[0] == "compressed_float" && len(words) == 4:
		var bounds [3]float32
		for i := range bounds {
			value, err := strconv.ParseFloat(words[i+1], 32)
			if err != nil {
				return s, fmt.Errorf("step %q: %q is not a float32: %w", text, words[i+1], err)
			}
			bounds[i] = float32(value)
		}
		s.kind, s.fmin, s.fmax, s.fres = stepCompressedFloat, bounds[0], bounds[1], bounds[2]
	case words[0] == "bits" && len(words) == 2:
		width, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.width = stepBits, int64(width.Lo)
	case words[0] == "object" && len(words) == 2:
		width, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.width = stepObject, int64(width.Lo)
	case words[0] == "bytes" && len(words) == 2:
		width, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.width = stepBytes, int64(width.Lo)
	case words[0] == "string" && len(words) == 2:
		width, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.width = stepString, int64(width.Lo)
	case words[0] == "wstring" && len(words) == 2:
		width, err := number(1)
		if err != nil {
			return s, err
		}
		s.kind, s.width = stepWideString, int64(width.Lo)
	case (words[0] == "int" || words[0] == "int64" || words[0] == "int128") && len(words) == 3:
		min, err := number(1)
		if err != nil {
			return s, err
		}
		max, err := number(2)
		if err != nil {
			return s, err
		}
		switch words[0] {
		case "int":
			s.kind = stepInt
		case "int64":
			s.kind = stepInt64
		default:
			s.kind = stepInt128
		}
		s.min, s.max = min, max
	case words[0] == "fixed" && len(words) == 5:
		integerBits, err := number(1)
		if err != nil {
			return s, err
		}
		fractionBits, err := number(2)
		if err != nil {
			return s, err
		}
		min, err := number(3)
		if err != nil {
			return s, err
		}
		max, err := number(4)
		if err != nil {
			return s, err
		}
		s.kind = stepFixed
		s.integerBits, s.fractionBits = int(integerBits.Lo), int(fractionBits.Lo)
		s.min, s.max = min, max
	default:
		return s, fmt.Errorf("no runner for step %q", text)
	}
	return s, nil
}

// operationTakesParam states the parameters each operation consumes. A parameter this
// runner does not understand is a failure rather than a silent default, because a vector
// whose declaration is not the one being exercised proves nothing.
func operationTakesParam(operation, name string) bool {
	switch name {
	case "preceding_bits":
		return operation == "align" || operation == "bytes"
	case "bits":
		return operation == "bits"
	case "count":
		return operation == "bytes"
	case "buffer_size":
		return operation == "string" || operation == "wstring"
	case "previous":
		return operation == "int_relative"
	case "res":
		return operation == "compressed_float"
	case "integer_bits", "fraction_bits":
		return operation == "fixed"
	case "min", "max":
		switch operation {
		case "int", "int64", "int128", "fixed", "compressed_float":
			return true
		}
	}
	return false
}

// buildSteps builds a vector's step list. The operations whose interesting behavior only
// exists at a non-zero bit index take a `preceding_bits` parameter, which becomes a leading
// bits step.
func buildSteps(vector conformanceVector) ([]step, error) {
	if vector.operation == "sequence" {
		if len(vector.stepText) == 0 {
			return nil, fmt.Errorf("a sequence states no steps")
		}
		steps := make([]step, 0, len(vector.stepText))
		for _, text := range vector.stepText {
			s, err := stepFromWords(text)
			if err != nil {
				return nil, err
			}
			steps = append(steps, s)
		}
		return steps, nil
	}
	if len(vector.stepText) > 0 {
		return nil, fmt.Errorf("steps are only meaningful on a sequence")
	}

	number := func(name string) (Uint128, error) {
		text, ok := vector.params[name]
		if !ok {
			return Uint128{}, fmt.Errorf("no %s parameter", name)
		}
		value, ok := parseVectorNumber(text)
		if !ok {
			return Uint128{}, fmt.Errorf("%s = %q is not a number", name, text)
		}
		return value, nil
	}
	float32Param := func(name string) (float32, error) {
		text, ok := vector.params[name]
		if !ok {
			return 0, fmt.Errorf("no %s parameter", name)
		}
		value, err := strconv.ParseFloat(text, 32)
		if err != nil {
			return 0, fmt.Errorf("%s = %q is not a float32: %w", name, text, err)
		}
		return float32(value), nil
	}
	minMax := func(s *step) error {
		min, err := number("min")
		if err != nil {
			return err
		}
		max, err := number("max")
		if err != nil {
			return err
		}
		s.min, s.max = min, max
		return nil
	}

	var steps []step
	if text, ok := vector.params["preceding_bits"]; ok {
		width, ok := parseVectorNumber(text)
		if !ok {
			return nil, fmt.Errorf("preceding_bits = %q is not a number", text)
		}
		if width.Lo > 0 {
			steps = append(steps, step{kind: stepBits, width: int64(width.Lo)})
		}
	}

	var s step
	switch vector.operation {
	case "bits":
		width, err := number("bits")
		if err != nil {
			return nil, err
		}
		s.kind, s.width = stepBits, int64(width.Lo)
	case "bool":
		s.kind = stepBool
	case "uint128":
		s.kind = stepUint128
	case "align":
		s.kind = stepAlign
	case "float":
		s.kind = stepFloat
	case "double":
		s.kind = stepDouble
	case "int":
		s.kind = stepInt
		if err := minMax(&s); err != nil {
			return nil, err
		}
	case "int64":
		s.kind = stepInt64
		if err := minMax(&s); err != nil {
			return nil, err
		}
	case "int128":
		s.kind = stepInt128
		if err := minMax(&s); err != nil {
			return nil, err
		}
	case "int_relative":
		previous, err := number("previous")
		if err != nil {
			return nil, err
		}
		s.kind, s.previous = stepIntRelative, int32(previous.Lo)
	case "compressed_float":
		min, err := float32Param("min")
		if err != nil {
			return nil, err
		}
		max, err := float32Param("max")
		if err != nil {
			return nil, err
		}
		res, err := float32Param("res")
		if err != nil {
			return nil, err
		}
		s.kind, s.fmin, s.fmax, s.fres = stepCompressedFloat, min, max, res
	case "bytes":
		count, err := number("count")
		if err != nil {
			return nil, err
		}
		s.kind, s.width = stepBytes, int64(count.Lo)
	case "string":
		size, err := number("buffer_size")
		if err != nil {
			return nil, err
		}
		s.kind, s.width = stepString, int64(size.Lo)
	case "wstring":
		size, err := number("buffer_size")
		if err != nil {
			return nil, err
		}
		s.kind, s.width = stepWideString, int64(size.Lo)
	case "fixed":
		integerBits, err := number("integer_bits")
		if err != nil {
			return nil, err
		}
		fractionBits, err := number("fraction_bits")
		if err != nil {
			return nil, err
		}
		s.kind = stepFixed
		s.integerBits, s.fractionBits = int(integerBits.Lo), int(fractionBits.Lo)
		if err := minMax(&s); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("no runner for operation %q", vector.operation)
	}
	return append(steps, s), nil
}

// --------------------------------------------------------------------------------------
// expectations

func hexBytes(data []byte) string {
	parts := make([]string, len(data))
	for i, b := range data {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, " ")
}

// hexCodeUnits renders a decoded wide string as the UTF-16 code units the corpus states.
// STANDARD.md, "wstring": each 32 bit group carries one UTF-16 code unit, and a runtime
// whose strings are not UTF-16 recombines surrogate pairs into code points on read. Go
// strings are UTF-8, so a recombined astral code point is split again here and both
// platforms compare the same text.
func hexCodeUnits(text string) string {
	parts := make([]string, 0, len(text))
	for _, r := range text {
		if r > 0xFFFF {
			high, low := utf16.EncodeRune(r)
			parts = append(parts, fmt.Sprintf("%04X", uint16(high)), fmt.Sprintf("%04X", uint16(low)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%04X", uint16(r)))
	}
	return strings.Join(parts, " ")
}

// renderStepValue renders a step's decoded value the way the corpus states it.
func renderStepValue(s *step) string {
	if s.valueIsNumeric() {
		return fmt.Sprintf("0x%016X%016X", s.pattern.Hi, s.pattern.Lo)
	}
	switch s.kind {
	case stepBool:
		return strconv.FormatBool(s.boolean)
	case stepBytes:
		return hexBytes(s.data)
	case stepString:
		return hexBytes([]byte(s.text))
	case stepWideString:
		return hexCodeUnits(s.text)
	case stepAlign:
		// align has no value of its own, and the corpus states the padding it consumed,
		// which a conforming read always finds zero
		return "0"
	}
	return "-"
}

// expectationMatches compares a step's decoded value against the corpus entry. Every
// numeric kind is compared as a 128 bit PATTERN and nothing here goes through a float:
// STANDARD.md requires float and double vectors to compare bit patterns and not values,
// because NaN compares unequal to itself, -0.0 equals 0.0, and a tolerance comparison
// cannot see a quieted signaling bit.
func expectationMatches(s *step, expected string) bool {
	if s.valueIsNumeric() {
		wanted, ok := parseVectorNumber(expected)
		if !ok {
			return false
		}
		return s.pattern == wanted
	}
	return renderStepValue(s) == expected
}

func splitExpect(text string) []string {
	parts := strings.Split(text, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// --------------------------------------------------------------------------------------
// running a vector

// slackFill is non-zero, so a decode that strays past the end of a stream cannot pass by
// reading zeros.
const slackFill = 0xA5

// newConformanceReadStream presents a vector's bytes as a stream carrying the slack behind
// them that STANDARD.md's harness rule requires, filled with a non-zero pattern.
func newConformanceReadStream(data []byte, slack int) *ReadStream {
	backing := make([]byte, len(data)+slack)
	for i := range backing {
		backing[i] = slackFill
	}
	copy(backing, data)
	return NewReadStream(backing[:len(data)])
}

// checkTerminal is the terminal-failure obligation, checked by behavior rather than by an
// accessor so the same check ports to every implementation in the family: a further read on
// a stream that has refused must also fail, consume no bits and write nothing.
func checkTerminal(t *testing.T, vector conformanceVector, stream *ReadStream) {
	t.Helper()
	after := uint32(0xFFFFFFFF)
	before := stream.BitsProcessed()
	if err := stream.SerializeBits(&after, 8); err == nil {
		t.Fatalf("%s: the stream accepted a read after the refusal, failure is not terminal", vector.where())
	}
	if after != 0xFFFFFFFF {
		t.Fatalf("%s: the read after the refusal wrote to its destination", vector.where())
	}
	if stream.BitsProcessed() != before {
		t.Fatalf("%s: the read after the refusal consumed bits", vector.where())
	}
}

// The sentinels seed every scalar destination, and they must survive the narrowing this
// runner performs on the way to each operation's own width, or a destination the library
// correctly left alone still reads as written. A bit pattern kind takes one that fits 32
// bits, and a number kind takes a small negative whose sign extension is stable at every
// width the runner narrows to.
var (
	sentinelPattern = Uint128{Lo: 0xCAFEF00D}
	sentinelNumber  = Int128From64(-1234567).Uint128()
)

// sentinel is the value seeded into a step's destination before a read, and the value a
// refused read must leave behind.
func sentinel(kind stepKind) Uint128 {
	switch kind {
	case stepInt, stepInt64, stepInt128, stepIntRelative, stepFixed:
		return sentinelNumber
	}
	return sentinelPattern
}

func seedSentinels(steps []step) {
	for i := range steps {
		steps[i].pattern = sentinel(steps[i].kind)
		steps[i].boolean = true
		steps[i].data = nil
		steps[i].text = ""
	}
}

func runReader(t *testing.T, vector conformanceVector, steps []step, slack int) {
	t.Helper()
	seedSentinels(steps)
	stream := newConformanceReadStream(vector.bytes, slack)

	state := &runState{}
	stoppedAt, err := state.runSteps(stream, steps)

	if vector.expectKind == expectRefused {
		if err == nil {
			t.Fatalf("%s: the read succeeded, the corpus requires refusal", vector.where())
		}

		// STANDARD.md, "A refused primitive read must leave its destination unwritten".
		// The rule reaches the scalars only: a read into a caller-owned buffer, which is
		// bytes, string and wstring, leaves that buffer's contents unspecified after a
		// refusal, so those kinds are not checked here.
		if s := state.failed; s != nil {
			if s.valueIsNumeric() && s.pattern != sentinel(s.kind) {
				t.Fatalf("%s: the refused read wrote to the destination", vector.where())
			}
			if s.kind == stepBool && !s.boolean {
				t.Fatalf("%s: the refused read wrote to the destination", vector.where())
			}
		}

		// Failure is terminal, and a sequence states its own successors: every step after
		// the failing one must fail too, however many readable bits the stream still
		// holds. The vectors are built so that a reader without the latch passes the
		// successor, and one of them makes the successor a degenerate range, a read that
		// consumes no bits and would otherwise always succeed.
		for i := stoppedAt + stepSpan(steps, stoppedAt); i < len(steps); i += stepSpan(steps, i) {
			span := stepSpan(steps, i)
			if _, err := state.runSteps(stream, steps[i:i+span]); err == nil {
				t.Fatalf("%s: step %d succeeded after step %d was refused, failure must be terminal",
					vector.where(), i+1, stoppedAt+1)
			}
		}

		// and the same rule against a read the vector does not name, so every refused
		// vector carries the terminality check and not only the sequences that spell a
		// successor
		checkTerminal(t, vector, stream)
		return
	}

	if err != nil {
		t.Fatalf("%s: the read was refused (%v), the corpus requires it to be accepted", vector.where(), err)
	}

	entries := splitExpect(vector.expect)
	// one expect entry per step, objects and aligns included, which state `-`. A leading
	// preceding_bits step carries no expectation of its own: it exists to place the
	// stream, and the record states only the operation under test.
	offset := len(steps) - len(entries)
	if offset < 0 {
		t.Fatalf("%s: the expect list states more values than the vector has steps", vector.where())
	}
	for i, entry := range entries {
		if entry == "-" {
			continue
		}
		if !expectationMatches(&steps[offset+i], entry) {
			t.Fatalf("%s: step %d decoded %s, the corpus states %s",
				vector.where(), offset+i+1, renderStepValue(&steps[offset+i]), entry)
		}
	}

	if vector.hasConsumed && stream.BitsProcessed() != vector.consumed {
		t.Fatalf("%s: consumed %d bits, the corpus states %d",
			vector.where(), stream.BitsProcessed(), vector.consumed)
	}
}

// runWriter is the writer leg. A vector marked `writer = canonical` states the bytes a
// conforming writer emits for its value, so the runner writes the decoded steps back and
// compares the whole stream. That is where the trailing-bits writer obligation bites: the
// unused bits of the final byte must be zero, and a writer leaking anything into them
// produces a byte the vector does not carry.
func runWriter(t *testing.T, vector conformanceVector, steps []step) {
	t.Helper()
	buffer := make([]byte, (len(vector.bytes)+64)/8*8+8)
	stream := NewWriteStream(buffer)
	state := &runState{}
	if _, err := state.runSteps(stream, steps); err != nil {
		t.Fatalf("%s: the writer refused a canonical vector: %v", vector.where(), err)
	}
	stream.Flush()
	emitted := stream.Data()
	if len(emitted) != len(vector.bytes) {
		t.Fatalf("%s: the writer emitted %d bytes, the corpus states %d",
			vector.where(), len(emitted), len(vector.bytes))
	}
	if hexBytes(emitted) != hexBytes(vector.bytes) {
		t.Fatalf("%s: the writer emitted %s, the corpus states %s",
			vector.where(), hexBytes(emitted), hexBytes(vector.bytes))
	}
}

// runMeasure is the measure leg. STANDARD.md makes a measure a BOUND and not the packet
// size, so the corpus states a floor and the check is an inequality. The exact-from-zero
// accounting the document calls non-conforming falls below it.
func runMeasure(t *testing.T, vector conformanceVector, steps []step) {
	t.Helper()
	stream := NewMeasureStream()
	state := &runState{}
	if _, err := state.runSteps(stream, steps); err != nil {
		t.Fatalf("%s: the measure refused a step, a measure refuses nothing at runtime: %v", vector.where(), err)
	}
	if stream.BitsProcessed() < vector.measureAtLeast {
		t.Fatalf("%s: measured %d bits, the corpus requires at least %d",
			vector.where(), stream.BitsProcessed(), vector.measureAtLeast)
	}
}

// TestConformanceCorpus runs every vector in the vendored corpus.
//
// Each vector runs its reader leg twice: once over a tight slice, which drives the reader's
// zero padded tail window, and once with backing slack, which drives the branchless window
// loads. The two paths are separate code in bitpacker.go and must agree on every vector.
func TestConformanceCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("conformance", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no conformance vectors found: the vendored conformance/ directory is missing")
	}

	total := 0
	writers := 0
	measures := 0
	for _, file := range files {
		for _, vector := range parseConformanceFile(t, file) {
			if vector.operation == "" || vector.name == "" {
				t.Fatalf("%s:%d: vector needs both an operation and a name", vector.file, vector.line)
			}
			total++
			if vector.writerCanonical {
				writers++
			}
			if vector.hasMeasure {
				measures++
			}
			t.Run(vector.operation+"/"+vector.name, func(t *testing.T) {
				for _, param := range vector.paramNames {
					if !operationTakesParam(vector.operation, param) {
						t.Fatalf("%s: no runner for parameter %q on operation %q",
							vector.where(), param, vector.operation)
					}
				}
				steps, err := buildSteps(vector)
				if err != nil {
					// a corpus file this runner cannot drive is a gap in the runner and
					// never a pass
					t.Fatalf("%s: %v", vector.where(), err)
				}

				for _, slack := range []int{0, 16} {
					name := "tight"
					if slack > 0 {
						name = "slack"
					}
					t.Run(name, func(t *testing.T) {
						runReader(t, vector, steps, slack)
					})
				}

				// the writer and the measure are handed the values the reader decoded, so
				// the reader leg runs first and a canonical vector's round trip is decode
				// then re-emit
				if vector.expectKind != expectRefused && !t.Failed() {
					if vector.writerCanonical {
						runWriter(t, vector, steps)
					}
					if vector.hasMeasure {
						runMeasure(t, vector, steps)
					}
				}
			})
		}
	}
	t.Logf("ran %d conformance vectors from %d files: %d writer checks, %d measure checks",
		total, len(files), writers, measures)
}
