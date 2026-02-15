//go:build cgo && integration

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func isDoltBackendUnavailable(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown"))
}

func setupGitRepoForIntegration(t *testing.T, dir string) {
	t.Helper()
	if err := runCommandInDir(dir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(dir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(dir, "git", "config", "user.name", "Test User")
}

func TestSQLiteToDolt_JSONLRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("cross-backend integration test not supported on windows")
	}

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
	}

	// Workspace 1: SQLite create -> export JSONL
	ws1 := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, ws1)

	// Explicitly initialize sqlite for clarity.
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "init", "--backend", "sqlite", "--prefix", "test", "--quiet"); err != nil {
		t.Fatalf("bd init --backend sqlite failed: %v\n%s", err, out)
	}

	outA, err := runBDExecAllowErrorWithEnv(t, ws1, env, "create", "Issue A", "--json")
	if err != nil {
		t.Fatalf("bd create A failed: %v\n%s", err, outA)
	}
	idA := parseCreateID(t, outA)

	outB, err := runBDExecAllowErrorWithEnv(t, ws1, env, "create", "Issue B", "--json")
	if err != nil {
		t.Fatalf("bd create B failed: %v\n%s", err, outB)
	}
	idB := parseCreateID(t, outB)

	// Add label + comment + dependency.
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "label", "add", idA, "urgent"); err != nil {
		t.Fatalf("bd label add failed: %v\n%s", err, out)
	}
	commentText := "Cross-backend round-trip"
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "comments", "add", idA, commentText); err != nil {
		t.Fatalf("bd comments add failed: %v\n%s", err, out)
	}
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "dep", "add", idA, idB); err != nil {
		t.Fatalf("bd dep add failed: %v\n%s", err, out)
	}

	// Delete issue B via force delete.
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "delete", idB, "--force", "--reason", "test deletion"); err != nil {
		t.Fatalf("bd delete failed: %v\n%s", err, out)
	}

	jsonl1 := filepath.Join(ws1, ".beads", "issues.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "export", "-o", jsonl1); err != nil {
		t.Fatalf("bd export failed: %v\n%s", err, out)
	}

	issues1 := readJSONLIssues(t, jsonl1)
	ts1, ok := findCommentTimestampByText(issues1[idA], commentText)
	if !ok || ts1.IsZero() {
		t.Fatalf("expected comment on %s in sqlite export", idA)
	}

	// Workspace 2: Dolt import JSONL -> export JSONL
	ws2 := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, ws2)

	initOut, initErr := runBDExecAllowErrorWithEnv(t, ws2, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	jsonl2in := filepath.Join(ws2, ".beads", "issues.jsonl")
	data, err := os.ReadFile(jsonl1)
	if err != nil {
		t.Fatalf("read sqlite export: %v", err)
	}
	if err := os.WriteFile(jsonl2in, data, 0o600); err != nil {
		t.Fatalf("write dolt issues.jsonl: %v", err)
	}

	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "import", "-i", jsonl2in); err != nil {
		t.Fatalf("bd import (dolt) failed: %v\n%s", err, out)
	}

	jsonl2out := filepath.Join(ws2, ".beads", "roundtrip.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "export", "-o", jsonl2out); err != nil {
		t.Fatalf("bd export (dolt) failed: %v\n%s", err, out)
	}

	issues2 := readJSONLIssues(t, jsonl2out)
	ts2, ok := findCommentTimestampByText(issues2[idA], commentText)
	if !ok {
		t.Fatalf("expected comment on %s in dolt export", idA)
	}
	if !ts2.Equal(ts1) {
		t.Fatalf("expected comment timestamp preserved across sqlite->dolt, export1=%s export2=%s", ts1.Format(time.RFC3339Nano), ts2.Format(time.RFC3339Nano))
	}
}

func TestDoltToSQLite_JSONLRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("cross-backend integration test not supported on windows")
	}

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
	}

	// Workspace 1: Dolt create -> export JSONL
	ws1 := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, ws1)

	initOut, initErr := runBDExecAllowErrorWithEnv(t, ws1, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	outA, err := runBDExecAllowErrorWithEnv(t, ws1, env, "create", "Issue A", "--json")
	if err != nil {
		t.Fatalf("bd create A failed: %v\n%s", err, outA)
	}
	idA := parseCreateID(t, outA)

	outB, err := runBDExecAllowErrorWithEnv(t, ws1, env, "create", "Issue B", "--json")
	if err != nil {
		t.Fatalf("bd create B failed: %v\n%s", err, outB)
	}
	idB := parseCreateID(t, outB)

	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "label", "add", idA, "urgent"); err != nil {
		t.Fatalf("bd label add failed: %v\n%s", err, out)
	}
	commentText := "Cross-backend round-trip"
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "comments", "add", idA, commentText); err != nil {
		t.Fatalf("bd comments add failed: %v\n%s", err, out)
	}
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "dep", "add", idA, idB); err != nil {
		t.Fatalf("bd dep add failed: %v\n%s", err, out)
	}

	jsonl1 := filepath.Join(ws1, ".beads", "issues.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "export", "-o", jsonl1); err != nil {
		t.Fatalf("bd export (dolt) failed: %v\n%s", err, out)
	}

	issues1 := readJSONLIssues(t, jsonl1)
	if len(issues1) != 2 {
		t.Fatalf("expected 2 issues in dolt export, got %d", len(issues1))
	}
	ts1, ok := findCommentTimestampByText(issues1[idA], commentText)
	if !ok || ts1.IsZero() {
		t.Fatalf("expected comment on %s in dolt export", idA)
	}

	// Mark issue B as closed+deleted in JSONL for round-trip testing.
	now := time.Now().UTC()
	issues1[idB].Status = types.StatusClosed
	issues1[idB].ClosedAt = &now
	issues1[idB].SetDefaults()

	jsonl1Deleted := filepath.Join(ws1, ".beads", "issues.deleted.jsonl")
	writeJSONLIssues(t, jsonl1Deleted, issues1)

	// Workspace 2: SQLite import JSONL -> export JSONL
	ws2 := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, ws2)

	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "init", "--backend", "sqlite", "--prefix", "test", "--quiet"); err != nil {
		t.Fatalf("bd init --backend sqlite failed: %v\n%s", err, out)
	}

	jsonl2in := filepath.Join(ws2, ".beads", "issues.jsonl")
	data, err := os.ReadFile(jsonl1Deleted)
	if err != nil {
		t.Fatalf("read dolt export: %v", err)
	}
	if err := os.WriteFile(jsonl2in, data, 0o600); err != nil {
		t.Fatalf("write sqlite issues.jsonl: %v", err)
	}

	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "import", "-i", jsonl2in); err != nil {
		t.Fatalf("bd import (sqlite) failed: %v\n%s", err, out)
	}

	jsonl2out := filepath.Join(ws2, ".beads", "roundtrip.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "export", "-o", jsonl2out); err != nil {
		t.Fatalf("bd export (sqlite) failed: %v\n%s", err, out)
	}

	issues2 := readJSONLIssues(t, jsonl2out)
	if len(issues2) != 2 {
		t.Fatalf("expected 2 issues in sqlite export, got %d", len(issues2))
	}
	if issues2[idB].Status != types.StatusClosed {
		t.Fatalf("expected %s to be closed after import into sqlite, got %q", idB, issues2[idB].Status)
	}
	ts2, ok := findCommentTimestampByText(issues2[idA], commentText)
	if !ok {
		t.Fatalf("expected comment on %s in sqlite export", idA)
	}
	if !ts2.Equal(ts1) {
		t.Fatalf("expected comment timestamp preserved across dolt->sqlite, export1=%s export2=%s", ts1.Format(time.RFC3339Nano), ts2.Format(time.RFC3339Nano))
	}
}
