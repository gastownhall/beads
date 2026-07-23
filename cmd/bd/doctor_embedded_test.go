//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// doctorTestEnv extends bdEnv by forcing legacy JSON output (BD_JSON_ENVELOPE=0)
// so the test assertions can read top-level keys regardless of whether the
// developer's shell exports envelope mode.
func doctorTestEnv(dir string) []string {
	return append(bdEnv(dir), "BD_JSON_ENVELOPE=0")
}

// runBDCombined runs `bd <args...>` in the given dir with the test embedded
// env and returns combined stdout+stderr.
func runBDCombined(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = doctorTestEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runBDSplit runs `bd <args...>` capturing stdout and stderr separately so
// each stream can be asserted independently. The bd JSON contract puts errors
// (including embedded-unsupported stubs) on stderr per docs/JSON_SCHEMA.md.
func runBDSplit(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = doctorTestEnv(dir)
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// TestDoctorEmbeddedSupportedChecks verifies that --check=artifacts,
// --check=conventions, and --check=pollution run in embedded mode rather
// than hitting the "not yet supported" stub (GH#3597).
func TestDoctorEmbeddedSupportedChecks(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "de")

	supported := []struct {
		name string
		args []string
	}{
		{"artifacts", []string{"doctor", "--check=artifacts"}},
		{"artifacts_json", []string{"doctor", "--check=artifacts", "--json"}},
		{"conventions", []string{"doctor", "--check=conventions"}},
		{"conventions_json", []string{"doctor", "--check=conventions", "--json"}},
		{"pollution", []string{"doctor", "--check=pollution"}},
		{"pollution_json", []string{"doctor", "--check=pollution", "--json"}},
	}

	for _, tc := range supported {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := runBDCombined(t, bd, dir, tc.args...)
			if err != nil {
				t.Fatalf("bd %s failed: %v\n%s", strings.Join(tc.args, " "), err, out)
			}
			if strings.Contains(out, "not yet supported in embedded mode") {
				t.Errorf("bd %s hit the embedded-mode stub unexpectedly:\n%s",
					strings.Join(tc.args, " "), out)
			}
		})
	}
}

// TestDoctorEmbeddedUnsupportedJSON verifies that variants which still require
// server mode (bare doctor, --check=validate) emit a structured JSON payload
// when --json or --agent --json is set, rather than the prose stub. Tooling
// can detect "unsupported" without text-matching (GH#3597).
func TestDoctorEmbeddedUnsupportedJSON(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "du")

	cases := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		{"bare_doctor_json", []string{"doctor", "--json"}, "doctor"},
		{"agent_json", []string{"doctor", "--agent", "--json"}, "doctor"},
		{"validate_json", []string{"doctor", "--check=validate", "--json"}, "doctor --check=validate"},
		{"perf_json", []string{"doctor", "--perf", "--json"}, "doctor --perf"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runBDSplit(t, bd, dir, tc.args...)
			if err != nil {
				t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
					strings.Join(tc.args, " "), err, stdout, stderr)
			}

			// JSON error payloads are written to stderr per the bd JSON
			// contract (docs/JSON_SCHEMA.md). Stdout should be empty so
			// stdout-only consumers don't accidentally swallow the stub.
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("expected empty stdout for embedded-unsupported stub, got:\n%s", stdout)
			}

			var result map[string]interface{}
			if err := json.Unmarshal([]byte(stderr), &result); err != nil {
				t.Fatalf("bd %s did not emit valid JSON on stderr: %v\nstderr:\n%s",
					strings.Join(tc.args, " "), err, stderr)
			}

			if result["unsupported"] != true {
				t.Errorf("expected unsupported=true, got %v", result["unsupported"])
			}
			if result["mode"] != "embedded" {
				t.Errorf("expected mode=embedded, got %v", result["mode"])
			}
			if result["command"] != tc.wantCommand {
				t.Errorf("expected command=%q, got %v", tc.wantCommand, result["command"])
			}
			if result["code"] != "embedded_unsupported" {
				t.Errorf("expected code=embedded_unsupported, got %v", result["code"])
			}
			errStr, _ := result["error"].(string)
			if !strings.Contains(errStr, "not yet supported in embedded mode") {
				t.Errorf("expected error to mention embedded-mode unsupported, got %q", errStr)
			}
		})
	}
}

// TestDoctorEmbeddedUnsupportedProse verifies that bare bd doctor (without
// --json) still emits the human-readable prose stub for terminal users,
// pointing at the supported --check=* variants (GH#3597).
func TestDoctorEmbeddedUnsupportedProse(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dp")

	out, err := runBDCombined(t, bd, dir, "doctor")
	if err != nil {
		t.Fatalf("bd doctor failed: %v\n%s", err, out)
	}

	if !strings.Contains(out, "not yet supported in embedded mode") {
		t.Errorf("expected prose stub in plain bd doctor output:\n%s", out)
	}

	// The stub should advertise the embedded-mode-supported checks.
	for _, want := range []string{"--check=artifacts", "--check=conventions", "--check=pollution"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected stub to mention %q so users know what works:\n%s", want, out)
		}
	}
}
