//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctorExportCheckResult mirrors the structured payload of
// `bd doctor --check=export --json` (GH#6955): the field set is pinned by the
// design — check, status, path, export_auto, in_scope, in_file, missing,
// missing_ids — plus the explanatory message for absent-export cases.
type doctorExportCheckResult struct {
	Check      string   `json:"check"`
	Status     string   `json:"status"`
	Path       string   `json:"path"`
	ExportAuto bool     `json:"export_auto"`
	InScope    int      `json:"in_scope"`
	InFile     int      `json:"in_file"`
	Missing    int      `json:"missing"`
	MissingIDs []string `json:"missing_ids"`
	Message    string   `json:"message"`
}

func runDoctorExportCheck(t *testing.T, bd, dir string, jsonMode bool) (doctorExportCheckResult, string) {
	t.Helper()
	args := []string{"doctor", "--check=export"}
	if jsonMode {
		args = append(args, "--json")
	}
	stdout, stderr, err := runBDSplit(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	if !jsonMode {
		return doctorExportCheckResult{}, stdout
	}
	var result doctorExportCheckResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("bd %s did not emit valid JSON on stdout: %v\nstdout:\n%s", strings.Join(args, " "), err, stdout)
	}
	return result, stdout
}

// TestEmbeddedDoctorExportCheckStaleFileWarns is GH#6955's reported shape,
// detected rather than silently tolerated: the passive export file lags the
// store, and `bd doctor --check=export` must exit 0 with a warning carrying
// the missing-ID set, the resolved path, and the refresh action. The
// comparison is by issue ID, not line counts.
func TestEmbeddedDoctorExportCheckStaleFileWarns(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dxs")

	// Export a one-issue snapshot, then create a second issue with
	// auto-export left off: the file now lags the store by exactly one ID.
	bdCreateSilent(t, bd, dir, "Exported issue", "--type", "task")
	if out, err := bdRunWithFlockRetry(t, bd, dir, "export", "-o", filepath.Join(beadsDir, "issues.jsonl")); err != nil {
		t.Fatalf("bd export -o failed: %v\n%s", err, out)
	}
	idB := bdCreateSilent(t, bd, dir, "Issue created after the export", "--type", "task")

	_, stdout := runDoctorExportCheck(t, bd, dir, false)
	if strings.Contains(stdout, "not yet supported in embedded mode") {
		t.Fatalf("export check hit the embedded-mode stub:\n%s", stdout)
	}
	if !strings.Contains(stdout, idB) {
		t.Errorf("human warning does not name the missing issue %s:\n%s", idB, stdout)
	}
	if !strings.Contains(stdout, "bd export -o") {
		t.Errorf("human warning does not give the refresh action:\n%s", stdout)
	}

	result, _ := runDoctorExportCheck(t, bd, dir, true)
	if result.Check != "export" {
		t.Errorf("check = %q, want %q", result.Check, "export")
	}
	if result.Status != "warning" {
		t.Errorf("status = %q, want %q (a missing-ID finding is a warning, and the command must still exit 0)", result.Status, "warning")
	}
	if want := filepath.Join(beadsDir, "issues.jsonl"); result.Path != want {
		t.Errorf("path = %q, want %q", result.Path, want)
	}
	if result.ExportAuto {
		t.Errorf("export_auto = true, want false (the scenario runs with auto-export off)")
	}
	if result.InScope != 2 || result.InFile != 1 {
		t.Errorf("in_scope = %d, in_file = %d, want 2 and 1", result.InScope, result.InFile)
	}
	if result.Missing != 1 {
		t.Errorf("missing = %d, want 1", result.Missing)
	}
	if len(result.MissingIDs) != 1 || result.MissingIDs[0] != idB {
		t.Errorf("missing_ids = %v, want exactly [%s]", result.MissingIDs, idB)
	}
	if !strings.Contains(result.Message, "bd export -o "+result.Path) {
		t.Errorf("message = %q, want the refresh action for %s", result.Message, result.Path)
	}
}

// TestEmbeddedDoctorExportCheckMissingFileWarns pins the other warning path:
// auto-export is enabled but the passive export file is gone (fresh clone, a
// wipe, or an accident). With in-scope store content, that is a warning
// covering every in-scope ID — not a "file missing" error and not a parity
// claim for an empty file.
func TestEmbeddedDoctorExportCheckMissingFileWarns(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dxm")

	for _, kv := range [][]string{
		{"export.auto", "true"},
		{"export.git-add", "false"},
	} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("bd config set %s %s failed: %v\n%s", kv[0], kv[1], err, out)
		}
	}

	// The create itself auto-exports (AC-1..AC-3 fix exactly this), so the
	// file exists; removing it simulates the wiped-file scenario.
	id := bdCreateSilent(t, bd, dir, "Wiped-file sentinel issue", "--type", "task")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); err != nil {
		t.Fatalf("expected the create to auto-export %s first — without it the scenario is not the missing-file branch: %v", jsonlPath, err)
	}
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatalf("removing the export file: %v", err)
	}

	result, stdout := runDoctorExportCheck(t, bd, dir, true)
	if result.Status != "warning" {
		t.Errorf("status = %q, want %q — a missing file with auto-export enabled and nonempty scope is a warning:\n%s", result.Status, "warning", stdout)
	}
	if !result.ExportAuto {
		t.Errorf("export_auto = false, want true")
	}
	if result.Missing != 1 || len(result.MissingIDs) != 1 || result.MissingIDs[0] != id {
		t.Errorf("missing = %d, missing_ids = %v, want 1 and [%s]", result.Missing, result.MissingIDs, id)
	}
	if want := jsonlPath; result.Path != want {
		t.Errorf("path = %q, want %q", result.Path, want)
	}

	// With auto-export off, the same absent file is explicitly N/A: the
	// diagnostic must not claim parity for a file nobody promised.
	if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "export.auto", "false"); err != nil {
		t.Fatalf("bd config set export.auto false failed: %v\n%s", err, out)
	}
	result, stdout = runDoctorExportCheck(t, bd, dir, true)
	if result.Status != "not_applicable" {
		t.Errorf("status = %q, want %q for a missing file with auto-export disabled:\n%s", result.Status, "not_applicable", stdout)
	}
	if result.Message == "" {
		t.Error("message is empty; the N/A result must say why it makes no parity claim")
	}
	if result.Missing != 0 {
		t.Errorf("N/A result still reports missing = %d", result.Missing)
	}
}

// TestEmbeddedDoctorExportCheckScopes pins the no-false-positive promises of
// the diagnostic: legitimate export exclusions (an ephemeral wisp the writer
// would never export) and extra JSONL IDs (a historical export or a manual
// wider export) never manufacture missing-ID warnings on their own.
func TestEmbeddedDoctorExportCheckScopes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dxc")

	idA := bdCreateSilent(t, bd, dir, "Durable issue", "--type", "task")
	idE := bdCreateSilent(t, bd, dir, "Ephemeral wisp the export writer excludes", "--ephemeral")

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if out, err := bdRunWithFlockRetry(t, bd, dir, "export", "-o", jsonlPath); err != nil {
		t.Fatalf("bd export -o failed: %v\n%s", err, out)
	}
	if !jsonlHasIssueID(t, jsonlPath, idA) {
		data, _ := os.ReadFile(jsonlPath)
		t.Fatalf("export is missing the durable issue %s — fixture broken:\n%s", idA, data)
	}
	if jsonlHasIssueID(t, jsonlPath, idE) {
		data, _ := os.ReadFile(jsonlPath)
		t.Fatalf("export unexpectedly contains the ephemeral wisp %s — fixture broken:\n%s", idE, data)
	}

	// An extra ID in the file (a deleted issue's tombstone-free history, or a
	// manual wider export) is informational, not divergence.
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("appending an extra record: %v", err)
	}
	if _, err := f.WriteString("{\"id\": \"bd-extrafile1\", \"_type\": \"issue\", \"status\": \"open\"}\n"); err != nil {
		t.Fatalf("appending an extra record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing the export file: %v", err)
	}

	result, stdout := runDoctorExportCheck(t, bd, dir, true)
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q — the excluded wisp and the extra file ID are not missing store records:\n%s", result.Status, "ok", stdout)
	}
	if result.Missing != 0 || len(result.MissingIDs) != 0 {
		t.Errorf("missing = %d, missing_ids = %v, want zero on a fully covered in-scope set", result.Missing, result.MissingIDs)
	}
	if result.InScope != 1 || result.InFile != 2 {
		t.Errorf("in_scope = %d, in_file = %d, want 1 (durable only) and 2 (with the extra record)", result.InScope, result.InFile)
	}
	for _, sample := range result.MissingIDs {
		if sample == idE {
			t.Errorf("missing_ids contains the excluded wisp %s", idE)
		}
	}
}
