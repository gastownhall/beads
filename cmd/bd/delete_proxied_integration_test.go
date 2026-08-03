//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("delete_mixed_wisp_and_issue_partition", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dmp")
		issue := bdProxiedCreate(t, bd, p.dir, "Regular target", "-t", "task")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp target", "--ephemeral")

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "issues", issue.ID)
		assertRowExists(t, db, "wisps", wisp.ID)

		bdProxiedDelete(t, bd, p.dir, issue.ID, wisp.ID, "--force")

		assertRowAbsent(t, db, "issues", issue.ID)
		assertRowAbsent(t, db, "wisps", wisp.ID)
	})

	t.Run("delete_wisp_clears_wisp_aux_tables", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwc")
		a := bdProxiedCreate(t, bd, p.dir, "Wisp aux A", "--ephemeral")
		_ = bdProxiedCreate(t, bd, p.dir, "Wisp aux B", "--ephemeral",
			"--deps", "depends-on:"+a.ID)
		bdProxiedUpdateOne(t, bd, p.dir, a.ID, "--add-label", "alpha")

		bdProxiedDelete(t, bd, p.dir, a.ID, "--force")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		assertRowAbsent(t, db, "wisps", a.ID)

		for _, q := range []struct {
			table, where string
		}{
			{"wisp_labels", "issue_id = ?"},
			{"wisp_events", "issue_id = ?"},
			{"wisp_dependencies", "issue_id = ? OR depends_on_wisp_id = ?"},
		} {
			var count int
			args := []any{a.ID}
			if strings.Count(q.where, "?") == 2 {
				args = append(args, a.ID)
			}
			query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", q.table, q.where)
			if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
				t.Fatalf("count %s for %s: %v", q.table, a.ID, err)
			}
			if count != 0 {
				t.Errorf("%s rows for deleted wisp %s: got %d, want 0", q.table, a.ID, count)
			}
		}
	})

	t.Run("delete_wisp_routes_to_wisps_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp delete routing", "--ephemeral")

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "wisps", wisp.ID)
		assertRowAbsent(t, db, "issues", wisp.ID)

		if _, err := db.ExecContext(context.Background(),
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')",
			wisp.ID, "shadow row"); err != nil {
			t.Fatalf("seed shadow issues row: %v", err)
		}
		assertRowExists(t, db, "issues", wisp.ID)

		bdProxiedDelete(t, bd, p.dir, wisp.ID, "--force")

		assertRowAbsent(t, db, "wisps", wisp.ID)
		assertRowExists(t, db, "issues", wisp.ID)
	})

	t.Run("delete_wisp_batch", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwb")
		a := bdProxiedCreate(t, bd, p.dir, "Wisp batch 1", "--ephemeral")
		b := bdProxiedCreate(t, bd, p.dir, "Wisp batch 2", "--ephemeral")
		c := bdProxiedCreate(t, bd, p.dir, "Wisp batch 3", "--ephemeral")

		bdProxiedDelete(t, bd, p.dir, a.ID, b.ID, c.ID, "--force")

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "wisps", id)
		}
	})

	t.Run("delete_wisp_cascades_dependents", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwc")
		parent := bdProxiedCreate(t, bd, p.dir, "Wisp parent", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Wisp child", "--ephemeral",
			"--deps", "depends-on:"+parent.ID)

		bdProxiedDelete(t, bd, p.dir, parent.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "wisps", parent.ID)
		assertRowAbsent(t, db, "wisps", child.ID)
	})

	t.Run("delete_wisp_cascade_spans_all_dep_types", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dws")
		a := bdProxiedCreate(t, bd, p.dir, "Wisp A", "--ephemeral")
		b := bdProxiedCreate(t, bd, p.dir, "Wisp B", "--ephemeral",
			"--deps", "depends-on:"+a.ID)
		c := bdProxiedCreate(t, bd, p.dir, "Wisp C", "--ephemeral",
			"--parent", b.ID)

		bdProxiedDelete(t, bd, p.dir, a.ID, "--force")

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "wisps", id)
		}
	})

	t.Run("delete_wisp_skips_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwdc")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp commit skip", "--ephemeral")

		db := openProxiedDB(t, p)
		var before string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&before); err != nil {
			t.Fatalf("read HEAD before: %v", err)
		}

		bdProxiedDelete(t, bd, p.dir, wisp.ID, "--force")

		var after string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&after); err != nil {
			t.Fatalf("read HEAD after: %v", err)
		}
		if after != before {
			t.Errorf("HEAD advanced for a wisp-only delete (wisps are dolt_ignored): before=%s after=%s",
				before, after)
		}
	})

	t.Run("delete_wisp_dry_run_does_not_mutate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwdr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp dry-run target", "--ephemeral")

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", wisp.ID, "--dry-run")
		if _, ok := got["would_delete"]; !ok {
			t.Errorf("dry-run JSON missing `would_delete`; got keys: %v", mapKeys(got))
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "wisps", wisp.ID)
	})

	t.Run("delete_wisp_rewrites_text_references", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwrt")
		neighbor := bdProxiedCreate(t, bd, p.dir, "Wisp neighbor", "--ephemeral")
		target := bdProxiedCreate(t, bd, p.dir, "Wisp target", "--ephemeral",
			"--deps", "depends-on:"+neighbor.ID)
		bdProxiedUpdateOne(t, bd, p.dir, neighbor.ID, "--description", "see "+target.ID+" for context")

		bdProxiedDelete(t, bd, p.dir, target.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "wisps", target.ID)
		assertRowExists(t, db, "wisps", neighbor.ID)

		var desc string
		if err := db.QueryRowContext(context.Background(),
			"SELECT description FROM wisps WHERE id = ?", neighbor.ID).Scan(&desc); err != nil {
			t.Fatalf("read wisp neighbor description: %v", err)
		}
		want := "[deleted:" + target.ID + "]"
		if !strings.Contains(desc, want) {
			t.Errorf("wisp neighbor description: got %q, want substring %q", desc, want)
		}
	})

	t.Run("delete_wisp_nonexistent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dwn")
		out := bdProxiedDeleteFail(t, bd, p.dir, "dwn-doesnotexist", "--force")
		if !strings.Contains(strings.ToLower(out), "not found") {
			t.Errorf("expected `not found` error for bogus wisp id, got: %s", out)
		}
	})
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

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
