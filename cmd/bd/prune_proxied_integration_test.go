//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func issueRowExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count issue %s: %v", id, err)
	}
	return n > 0
}

func bdProxiedPrune(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prune"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd prune %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedPruneFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prune"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd prune %s to fail, got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func TestProxiedServerPrune(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	t.Run("requires_filter", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "prf")
		out := bdProxiedPruneFail(t, bd, p.dir)
		if !strings.Contains(out, "requires --older-than or --pattern") {
			t.Errorf("expected requires-filter error, got: %s", out)
		}
	})

	t.Run("dry_run_deletes_nothing", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pdr")
		issue := bdProxiedCreate(t, bd, p.dir, "Prune me")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		out := bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--dry-run")
		if !strings.Contains(out, "Would prune") {
			t.Errorf("dry-run output missing 'Would prune': %s", out)
		}
		db := openProxiedDB(t, p)
		if !issueRowExists(t, db, issue.ID) {
			t.Errorf("dry-run must not delete %s", issue.ID)
		}
	})

	t.Run("force_deletes_closed", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pfd")
		issue := bdProxiedCreate(t, bd, p.dir, "Delete me")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--force")
		db := openProxiedDB(t, p)
		if issueRowExists(t, db, issue.ID) {
			t.Errorf("closed bead %s should be pruned", issue.ID)
		}
	})

	t.Run("skips_open", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pso")
		open := bdProxiedCreate(t, bd, p.dir, "Still open")
		closed := bdProxiedCreate(t, bd, p.dir, "Now closed")
		bdProxiedClose(t, bd, p.dir, closed.ID)
		bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--force")
		db := openProxiedDB(t, p)
		if !issueRowExists(t, db, open.ID) {
			t.Errorf("open bead %s must survive prune", open.ID)
		}
		if issueRowExists(t, db, closed.ID) {
			t.Errorf("closed bead %s should be pruned", closed.ID)
		}
	})

	t.Run("reference_aware_skip", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pras")
		referenced := bdProxiedCreate(t, bd, p.dir, "Cited decision")
		_ = bdProxiedCreate(t, bd, p.dir, "Open citer",
			"--description", "per "+referenced.ID+" we decided X")
		bdProxiedClose(t, bd, p.dir, referenced.ID)

		out := bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--force")
		if !strings.Contains(out, "protected by open-bead references") {
			t.Errorf("expected referenced-protection note, got: %s", out)
		}
		db := openProxiedDB(t, p)
		if !issueRowExists(t, db, referenced.ID) {
			t.Errorf("referenced closed bead %s must be protected from prune", referenced.ID)
		}
	})

	t.Run("ignore_references_deletes_referenced", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pir")
		referenced := bdProxiedCreate(t, bd, p.dir, "Cited decision")
		_ = bdProxiedCreate(t, bd, p.dir, "Open citer",
			"--description", "per "+referenced.ID+" we decided X")
		bdProxiedClose(t, bd, p.dir, referenced.ID)

		bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--force", "--ignore-references")
		db := openProxiedDB(t, p)
		if issueRowExists(t, db, referenced.ID) {
			t.Errorf("--ignore-references should delete referenced bead %s", referenced.ID)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pjo")
		a := bdProxiedCreate(t, bd, p.dir, "JSON prune A")
		b := bdProxiedCreate(t, bd, p.dir, "JSON prune B")
		bdProxiedClose(t, bd, p.dir, a.ID, b.ID)
		referenced := bdProxiedCreate(t, bd, p.dir, "JSON cited decision")
		bdProxiedCreate(t, bd, p.dir, "JSON open citing bead",
			"--description", "per "+referenced.ID+" we decided X")
		bdProxiedClose(t, bd, p.dir, referenced.ID)
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "prune", "--json", "--pattern", "*", "--force")
		if err != nil {
			t.Fatalf("bd prune --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON object in prune output:\n%s", stdout)
		}
		var stats map[string]any
		if err := json.Unmarshal([]byte(stdout[start:]), &stats); err != nil {
			t.Fatalf("parse prune JSON: %v\nraw: %s", err, stdout[start:])
		}
		if got, ok := stats["pruned_count"].(float64); !ok || int(got) != 2 {
			t.Errorf("pruned_count: got %v, want 2", stats["pruned_count"])
		}
		if got, ok := stats["referenced_skipped"].(float64); !ok || int(got) != 1 {
			t.Errorf("referenced_skipped: got %v, want 1", stats["referenced_skipped"])
		}
		sample, ok := stats["referenced_ids_sample"].([]any)
		if !ok || len(sample) != 1 || sample[0] != referenced.ID {
			t.Errorf("referenced_ids_sample: got %v, want [%s]", stats["referenced_ids_sample"], referenced.ID)
		}
	})

	t.Run("commits_deletion", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "pcd")
		issue := bdProxiedCreate(t, bd, p.dir, "Commit prune")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		db := openProxiedDB(t, p)
		before := readDoltHead(t, db)
		bdProxiedPrune(t, bd, p.dir, "--pattern", "*", "--force")
		if got := readDoltLogCountSince(t, db, before); got < 1 {
			t.Errorf("prune should create a dolt commit, got %d new commits", got)
		}
		msg := readDoltLogTopMessage(t, db)
		if !strings.HasPrefix(msg, "bd: prune ") {
			t.Errorf("dolt commit message should begin with 'bd: prune ', got: %q", msg)
		}
	})
}
