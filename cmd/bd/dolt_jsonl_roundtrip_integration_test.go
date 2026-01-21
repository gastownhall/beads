//go:build integration
// +build integration

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func parseCreateID(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "{")
	if idx < 0 {
		t.Fatalf("expected JSON in output, got:\n%s", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out[idx:]), &m); err != nil {
		t.Fatalf("failed to parse create JSON: %v\n%s", err, out)
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("missing id in create output:\n%s", out)
	}
	return id
}

func readJSONLIssues(t *testing.T, path string) map[string]*types.Issue {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// allow larger issues
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	out := make(map[string]*types.Issue)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			t.Fatalf("unmarshal JSONL line: %v\nline=%s", err, line)
		}
		iss.SetDefaults()
		copy := iss
		out[iss.ID] = &copy
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func writeJSONLIssues(t *testing.T, path string, issues map[string]*types.Issue) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatalf("open %s for write: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	ids := make([]string, 0, len(issues))
	for id := range issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	w := bufio.NewWriter(f)
	for _, id := range ids {
		iss := issues[id]
		if iss == nil {
			continue
		}
		b, err := json.Marshal(iss)
		if err != nil {
			t.Fatalf("marshal issue %s: %v", id, err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			t.Fatalf("write issue %s: %v", id, err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush %s: %v", path, err)
	}
}

func findCommentTimestamp(iss *types.Issue, author, text string) (time.Time, bool) {
	if iss == nil {
		return time.Time{}, false
	}
	for _, c := range iss.Comments {
		if c.Author == author && strings.TrimSpace(c.Text) == strings.TrimSpace(text) {
			return c.CreatedAt, true
		}
	}
	return time.Time{}, false
}

func findCommentTimestampByText(iss *types.Issue, text string) (time.Time, bool) {
	if iss == nil {
		return time.Time{}, false
	}
	for _, c := range iss.Comments {
		if strings.TrimSpace(c.Text) == strings.TrimSpace(text) {
			return c.CreatedAt, true
		}
	}
	return time.Time{}, false
}

func TestDoltJSONLRoundTrip_DepsLabelsCommentsTombstones(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	// Workspace 1: create data and export JSONL.
	ws1 := createTempDirWithCleanup(t)
	if err := runCommandInDir(ws1, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(ws1, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(ws1, "git", "config", "user.name", "Test User")

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
	}

	initOut, initErr := runBDExecAllowErrorWithEnv(t, ws1, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
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

	// Add label + comment + dependency.
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "label", "add", idA, "urgent"); err != nil {
		t.Fatalf("bd label add failed: %v\n%s", err, out)
	}
	commentText := "Hello from JSONL round-trip"
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "comments", "add", idA, commentText); err != nil {
		t.Fatalf("bd comments add failed: %v\n%s", err, out)
	}
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "dep", "add", idA, idB); err != nil {
		t.Fatalf("bd dep add failed: %v\n%s", err, out)
	}

	jsonl1 := filepath.Join(ws1, ".beads", "issues.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws1, env, "export", "-o", jsonl1); err != nil {
		t.Fatalf("bd export failed: %v\n%s", err, out)
	}

	issues1 := readJSONLIssues(t, jsonl1)
	if len(issues1) != 2 {
		t.Fatalf("expected 2 issues in export1, got %d", len(issues1))
	}
	if issues1[idA] == nil || issues1[idB] == nil {
		t.Fatalf("expected exported issues to include %s and %s", idA, idB)
	}
	// Label present
	foundUrgent := false
	for _, l := range issues1[idA].Labels {
		if l == "urgent" {
			foundUrgent = true
			break
		}
	}
	if !foundUrgent {
		t.Fatalf("expected label 'urgent' on %s in export1", idA)
	}
	// Dependency present
	foundDep := false
	for _, d := range issues1[idA].Dependencies {
		if d.DependsOnID == idB {
			foundDep = true
			break
		}
	}
	if !foundDep {
		t.Fatalf("expected dependency %s -> %s in export1", idA, idB)
	}
	// Comment present + capture timestamp
	ts1, ok := findCommentTimestampByText(issues1[idA], commentText)
	if !ok || ts1.IsZero() {
		t.Fatalf("expected comment on %s in export1", idA)
	}

	// Create a tombstone record in JSONL for issue B (Dolt backend may not support
	// creating tombstones via `bd delete`, but it must round-trip tombstones via JSONL).
	now := time.Now().UTC()
	issues1[idB].Status = types.StatusTombstone
	issues1[idB].DeletedAt = &now
	issues1[idB].DeletedBy = "test"
	issues1[idB].DeleteReason = "test tombstone"
	issues1[idB].OriginalType = string(issues1[idB].IssueType)
	issues1[idB].SetDefaults()

	jsonl1Tomb := filepath.Join(ws1, ".beads", "issues.tomb.jsonl")
	writeJSONLIssues(t, jsonl1Tomb, issues1)
	issues1Tomb := readJSONLIssues(t, jsonl1Tomb)
	if issues1Tomb[idB].Status != types.StatusTombstone {
		t.Fatalf("expected %s to be tombstone in tombstone JSONL, got %q", idB, issues1Tomb[idB].Status)
	}

	// Workspace 2: import JSONL into fresh Dolt DB and re-export.
	ws2 := createTempDirWithCleanup(t)
	if err := runCommandInDir(ws2, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(ws2, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(ws2, "git", "config", "user.name", "Test User")

	initOut2, initErr2 := runBDExecAllowErrorWithEnv(t, ws2, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr2 != nil {
		lower := strings.ToLower(initOut2)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut2)
		}
		t.Fatalf("bd init --backend dolt (ws2) failed: %v\n%s", initErr2, initOut2)
	}

	// Copy JSONL into ws2 beads dir
	jsonl2in := filepath.Join(ws2, ".beads", "issues.jsonl")
	data, err := os.ReadFile(jsonl1Tomb)
	if err != nil {
		t.Fatalf("read export1: %v", err)
	}
	if err := os.WriteFile(jsonl2in, data, 0o600); err != nil {
		t.Fatalf("write ws2 issues.jsonl: %v", err)
	}

	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "import", "-i", jsonl2in); err != nil {
		t.Fatalf("bd import failed: %v\n%s", err, out)
	}

	jsonl2out := filepath.Join(ws2, ".beads", "roundtrip.jsonl")
	if out, err := runBDExecAllowErrorWithEnv(t, ws2, env, "export", "-o", jsonl2out); err != nil {
		t.Fatalf("bd export (ws2) failed: %v\n%s", err, out)
	}

	issues2 := readJSONLIssues(t, jsonl2out)
	if len(issues2) != 2 {
		t.Fatalf("expected 2 issues in export2 (including tombstone), got %d", len(issues2))
	}
	if issues2[idB].Status != types.StatusTombstone {
		t.Fatalf("expected %s to be tombstone in export2, got %q", idB, issues2[idB].Status)
	}
	// Ensure comment timestamp preserved across import/export
	ts2, ok := findCommentTimestampByText(issues2[idA], commentText)
	if !ok {
		t.Fatalf("expected comment on %s in export2", idA)
	}
	if !ts2.Equal(ts1) {
		t.Fatalf("expected comment timestamp preserved, export1=%s export2=%s", ts1.Format(time.RFC3339Nano), ts2.Format(time.RFC3339Nano))
	}
}
