package serialize

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// NEGATIVE CONTROLS. The corpus is the conformance instrument, and STANDARD.md, "The
// conformance corpus": a gate nobody has seen go red is a claim, not a measurement.
// Two kinds of control prove TestConformanceCorpus responds.
//
// The first is a vector whose operation has no runner, which the runner must reject
// rather than skip (TestConformanceControlUnknownOperation).
//
// The second is a reader with ONE refusal rule deleted, run against the real corpus,
// which must fail EXACTLY the vectors conformance-controls/<rule>.expected names and no
// others (TestConformanceControls). The lists are vendored verbatim from the C++
// repository, where its own sabotaged readers are held to them, so they are the oracle
// and never computed here. A sabotage that breaks more than its rule, or less, is a
// control that measures nothing.
//
// Each sabotage is a needle and its patch: the test copies this package to a scratch
// directory, replaces the needle (which must occur exactly once, so a needle that stops
// matching is a failure and never a silent no-op), and runs TestConformanceCorpus there
// under `go test -json`. Nothing of it ships: the library carries no sabotage switch.
//
// These are the reversed witnesses for every 2.0 rule: each one is the rule's old
// behavior, planted back.

// conformanceControl is one sabotaged reader: in file, needle becomes patch.
type conformanceControl struct {
	name   string
	file   string
	needle string
	patch  string
}

var conformanceControls = []conformanceControl{
	{
		name:   "align-padding",
		file:   "read_stream.go",
		needle: "\tif s.reader.readBits(alignBits) != 0 {",
		patch:  "\tif s.reader.readBits(alignBits) == 0xFFFFFFFF { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "ranged-reconstruction",
		file:   "read_stream.go",
		needle: "\t*value = int32(unsigned + uint32(min))",
		// The C++ reader takes a degenerate range from min on an arm of its own, which
		// its sabotaged line does not reach; this reader forms the degenerate value on
		// the same line, offset zero plus min. The patch keeps that arm intact, so the
		// deleted rule is the same rule over the same reads in both languages.
		patch: "\tif bits == 0 { *value = int32(unsigned + uint32(min)) } else { *value = int32(unsigned) } // NEGATIVE CONTROL: min is dropped on purpose",
	},
	{
		name:   "ranged-range",
		file:   "read_stream.go",
		needle: "\tif unsigned > uint32(max)-uint32(min) {",
		patch:  "\tif uint64(unsigned) > uint64(uint32(max)-uint32(min))+0xFFFFFFFF { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "fixed-degenerate-terminal",
		file:   "read_stream.go",
		needle: "\trawMin, rawRange, numBits := fixedPointParams64(integerBits, fractionBits, min, max)\n\tif s.err != nil {",
		patch:  "\trawMin, rawRange, numBits := fixedPointParams64(integerBits, fractionBits, min, max)\n\tif s.err != nil && numBits != 0 { // NEGATIVE CONTROL: a zero bit read skips the failure state on purpose",
	},
	{
		name:   "float-finite",
		file:   "read_stream.go",
		needle: "\tif !float32BitsAreFinite(v) {",
		patch:  "\tif !float32BitsAreFinite(v) && v == 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "double-finite",
		file:   "read_stream.go",
		needle: "\tif !float64BitsAreFinite(v) {",
		patch:  "\tif !float64BitsAreFinite(v) && v == 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "compressed-float-finite",
		file:   "read_stream.go",
		needle: "\tif !float32BitsAreFinite(math.Float32bits(decoded)) {",
		patch:  "\tif !float32BitsAreFinite(math.Float32bits(decoded)) && decoded == 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "end-trailing-data",
		file:   "read_stream.go",
		needle: "\tif remaining >= 8 {",
		patch:  "\tif remaining >= 8 && remaining < 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "end-padding",
		file:   "read_stream.go",
		needle: "\tif remaining > 0 && s.reader.readBits(int(remaining)) != 0 {",
		patch:  "\tif remaining > 0 && s.reader.readBits(int(remaining)) == 0xFFFFFFFF { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "int-relative-canonical",
		file:   "read_stream.go",
		needle: "absolute-int64(previous) <= intRelativeBoundedMax {",
		patch:  "absolute-int64(previous) <= intRelativeBoundedMax && absolute < 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "string-embedded-nul",
		file:   "read_stream.go",
		needle: "\tif bytes.IndexByte(data, 0) >= 0 {",
		patch:  "\tif bytes.IndexByte(data, 0) >= 0 && len(data) < 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "string-utf8",
		file:   "read_stream.go",
		needle: "\tif !utf8.Valid(data) {",
		patch:  "\tif !utf8.Valid(data) && len(data) < 0 { // NEGATIVE CONTROL: this rule is deleted on purpose",
	},
	{
		name:   "int64-through-double",
		file:   "read_stream.go",
		needle: "\t*value = int64(unsigned + uint64(min))",
		// the degenerate arm stays intact, exactly as for ranged-reconstruction
		patch: "\tif throughDouble := float64(int64(unsigned + uint64(min))); bits == 0 { *value = int64(unsigned + uint64(min)) } else if throughDouble >= 9223372036854775807.0 { *value = math.MaxInt64 } else { *value = int64(throughDouble) } // NEGATIVE CONTROL: the value goes through a double on purpose",
	},
	{
		name:   "wrong-reason",
		file:   "read_stream.go",
		needle: "\t\treturn s.fail(ErrEmbeddedNul)",
		patch:  "\t\treturn s.fail(ErrInvalidUTF8) // NEGATIVE CONTROL: the wrong reason on purpose",
	},
	{
		name: "object-terminal",
		file: "read_stream.go",
		// The rule is SerializeObject's entry check and its exit together: delete the entry
		// alone and the exit still returns the latch, so an empty object on a failed stream
		// refuses anyway and the sabotage deletes nothing. Both go, as one rule.
		needle: "\tif s.err != nil {\n\t\treturn s.err\n\t}\n\tif err := object.Serialize(s); err != nil {\n\t\tif s.err != nil {\n\t\t\treturn s.err\n\t\t}\n\t\treturn s.fail(rejection(err))\n\t}\n\treturn s.err\n}",
		patch:  "\tif err := object.Serialize(s); err != nil {\n\t\tif s.err != nil {\n\t\t\treturn s.err\n\t\t}\n\t\treturn s.fail(rejection(err))\n\t}\n\treturn nil // NEGATIVE CONTROL: a failed stream runs the callback on purpose\n}",
	},
	{
		// 2.1: the 2.0 latch, which recorded the caller's own error and so named no
		// reason. The stream is still terminal, so what goes red is the reason.
		name:   "object-latch-rejected",
		file:   "read_stream.go",
		needle: "\t\treturn s.fail(rejection(err))",
		patch:  "\t\treturn s.fail(err) // NEGATIVE CONTROL: the 2.0 latch, recording no reason, on purpose",
	},
	{
		// 2.1: Reject refuses under a wire rule's name. The stream still fails and stays
		// failed; only the reason is wrong.
		name:   "reject-reason",
		file:   "read_stream.go",
		needle: "func (s *ReadStream) Reject() error {\n\treturn s.fail(ErrRejected)",
		patch:  "func (s *ReadStream) Reject() error {\n\treturn s.fail(ErrValueOutOfRange) // NEGATIVE CONTROL: the wrong reason on purpose",
	},
}

// controlsNotApplicable are the vendored lists this port cannot hold a sabotage to,
// each with the reason. A list in conformance-controls/ that is neither controlled above
// nor named here fails TestConformanceControls.
var controlsNotApplicable = map[string]string{
	// The C++ reader validates UTF-8 with its own decoder, so one line of it is the
	// encoded surrogate rule alone. This port validates with the standard library's
	// utf8.Valid, which refuses encoded surrogates inside a function this repository
	// does not own, so the surrogate rule cannot be deleted on its own here. The
	// string-utf8 control deletes the whole validator, and its list includes both
	// surrogate vectors, so they are still seen to go red.
	"string-utf8-surrogate": "utf8.Valid owns the surrogate rule: covered by string-utf8",
}

// readExpectedList reads a conformance-controls/<rule>.expected file: one vector name
// per line, '#' comments and blank lines skipped.
func readExpectedList(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	if len(names) == 0 {
		t.Fatalf("%s names no vectors", path)
	}
	slices.Sort(names)
	return names
}

// copyPackageForControl copies the package's Go sources, go.mod and the corpus into dir:
// everything TestConformanceCorpus needs, and nothing that could reach outside it.
func copyPackageForControl(t *testing.T, dir string) {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := filepath.Glob(filepath.Join(corpusRoot, "conformance", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "conformance"), 0o755); err != nil {
		t.Fatal(err)
	}
	copies := map[string]string{"go.mod": "go.mod"}
	for _, path := range sources {
		copies[path] = path
	}
	for _, path := range vectors {
		copies[path] = filepath.Join("conformance", filepath.Base(path))
	}
	for from, to := range copies {
		content, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, to), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// failingVectors runs TestConformanceCorpus in dir and returns the names of the vectors
// that failed, read from `go test -json`: a vector is the third element of its subtest
// path, TestConformanceCorpus/<operation>/<name>/<leg>.
func failingVectors(t *testing.T, goTool, dir string) []string {
	t.Helper()
	command := exec.Command(goTool, "test", "-count=1", "-json", "-run", "^TestConformanceCorpus$", ".")
	command.Dir = dir
	// the scratch module has no dependencies and must never look for any: no proxy, no
	// toolchain download, no workspace from above the scratch directory
	// and it reads the corpus copied into it, whatever root the outer run was pointed at
	command.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=", "SERIALIZE_CONFORMANCE_ROOT=")
	output, _ := command.CombinedOutput() // a sabotaged corpus run exits non-zero by design

	type event struct {
		Action string
		Test   string
		Output string
	}
	failed := map[string]bool{}
	sawCorpus := false
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		if strings.Contains(e.Output, "panic: ") {
			t.Fatalf("the sabotaged reader panicked, which no refusal rule's deletion licenses:\n%s", output)
		}
		parts := strings.Split(e.Test, "/")
		if parts[0] == "TestConformanceCorpus" && e.Action == "pass" && len(parts) == 1 {
			sawCorpus = true
		}
		if parts[0] == "TestConformanceCorpus" && e.Action == "fail" {
			sawCorpus = true
			if len(parts) >= 3 {
				failed[parts[2]] = true
			}
		}
	}
	if !sawCorpus {
		t.Fatalf("TestConformanceCorpus did not run in the scratch copy:\n%s", output)
	}
	names := make([]string, 0, len(failed))
	for name := range failed {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestConformanceControls runs every sabotaged reader against the corpus and requires
// its failing vectors to be exactly the vendored list for its rule.
//
// It compiles a scratch copy of the package once per control, so it runs in the full
// suite and is skipped under -short, which is the leg that also carries -race.
func TestConformanceControls(t *testing.T) {
	if testing.Short() {
		t.Skip("the sabotage controls compile a scratch copy of the package per rule: run without -short")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the controls need the go command: %v", err)
	}

	// every vendored list is controlled here or named as not applicable, with a reason
	lists, err := filepath.Glob(filepath.Join(corpusRoot, "conformance-controls", "*.expected"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) == 0 {
		t.Fatalf("no *.expected lists found: %s is missing", filepath.Join(corpusRoot, "conformance-controls"))
	}
	controlled := map[string]bool{}
	for _, control := range conformanceControls {
		controlled[control.name] = true
	}
	for _, list := range lists {
		name := strings.TrimSuffix(filepath.Base(list), ".expected")
		if !controlled[name] && controlsNotApplicable[name] == "" {
			t.Errorf("conformance-controls/%s.expected has no control here and no stated reason it cannot have one", name)
		}
	}

	for _, control := range conformanceControls {
		t.Run(control.name, func(t *testing.T) {
			t.Parallel()
			expected := readExpectedList(t, filepath.Join(corpusRoot, "conformance-controls", control.name+".expected"))

			dir := t.TempDir()
			copyPackageForControl(t, dir)
			path := filepath.Join(dir, control.file)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// a windows checkout may carry CRLF line endings, and a needle spans lines
			source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
			if n := strings.Count(string(source), control.needle); n != 1 {
				t.Fatalf("the needle occurs %d times in %s, want exactly once: a control that stopped matching sabotages nothing\nneedle: %q",
					n, control.file, control.needle)
			}
			patched := strings.Replace(string(source), control.needle, control.patch, 1)
			if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
				t.Fatal(err)
			}

			failed := failingVectors(t, goTool, dir)
			if !slices.Equal(failed, expected) {
				var missing, extra []string
				for _, name := range expected {
					if !slices.Contains(failed, name) {
						missing = append(missing, name)
					}
				}
				for _, name := range failed {
					if !slices.Contains(expected, name) {
						extra = append(extra, name)
					}
				}
				t.Fatalf("the sabotaged reader failed %d vectors, the list names %d\nnamed but not failed: %v\nfailed but not named: %v",
					len(failed), len(expected), missing, extra)
			}
		})
	}
}

// TestConformanceControlUnknownOperation is the first control: a corpus file whose
// operation this runner has no code for. STANDARD.md requires such a vector to be red
// rather than skipped, so the runner must refuse to build it.
func TestConformanceControlUnknownOperation(t *testing.T) {
	vectors := parseConformanceFile(t, filepath.Join(corpusRoot, "conformance-controls", "unknown-operation.txt"))
	if len(vectors) == 0 {
		t.Fatal("conformance-controls/unknown-operation.txt holds no vector")
	}
	for _, vector := range vectors {
		if _, err := vectorSteps(vector); err == nil {
			t.Fatalf("%s: the runner built steps for an operation it has no code for", vector.where())
		}
	}

	// and the operation 2.0 removed is unknown too: a wstring vector, from the 1.1
	// corpus, must be red and never quietly run
	removed := conformanceVector{file: "control", operation: "wstring", name: "removed-operation",
		paramNames: []string{"buffer_size"}, params: map[string]string{"buffer_size": "8"}}
	if _, err := vectorSteps(removed); err == nil {
		t.Fatal("the runner built steps for wstring, which format 2.0 removed")
	}
	if _, err := stepFromWords("wstring 8"); err == nil {
		t.Fatal("the runner built the step `wstring 8`, which format 2.0 removed")
	}
}
