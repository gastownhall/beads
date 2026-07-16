//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestFlatfileMigrationRoundTrip seeds an embedded Dolt workspace with
// representative data — an epic with parent-child and blocks dependencies,
// labels, comments (including two in the same second), custom metadata, a
// closed issue, a memory, and a custom config key — then migrates
// Dolt→flatfile→Dolt and asserts the portable JSONL serialization
// (`bd export --all`, the layer bd import/export round-trips through) is
// identical before and after. The export is an independent oracle: it never
// touches the migration code paths under test.
//
// Ordering the backends leave unspecified (issue result order, same-second
// comment ties, label order) is normalized before comparison; every record
// and field still has to match.
func TestFlatfileMigrationRoundTrip(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rt", "--backend", "dolt")

	// ── Seed ──
	epic := bdCreate(t, bd, dir, "Roundtrip epic", "--type", "epic", "--priority", "1")
	t1 := bdCreate(t, bd, dir, "Task one", "--description", "First task",
		"--deps", "parent-child:"+epic.ID)
	t2 := bdCreate(t, bd, dir, "Task two", "--priority", "3",
		"--deps", "blocks:"+t1.ID+",parent-child:"+epic.ID)

	bdCommand(t, bd, dir, "label", "add", t1.ID, "urgent")
	bdCommand(t, bd, dir, "label", "add", t1.ID, "backend")

	// Two comments on the same issue back-to-back: after export from Dolt
	// their created_at collapses to the same second, which must not lose one.
	bdCommand(t, bd, dir, "comments", "add", t1.ID, "first comment")
	bdCommand(t, bd, dir, "comments", "add", t1.ID, "second comment same second")
	bdCommand(t, bd, dir, "comments", "add", t2.ID, "comment on task two")

	// Custom metadata (the storage layer's metadata slots live in this JSON).
	bdCommand(t, bd, dir, "update", t1.ID, "--metadata", `{"review":"pending","severity":"high"}`)

	bdCommand(t, bd, dir, "close", t2.ID, "--reason", "done in roundtrip test")

	bdCommand(t, bd, dir, "remember", "roundtrip memory insight")
	bdCommand(t, bd, dir, "config", "set", "custom.roundtrip", "survives-migration")

	// ── Oracle dump #1 (from Dolt) ──
	before := filepath.Join(dir, "before.jsonl")
	bdCommand(t, bd, dir, "export", "--all", "-o", before)

	beforeIssues, beforeMemories := parsePortableDump(t, before)
	if len(beforeIssues) != 3 {
		t.Fatalf("seed produced %d issues in export, want 3", len(beforeIssues))
	}
	if len(beforeMemories) != 1 {
		t.Fatalf("seed produced %d memories in export, want 1", len(beforeMemories))
	}
	if got := countComments(beforeIssues); got != 3 {
		t.Fatalf("seed produced %d comments in export, want 3", got)
	}

	// ── Dolt → flatfile ──
	bdMigrate(t, bd, dir, "flatfile")
	assertBackend(t, beadsDir, "flatfile")
	issueFiles, err := filepath.Glob(filepath.Join(beadsDir, "issues", "*.json"))
	if err != nil || len(issueFiles) != 3 {
		t.Fatalf("flatfile issues dir has %d files (err=%v), want 3", len(issueFiles), err)
	}

	// ── flatfile → Dolt ──
	out := bdMigrate(t, bd, dir, "flatfile", "--reverse")
	if !strings.Contains(out, "Dolt backend is now active") {
		t.Fatalf("reverse migration output missing completion message:\n%s", out)
	}
	assertBackend(t, beadsDir, "dolt")

	// ── Oracle dump #2 (from Dolt again) ──
	after := filepath.Join(dir, "after.jsonl")
	bdCommand(t, bd, dir, "export", "--all", "-o", after)
	afterIssues, afterMemories := parsePortableDump(t, after)

	if !reflect.DeepEqual(beforeIssues, afterIssues) {
		t.Errorf("issue records differ after round trip:\nbefore: %s\nafter:  %s",
			mustJSON(t, beforeIssues), mustJSON(t, afterIssues))
	}
	if !reflect.DeepEqual(beforeMemories, afterMemories) {
		t.Errorf("memory records differ after round trip:\nbefore: %s\nafter:  %s",
			mustJSON(t, beforeMemories), mustJSON(t, afterMemories))
	}

	// Config beyond memories is not part of the export dump; assert it
	// separately.
	if got := strings.TrimSpace(bdCommand(t, bd, dir, "config", "get", "custom.roundtrip")); !strings.Contains(got, "survives-migration") {
		t.Errorf("custom config after round trip = %q, want survives-migration", got)
	}
	if got := strings.TrimSpace(bdCommand(t, bd, dir, "config", "get", "issue_prefix")); !strings.Contains(got, "rt") {
		t.Errorf("issue_prefix after round trip = %q, want rt", got)
	}
}

// TestFlatfileReverseMigrationGuards covers the failure-mode UX: --reverse on
// a Dolt workspace refuses, and --reverse --dry-run changes nothing.
func TestFlatfileReverseMigrationGuards(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("reverse_on_dolt_backend_refuses", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rg", "--backend", "dolt")
		out := bdMigrateFail(t, bd, dir, "flatfile", "--reverse")
		if !strings.Contains(out, "not using the flat-file backend") {
			t.Errorf("expected flat-file guard message, got:\n%s", out)
		}
	})

	t.Run("reverse_dry_run_changes_nothing", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rd", "--backend", "dolt")
		bdCreate(t, bd, dir, "Dry run issue")
		bdMigrate(t, bd, dir, "flatfile")
		assertBackend(t, beadsDir, "flatfile")

		out := bdMigrate(t, bd, dir, "flatfile", "--reverse", "--dry-run")
		if !strings.Contains(out, "Found 1 issues") || !strings.Contains(out, "Dry run") {
			t.Errorf("unexpected dry-run output:\n%s", out)
		}
		assertBackend(t, beadsDir, "flatfile")
	})
}

// assertBackend checks metadata.json's backend field.
func assertBackend(t *testing.T, beadsDir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var meta struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v", err)
	}
	if meta.Backend != want {
		t.Fatalf("metadata.json backend = %q, want %q", meta.Backend, want)
	}
}

// parsePortableDump reads a `bd export --all` JSONL file and returns issue
// records (sorted by id, with backend-unspecified orderings normalized) and
// memory records (sorted by key).
func parsePortableDump(t *testing.T, path string) (issues, memories []map[string]interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse export line %q: %v", line, err)
		}
		switch rec["_type"] {
		case "memory":
			memories = append(memories, rec)
		case "issue":
			normalizeIssueRecord(rec)
			issues = append(issues, rec)
		default:
			// Schema header lines carry _schema; anything else is unexpected.
			if _, ok := rec["_schema"]; !ok {
				t.Fatalf("unrecognized export record: %s", line)
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		return fmt.Sprint(issues[i]["id"]) < fmt.Sprint(issues[j]["id"])
	})
	sort.Slice(memories, func(i, j int) bool {
		return fmt.Sprint(memories[i]["key"]) < fmt.Sprint(memories[j]["key"])
	})
	return issues, memories
}

// normalizeIssueRecord sorts the orderings the storage contract leaves
// unspecified: labels, dependency records, and same-second comment ties.
// Field values are never altered.
func normalizeIssueRecord(rec map[string]interface{}) {
	if labels, ok := rec["labels"].([]interface{}); ok {
		sort.Slice(labels, func(i, j int) bool {
			return fmt.Sprint(labels[i]) < fmt.Sprint(labels[j])
		})
	}
	if deps, ok := rec["dependencies"].([]interface{}); ok {
		sort.Slice(deps, func(i, j int) bool {
			return depSortKey(deps[i]) < depSortKey(deps[j])
		})
	}
	if comments, ok := rec["comments"].([]interface{}); ok {
		sort.Slice(comments, func(i, j int) bool {
			return commentSortKey(comments[i]) < commentSortKey(comments[j])
		})
	}
}

func depSortKey(v interface{}) string {
	m, _ := v.(map[string]interface{})
	return fmt.Sprint(m["issue_id"], "|", m["depends_on_id"], "|", m["type"])
}

func commentSortKey(v interface{}) string {
	m, _ := v.(map[string]interface{})
	return fmt.Sprint(m["created_at"], "|", m["id"])
}

func countComments(issues []map[string]interface{}) int {
	n := 0
	for _, rec := range issues {
		if comments, ok := rec["comments"].([]interface{}); ok {
			n += len(comments)
		}
	}
	return n
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
