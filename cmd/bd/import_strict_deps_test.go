package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureImportOutcome swaps os.Stdout and os.Stderr for files, runs run, and
// returns what each stream collected plus run's error. renderImportOutcome
// writes the JSON result to stdout and the text report to stderr, so the
// strict-deps verdict's stream contract needs both captured separately.
func captureImportOutcome(t *testing.T, run func() error) (stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "stdout")
	errPath := filepath.Join(dir, "stderr")
	outF, oErr := os.Create(outPath)
	if oErr != nil {
		t.Fatalf("create stdout capture: %v", oErr)
	}
	errF, eErr := os.Create(errPath)
	if eErr != nil {
		outF.Close()
		t.Fatalf("create stderr capture: %v", eErr)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
		outF.Close()
		errF.Close()
	}()
	runErr := run()
	outBytes, rErr := os.ReadFile(outPath)
	if rErr != nil {
		t.Fatalf("read captured stdout: %v", rErr)
	}
	errBytes, rErr := os.ReadFile(errPath)
	if rErr != nil {
		t.Fatalf("read captured stderr: %v", rErr)
	}
	return string(outBytes), string(errBytes), runErr
}

// decodeSingleJSONObject decodes exactly one JSON object from s and fails the
// test if a second value follows: a strict-deps JSON failure must not append a
// second object to stdout (GH#6113's JSON contract keeps stdout one result).
func decodeSingleJSONObject(t *testing.T, name, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("%s does not hold one JSON object: %v\n%s", name, err, s)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Errorf("%s holds more than one JSON object; the result must be the only stdout value", name)
	}
	return obj
}

// TestRenderImportOutcomeSkippedDependencies pins the skipped-dependency signal
// of a completed import at the shared reporting seam (renderImportOutcome is
// where both the classic and the proxied import paths report, so one table
// covers both routes). GH#6113: an import that drops dependency edges must say
// so in aggregate — a count-only summary and a recovery hint by default, an
// exit-1 error under --strict-deps — without changing the JSON result shape.
// Store-free: renderImportOutcome reads only the result and the mode globals.
func TestRenderImportOutcomeSkippedDependencies(t *testing.T) {
	skips := []string{
		"zz-aaa -> zz-bbb: target not found",
		"zz-ccc -> zz-aaa: dependency validation failed: cycle detected",
	}

	oldJSON, oldStrict := jsonOutput, importStrictDeps
	defer func() { jsonOutput, importStrictDeps = oldJSON, oldStrict }()

	t.Run("text_default_summarizes_and_hints", func(t *testing.T) {
		jsonOutput = false
		var stderr string
		var err error
		_, stderr, err = captureImportOutcome(t, func() error {
			return renderImportOutcome(importResultJSON{Source: "chunk.jsonl", Created: 1, SkippedDependencies: skips}, "chunk.jsonl", 0)
		})
		if err != nil {
			t.Fatalf("default text import with skipped edges must still exit clean, got %v", err)
		}
		if !strings.Contains(stderr, "Skipped dependency: zz-aaa -> zz-bbb: target not found") {
			t.Errorf("per-edge report was dropped from stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Warning: 2 dependency edge(s) were reported skipped") {
			t.Errorf("missing aggregate skipped-edge summary in stderr:\n%s", stderr)
		}
		if n := strings.Count(stderr, "Hint:"); n != 1 {
			t.Errorf("want exactly one recovery hint line, got %d:\n%s", n, stderr)
		}
		if !strings.Contains(stderr, "re-run") {
			t.Errorf("the recovery hint must explain the re-run convergence:\n%s", stderr)
		}
	})

	t.Run("json_default_unchanged", func(t *testing.T) {
		jsonOutput = true
		var stdout, stderr string
		var err error
		stdout, stderr, err = captureImportOutcome(t, func() error {
			return renderImportOutcome(importResultJSON{Source: "chunk.jsonl", Created: 1, SkippedDependencies: skips}, "chunk.jsonl", 0)
		})
		if err != nil {
			t.Fatalf("default JSON import with skipped edges must still exit clean, got %v", err)
		}
		obj := decodeSingleJSONObject(t, "stdout", stdout)
		gotSkips, ok := obj["skipped_dependencies"].([]any)
		if !ok || len(gotSkips) != len(skips) {
			t.Errorf("skipped_dependencies = %v, want the %d reported edges", obj["skipped_dependencies"], len(skips))
		}
		if stderr != "" {
			t.Errorf("default JSON mode must not add a new stderr diagnostic, got:\n%s", stderr)
		}
	})

	t.Run("text_zero_skips_no_summary", func(t *testing.T) {
		jsonOutput, importStrictDeps = false, true
		var stderr string
		var err error
		_, stderr, err = captureImportOutcome(t, func() error {
			return renderImportOutcome(importResultJSON{Source: "clean.jsonl", Created: 2}, "clean.jsonl", 0)
		})
		if err != nil {
			t.Fatalf("clean import must exit clean even under --strict-deps, got %v", err)
		}
		if strings.Contains(stderr, "dependency edge") {
			t.Errorf("no skipped edges, but the report mentions them:\n%s", stderr)
		}
	})

	t.Run("text_strict_exit1_error_and_hint", func(t *testing.T) {
		jsonOutput, importStrictDeps = false, true
		var stderr string
		var err error
		_, stderr, err = captureImportOutcome(t, func() error {
			return renderImportOutcome(importResultJSON{Source: "chunk.jsonl", Created: 1, SkippedDependencies: skips}, "chunk.jsonl", 0)
		})
		exitErr, ok := err.(*exitError)
		if !ok {
			t.Fatalf("strict text import must fail with an exit error, got %v", err)
		}
		if exitErr.Code != 1 {
			t.Errorf("strict exit code = %d, want 1", exitErr.Code)
		}
		if !strings.Contains(stderr, "Error: 2 dependency edge(s) were reported skipped") {
			t.Errorf("missing aggregate error naming the skip count:\n%s", stderr)
		}
		if !strings.Contains(stderr, "--strict-deps") {
			t.Errorf("the aggregate error must name --strict-deps:\n%s", stderr)
		}
		if !strings.Contains(stderr, "remain committed") {
			t.Errorf("the aggregate error must say the imported rows remain committed:\n%s", stderr)
		}
		if n := strings.Count(stderr, "Hint:"); n != 1 {
			t.Errorf("want exactly one recovery hint line, got %d:\n%s", n, stderr)
		}
		if strings.Contains(stderr, "Warning:") {
			t.Errorf("strict mode must not also print the default summary warning:\n%s", stderr)
		}
	})

	t.Run("json_strict_error_envelope", func(t *testing.T) {
		jsonOutput, importStrictDeps = true, true
		var stdout, stderr string
		var err error
		stdout, stderr, err = captureImportOutcome(t, func() error {
			return renderImportOutcome(importResultJSON{Source: "chunk.jsonl", Created: 1, SkippedDependencies: skips}, "chunk.jsonl", 0)
		})
		exitErr, ok := err.(*exitError)
		if !ok {
			t.Fatalf("strict JSON import must fail with an exit error, got %v", err)
		}
		if exitErr.Code != 1 {
			t.Errorf("strict exit code = %d, want 1", exitErr.Code)
		}
		// stdout keeps exactly the one existing-shape result object.
		obj := decodeSingleJSONObject(t, "stdout", stdout)
		gotSkips, ok := obj["skipped_dependencies"].([]any)
		if !ok || len(gotSkips) != len(skips) {
			t.Errorf("skipped_dependencies = %v, want the %d reported edges", obj["skipped_dependencies"], len(skips))
		}
		// stderr carries the JSON error envelope with the count and hint.
		var envelope map[string]any
		if jErr := json.Unmarshal([]byte(stderr), &envelope); jErr != nil {
			t.Fatalf("stderr is not one JSON error envelope: %v\n%s", jErr, stderr)
		}
		if msg, _ := envelope["error"].(string); !strings.Contains(msg, "2 dependency edge(s) were reported skipped") || !strings.Contains(msg, "--strict-deps") {
			t.Errorf("JSON error = %v, want the skip count and the flag named", envelope["error"])
		}
		if hint, _ := envelope["hint"].(string); !strings.Contains(hint, "re-run") {
			t.Errorf("JSON hint = %v, want the recovery hint", envelope["hint"])
		}
		if strings.Contains(stderr, "Warning:") {
			t.Errorf("strict mode must not also print the default summary warning:\n%s", stderr)
		}
	})
}