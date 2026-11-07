//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedVersion(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("version_output", func(t *testing.T) {
		cmd := exec.Command(bd, "version")
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd version failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		if len(strings.TrimSpace(s)) == 0 {
			t.Error("expected non-empty version output")
		}
		// Should contain a version-like string
		if !strings.Contains(s, "v") && !strings.Contains(s, "0.") {
			t.Errorf("expected version string in output: %s", s)
		}
	})
}

func TestEmbeddedVersionConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			cmd := exec.Command(bd, "version")
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("version (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// versionLinePattern is the whole of `bd version`'s stdout:
// "bd version <version> (<build>)", with ": <commit>" or
// ": <branch>@<commit>" inside the parentheses when the binary knows them.
var versionLinePattern = regexp.MustCompile(`^bd version (\S+) \(([^):]+)(?:: (?:\S+@)?\S+)?\)\n$`)

// TestEmbeddedVersionOutputShape pins `bd version` the way scripts and
// plugins call it, in an ordinary workspace and in a directory that is not
// one:
//   - with no arguments it exits 0 and prints exactly one line on stdout,
//     "bd version <version> (<build>...)";
//   - --json prints one object with version, build and schema_version, plus
//     commit and branch when known, and no other key; its version and build
//     agree with the text line, and --format json prints the same document;
//   - an argument is refused with exit 1 and points at `bd versions <arg>`
//     (#6661): on stderr in text mode, as the JSON error on stdout with
//     --json, and without the version line either way.
//
// TestVersionWithAnArgumentPointsAtVersions pins the refusal on RunE; this
// pins it through the real binary, where the root pre-run and workspace
// discovery run first.
func TestEmbeddedVersionOutputShape(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	workspace, _, _ := bdInit(t, bd, "--prefix", "vo")

	for name, dir := range map[string]string{"workspace": workspace, "no workspace": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			text, stderr, exit := bdRunStreams(t, bd, dir, "version")
			if exit != 0 {
				t.Fatalf("bd version exited %d\nstdout:\n%s\nstderr:\n%s", exit, text, stderr)
			}
			line := versionLinePattern.FindStringSubmatch(text)
			if line == nil {
				t.Fatalf("bd version stdout is not one %q line:\n%q", versionLinePattern, text)
			}

			doc, stderr, exit := bdRunStreams(t, bd, dir, "version", "--json")
			if exit != 0 {
				t.Fatalf("bd version --json exited %d\nstdout:\n%s\nstderr:\n%s", exit, doc, stderr)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(doc), &fields); err != nil {
				t.Fatalf("bd version --json is not one JSON object: %v\n%s", err, doc)
			}
			for key := range fields {
				switch key {
				case "version", "build", "schema_version", "commit", "branch":
				default:
					t.Errorf("bd version --json has an unexpected key %q:\n%s", key, doc)
				}
			}
			if _, ok := fields["schema_version"].(float64); !ok {
				t.Errorf("bd version --json has no numeric schema_version:\n%s", doc)
			}
			if fields["version"] != line[1] || fields["build"] != line[2] {
				t.Errorf("bd version --json says version %v build %v, the text line says %s and %s:\n%s", fields["version"], fields["build"], line[1], line[2], doc)
			}
			for _, key := range []string{"commit", "branch"} {
				if v, ok := fields[key]; ok {
					if s, isString := v.(string); !isString || s == "" {
						t.Errorf("bd version --json %s = %#v, want a non-empty string when present", key, v)
					}
				}
			}
			if alias, _, exit := bdRunStreams(t, bd, dir, "version", "--format", "json"); exit != 0 || alias != doc {
				t.Errorf("bd version --format json exited %d and printed a different document from --json:\n%s\nwant:\n%s", exit, alias, doc)
			}

			stdout, stderr, exit := bdRunStreams(t, bd, dir, "version", "vo-123")
			if exit != 1 || stdout != "" {
				t.Errorf("bd version vo-123 exited %d with stdout %q, want exit 1 and nothing on stdout", exit, stdout)
			}
			for _, want := range []string{"Did you mean", "bd versions vo-123"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("bd version vo-123 stderr lacks %q:\n%s", want, stderr)
				}
			}

			stdout, stderr, exit = bdRunStreams(t, bd, dir, "version", "vo-123", "--json")
			var refusal struct {
				Error string `json:"error"`
			}
			if exit != 1 || json.Unmarshal([]byte(stdout), &refusal) != nil || !strings.Contains(refusal.Error, "bd versions vo-123") {
				t.Errorf("bd version vo-123 --json exited %d, want exit 1 and a JSON error naming `bd versions vo-123`\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
			}
		})
	}
}
