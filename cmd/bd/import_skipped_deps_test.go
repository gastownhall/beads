package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// renderImportOutcomeStreams runs renderImportOutcome with jsonOutput and
// --strict-deps set as given, capturing stdout and stderr separately so a test can tell which
// stream each line of the report went to.
func renderImportOutcomeStreams(t *testing.T, asJSON, strict bool, result importResultJSON) (stdout, stderr string, err error) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldJSON := jsonOutput
	jsonOutput = asJSON
	defer func() { jsonOutput = oldJSON }()
	oldStrict := importStrictDeps
	importStrictDeps = strict
	defer func() { importStrictDeps = oldStrict }()

	pipe := func() (*os.File, *os.File, chan string) {
		r, w, perr := os.Pipe()
		if perr != nil {
			t.Fatalf("os.Pipe: %v", perr)
		}
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			_ = r.Close()
			done <- buf.String()
		}()
		return r, w, done
	}
	_, outW, outDone := pipe()
	_, errW, errDone := pipe()

	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()
		err = renderImportOutcome(result, "chunk.jsonl", 0)
	}()
	_ = outW.Close()
	_ = errW.Close()
	return <-outDone, <-errDone, err
}

// decodeSingleJSONObject decodes stdout as exactly one JSON object: a second
// object (an error envelope appended after the result) fails the test.
func decodeSingleJSONObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\nstdout:\n%s", err, stdout)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout carries more than one JSON value (next: %s, err %v)\nstdout:\n%s", extra, err, stdout)
	}
	return obj
}

// TestRenderImportOutcomeSkippedDependencies pins what a completed import
// reports about dependency edges it skipped (GH#6113): by default the
// per-edge lines, a closing count and recovery hint in text mode, an
// unchanged single JSON result object in --json mode, and exit status 0;
// under --strict-deps the same report followed by an exit-1 error.
func TestRenderImportOutcomeSkippedDependencies(t *testing.T) {
	skipped := []string{
		"imp-a -> imp-b: target not found",
		"imp-c -> imp-d: dependency would create a cycle",
	}

	t.Run("text_reports_count_and_hint", func(t *testing.T) {
		stdout, stderr, err := renderImportOutcomeStreams(t, false, false, importResultJSON{
			Source: "chunk.jsonl", Created: 2, SkippedDependencies: skipped,
		})
		if err != nil {
			t.Fatalf("renderImportOutcome = %v, want nil (exit 0)", err)
		}
		if stdout != "" {
			t.Errorf("text mode wrote stdout %q, want nothing", stdout)
		}
		for _, edge := range skipped {
			if !strings.Contains(stderr, "Skipped dependency: "+edge+"\n") {
				t.Errorf("stderr lacks the per-edge line for %q:\n%s", edge, stderr)
			}
		}
		if got := strings.Count(stderr, "Warning: 2 dependency edge(s) were reported skipped\n"); got != 1 {
			t.Errorf("stderr carries the skipped-count summary %d times, want 1:\n%s", got, stderr)
		}
		if got := strings.Count(stderr, "Hint: "); got != 1 {
			t.Errorf("stderr carries %d hint lines, want 1:\n%s", got, stderr)
		}
		if !strings.Contains(stderr, "re-run") {
			t.Errorf("hint does not tell the user to re-run the import:\n%s", stderr)
		}
		if !strings.Contains(stderr, "--strict-deps") {
			t.Errorf("hint does not name --strict-deps:\n%s", stderr)
		}
	})

	t.Run("strict_text_fails_after_reporting", func(t *testing.T) {
		stdout, stderr, err := renderImportOutcomeStreams(t, false, true, importResultJSON{
			Source: "chunk.jsonl", Created: 2, SkippedDependencies: skipped,
		})
		requireImportExitCode(t, err, 1)
		if stdout != "" {
			t.Errorf("text mode wrote stdout %q, want nothing", stdout)
		}
		if !strings.Contains(stderr, "Imported 2 issues from chunk.jsonl") {
			t.Errorf("stderr lacks the import report:\n%s", stderr)
		}
		for _, edge := range skipped {
			if !strings.Contains(stderr, "Skipped dependency: "+edge+"\n") {
				t.Errorf("stderr lacks the per-edge line for %q:\n%s", edge, stderr)
			}
		}
		if got := strings.Count(stderr, "Error: --strict-deps: 2 dependency edge(s) were reported skipped; imported rows remain committed\n"); got != 1 {
			t.Errorf("stderr carries the strict-deps error %d times, want 1:\n%s", got, stderr)
		}
		if strings.Contains(stderr, "Warning:") {
			t.Errorf("strict mode also printed the default warning:\n%s", stderr)
		}
		if got := strings.Count(stderr, "Hint: "); got != 1 {
			t.Errorf("stderr carries %d hint lines, want 1:\n%s", got, stderr)
		}
	})

	t.Run("json_keeps_one_result_object", func(t *testing.T) {
		stdout, stderr, err := renderImportOutcomeStreams(t, true, false, importResultJSON{
			Source: "chunk.jsonl", Created: 2, SkippedDependencies: skipped,
		})
		if err != nil {
			t.Fatalf("renderImportOutcome = %v, want nil (exit 0)", err)
		}
		if stderr != "" {
			t.Errorf("json mode wrote stderr %q, want nothing", stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		got, _ := obj["skipped_dependencies"].([]any)
		if len(got) != len(skipped) {
			t.Errorf("skipped_dependencies = %v, want %d entries", obj["skipped_dependencies"], len(skipped))
		}
		want := map[string]bool{"source": true, "created": true, "skipped": true, "skipped_dependencies": true, "schema_version": true}
		for key := range obj {
			if !want[key] {
				t.Errorf("result object gained field %q", key)
			}
		}
	})

	t.Run("text_without_skips_adds_nothing", func(t *testing.T) {
		_, stderr, err := renderImportOutcomeStreams(t, false, false, importResultJSON{Source: "chunk.jsonl", Created: 1})
		if err != nil {
			t.Fatalf("renderImportOutcome = %v, want nil", err)
		}
		if strings.Contains(stderr, "Warning:") || strings.Contains(stderr, "Hint:") || strings.Contains(stderr, "dependency") {
			t.Errorf("clean import reported skipped dependencies:\n%s", stderr)
		}
	})
}

// requireImportExitCode fails unless err is the exitError runImport passes
// through with the given code.
func requireImportExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("renderImportOutcome = %v (%T), want *exitError with code %d", err, err, code)
	}
	if exitErr.Code != code {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, code)
	}
}
