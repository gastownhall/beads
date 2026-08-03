//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestProxiedServerDelete(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "delete")

	survivor := bdProxiedCreate(t, bd, p.dir, "Survivor", "--type", "task")
	target := bdProxiedCreate(t, bd, p.dir, "Target", "--type", "task", "--label", "alpha", "--deps", "depends-on:"+survivor.ID)
	dependent := bdProxiedCreate(t, bd, p.dir, "Dependent", "--type", "task", "--deps", "depends-on:"+target.ID)
	descendant := bdProxiedCreate(t, bd, p.dir, "Descendant", "--type", "task", "--parent", dependent.ID)
	bdProxiedUpdateOne(t, bd, p.dir, survivor.ID, "--description", "see "+target.ID+" for context")

	db := openProxiedDB(t, p)
	ctx := context.Background()
	var headBefore string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&headBefore); err != nil {
		t.Fatalf("read HEAD before preview: %v", err)
	}

	preview := bdProxiedDeleteJSON(t, bd, p.dir, "--json", target.ID)
	previewWant := map[string]any{
		"schema_version":       float64(1),
		"would_delete":         float64(3),
		"dependencies_removed": float64(3),
		"labels_removed":       float64(1),
		"events_removed":       float64(4),
		"ids":                  []any{target.ID},
		"not_found":            nil,
		"connected":            []any{survivor.ID},
		"dry_run":              false,
	}
	if !deleteJSONEqual(preview, previewWant) {
		t.Errorf("preview JSON: got %#v, want %#v", preview, previewWant)
	}
	for _, id := range []string{survivor.ID, target.ID, dependent.ID, descendant.ID} {
		assertRowExists(t, db, "issues", id)
	}
	var headAfterPreview string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&headAfterPreview); err != nil {
		t.Fatalf("read HEAD after preview: %v", err)
	}
	if headAfterPreview != headBefore {
		t.Errorf("preview advanced HEAD: before=%s after=%s", headBefore, headAfterPreview)
	}

	deleted := bdProxiedDeleteJSON(t, bd, p.dir, "--json", target.ID, "--force")
	deletedWant := map[string]any{
		"schema_version":       float64(1),
		"deleted":              []any{target.ID},
		"deleted_count":        float64(3),
		"dependencies_removed": float64(3),
		"labels_removed":       float64(1),
		"events_removed":       float64(4),
		"references_updated":   float64(1),
	}
	if !deleteJSONEqual(deleted, deletedWant) {
		t.Errorf("delete JSON: got %#v, want %#v", deleted, deletedWant)
	}

	for _, id := range []string{target.ID, dependent.ID, descendant.ID} {
		assertRowAbsent(t, db, "issues", id)
		for _, q := range []struct{ table, where string }{
			{"labels", "issue_id = ?"},
			{"events", "issue_id = ?"},
			{"dependencies", "issue_id = ? OR depends_on_issue_id = ?"},
		} {
			var count int
			args := []any{id}
			if strings.Count(q.where, "?") == 2 {
				args = append(args, id)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+q.table+" WHERE "+q.where, args...).Scan(&count); err != nil {
				t.Fatalf("count %s rows for %s: %v", q.table, id, err)
			}
			if count != 0 {
				t.Errorf("%s rows for deleted %s: got %d, want 0", q.table, id, count)
			}
		}
	}
	assertRowExists(t, db, "issues", survivor.ID)
	var description string
	if err := db.QueryRowContext(ctx, "SELECT description FROM issues WHERE id = ?", survivor.ID).Scan(&description); err != nil {
		t.Fatalf("read survivor description: %v", err)
	}
	if !strings.Contains(description, "[deleted:"+target.ID+"]") {
		t.Errorf("survivor description: got %q, want rewritten target reference", description)
	}
	var headAfterDelete string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&headAfterDelete); err != nil {
		t.Fatalf("read HEAD after delete: %v", err)
	}
	if headAfterDelete == headBefore {
		t.Errorf("HEAD did not advance: before=%s after=%s", headBefore, headAfterDelete)
	}

	missing := "delete-missing-id"
	missingOut := bdProxiedDeleteFail(t, bd, p.dir, missing, "--force")
	if !strings.Contains(strings.ToLower(missingOut), "not found") || !strings.Contains(missingOut, missing) {
		t.Errorf("missing-ID error: got %q, want not-found translation naming %q", missingOut, missing)
	}
}

func deleteJSONEqual(got, want map[string]any) bool {
	return reflect.DeepEqual(got, want)
}

func TestProxiedServerDeleteWisp(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "dwr")
	wisp := bdProxiedCreate(t, bd, p.dir, "Wisp delete routing", "--ephemeral")
	db := openProxiedDB(t, p)
	assertRowExists(t, db, "wisps", wisp.ID)

	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')",
		wisp.ID, "shadow row"); err != nil {
		t.Fatalf("seed shadow issues row: %v", err)
	}
	assertRowExists(t, db, "issues", wisp.ID)
	if _, err := db.ExecContext(context.Background(), "CALL DOLT_COMMIT('-Am', 'seed shadow issue')"); err != nil {
		t.Fatalf("commit shadow issues row: %v", err)
	}

	var headBefore string
	if err := db.QueryRowContext(context.Background(), "SELECT HASHOF('HEAD')").Scan(&headBefore); err != nil {
		t.Fatalf("read HEAD before: %v", err)
	}

	out := bdProxiedDelete(t, bd, p.dir, "--json", wisp.ID, "--force")
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in delete output:\n%s", out)
	}
	var result struct {
		SchemaVersion int      `json:"schema_version"`
		Deleted       []string `json:"deleted"`
		DeletedCount  int      `json:"deleted_count"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &result); err != nil {
		t.Fatalf("parse delete JSON: %v\nraw: %s", err, out[start:])
	}
	if result.SchemaVersion != JSONSchemaVersion || !reflect.DeepEqual(result.Deleted, []string{wisp.ID}) || result.DeletedCount != 1 {
		t.Errorf("delete JSON: got %+v, want schema_version=%d deleted=[%s] deleted_count=1",
			result, JSONSchemaVersion, wisp.ID)
	}

	assertRowAbsent(t, db, "wisps", wisp.ID)
	assertRowExists(t, db, "issues", wisp.ID)

	var headAfter string
	if err := db.QueryRowContext(context.Background(), "SELECT HASHOF('HEAD')").Scan(&headAfter); err != nil {
		t.Fatalf("read HEAD after: %v", err)
	}
	if headAfter != headBefore {
		t.Errorf("HEAD advanced for a wisp-only delete (wisps are dolt_ignored): before=%s after=%s", headBefore, headAfter)
	}
}

func TestProxiedServerDeleteConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "ddc")
	issue := bdProxiedCreate(t, bd, p.dir, "Concurrent delete contest", "-t", "task")

	const n = 5
	type result struct {
		idx      int
		exitErr  error
		combined string
	}
	results := make([]result, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, stderr, err := bdProxiedDeleteRaw(t, bd, p.dir, issue.ID, "--force")
			results[i] = result{idx: i, exitErr: err, combined: stdout + stderr}
		}()
	}
	wg.Wait()

	var winners []int
	var conflicts int
	for _, r := range results {
		if r.exitErr == nil {
			winners = append(winners, r.idx)
			continue
		}
		isNotFound := strings.Contains(strings.ToLower(r.combined), "not found")
		isSerializationFailure := strings.Contains(r.combined, "serialization failure") ||
			strings.Contains(r.combined, "Error 1213")
		if isNotFound || isSerializationFailure {
			conflicts++
			continue
		}
		t.Errorf("unexpected failure for goroutine %d: err=%v combined=%s",
			r.idx, r.exitErr, r.combined)
	}

	if len(winners) < 1 {
		t.Errorf("expected at least one winner, got 0")
	}
	if len(winners)+conflicts != n {
		t.Errorf("winners (%d) + conflicts (%d) != n (%d) — some goroutine had an unexpected failure",
			len(winners), conflicts, n)
	}

	db := openProxiedDB(t, p)
	assertRowAbsent(t, db, "issues", issue.ID)
}

func bdProxiedDelete(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"delete"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd delete %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedDeleteJSON(t *testing.T, bd, dir string, args ...string) map[string]any {
	t.Helper()
	out := bdProxiedDelete(t, bd, dir, args...)
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in delete output:\n%s", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &got); err != nil {
		t.Fatalf("parse delete JSON: %v\nraw: %s", err, out[start:])
	}
	return got
}

func bdProxiedDeleteRaw(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	fullArgs := append([]string{"delete"}, args...)
	return bdProxiedRunBuffers(t, bd, dir, fullArgs...)
}

func bdProxiedDeleteFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedDeleteRaw(t, bd, dir, args...)
	if err == nil {
		t.Fatalf("bd delete %s should have failed; got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}
