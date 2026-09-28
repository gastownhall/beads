//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// JSON-mode contract for the `--if-updated-at` fence (spec D7/T29).
//
// The guard family's machine-readable refusal is a single compact JSON line —
// the LAST line on stderr — carrying "error", "failed", "schema_version", and
// a per-failure "guard_mismatch" marker. A stamp-guard refusal must join that
// family: consumers (and the AF reconciliation lane in particular) branch on
// guard_mismatch to tell "a racer won, don't retry" from infra failure, so a
// refusal without the marker — or a per-ID list that drops entries — breaks
// every guarded caller, not just the new flag's.

// bdUpdateStreams runs "bd update" regardless of outcome and returns stdout,
// stderr and the process exit code separately. The JSON contract test needs
// the split: the success array lives on stdout while the failure report is
// the last line on stderr, and a warning on the wrong stream is itself a
// contract break.
func bdUpdateStreams(t *testing.T, bd, dir string, args ...string) (string, string, int) {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("bd update %s could not run: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// TestUpdateIfUpdatedAtJSONFailureReport fences an update behind a stamp that
// a heartbeat has since invalidated, and pins the machine-readable refusal:
// exit 13, one failed entry per refused ID, guard_mismatch true, and a
// per-ID error string that names the CURRENT updated_at.
func TestUpdateIfUpdatedAtJSONFailureReport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "uuj")

	issue := bdCreate(t, bd, dir, "Stamp json refusal", "--type", "task")
	readStamp := bdShow(t, bd, dir, issue.ID).UpdatedAt.UTC().Format(time.RFC3339)

	// The heartbeat bumps the generation after the supervisor's read; the
	// fenced write is then refused per the stale-read contract.
	waitPastStampBoundary(t)
	bdUpdate(t, bd, dir, issue.ID, "--append-notes", "heartbeat")
	currentStamp := bdShow(t, bd, dir, issue.ID).UpdatedAt.UTC().Format(time.RFC3339)

	stdout, stderr, code := bdUpdateStreams(t, bd, dir, issue.ID,
		"--priority", "1", "--if-updated-at", readStamp, "--json")
	if code != ExitGuardMismatch {
		t.Errorf("stale stamp with --json exit = %d, want %d\nstdout:\n%s\nstderr:\n%s",
			code, ExitGuardMismatch, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want empty (no issue was updated)", stdout)
	}

	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	report := lines[len(lines)-1]
	var payload struct {
		Error         string `json:"error"`
		SchemaVersion int    `json:"schema_version"`
		Failed        []struct {
			ID            string `json:"id"`
			Error         string `json:"error"`
			GuardMismatch bool   `json:"guard_mismatch"`
		} `json:"failed"`
	}
	if err := json.Unmarshal([]byte(report), &payload); err != nil {
		t.Fatalf("last stderr line is not the JSON failure report: %v\nline: %s\nstderr:\n%s", err, report, stderr)
	}
	if payload.Error != "1 of 1 issues failed to update" {
		t.Errorf("error = %q, want %q", payload.Error, "1 of 1 issues failed to update")
	}
	if payload.SchemaVersion != JSONSchemaVersion {
		t.Errorf("schema_version = %d, want %d", payload.SchemaVersion, JSONSchemaVersion)
	}
	if len(payload.Failed) != 1 {
		t.Fatalf("failed entries = %d, want 1 (per-ID failure report, not a summary blob)", len(payload.Failed))
	}
	if payload.Failed[0].ID != issue.ID {
		t.Errorf("failed[0].id = %q, want %q", payload.Failed[0].ID, issue.ID)
	}
	if !payload.Failed[0].GuardMismatch {
		t.Error("failed[0].guard_mismatch = false; a stale --if-updated-at must be marked like the other guards")
	}
	if !outputCarriesStamp(t, payload.Failed[0].Error, currentStamp) {
		t.Errorf("failed[0].error must report the CURRENT updated_at %s, got: %q", currentStamp, payload.Failed[0].Error)
	}
}
