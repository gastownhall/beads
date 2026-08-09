//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestProxiedServerDelete pins the CONVERGED semantics on a team server.
//
// This route used to hardcode cascade at both of its call sites and refuse the
// --cascade flag outright, so `bd delete X --force` deleted X's whole subtree.
// The three modes below are the direct route's, unchanged, checked from the far
// side of a real Dolt sql-server.
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

	// Embedded parity (bd-paurh): with external dependents and neither
	// --cascade nor --force, delete refuses and tells the caller how to
	// proceed instead of silently cascading.
	refusal := bdProxiedDeleteFail(t, bd, p.dir, target.ID)
	if !strings.Contains(refusal, "has dependents not in deletion set") ||
		!strings.Contains(refusal, "--cascade") || !strings.Contains(refusal, "--force") {
		t.Errorf("bare delete with dependents: got %q, want refusal naming --cascade/--force", refusal)
	}

	preview := bdProxiedDeleteJSON(t, bd, p.dir, "--json", "--cascade", target.ID)
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
		"cascade":              true,
		"would_orphan":         float64(0),
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

	deleted := bdProxiedDeleteJSON(t, bd, p.dir, "--json", "--cascade", target.ID, "--force")
	deletedWant := map[string]any{
		"schema_version":       float64(1),
		"deleted":              []any{target.ID},
		"deleted_count":        float64(3),
		"dependencies_removed": float64(3),
		"labels_removed":       float64(1),
		"events_removed":       float64(4),
		"references_updated":   float64(1),
		"orphaned_issues":      nil,
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

// TestProxiedServerDeleteForceOrphans pins the embedded-parity --force
// semantics (bd-paurh): without --cascade, --force deletes ONLY the named IDs,
// orphans external dependents, and cleans up the dependency links touching the
// deleted rows.
func TestProxiedServerDeleteForceOrphans(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "delfo")

	target := bdProxiedCreate(t, bd, p.dir, "Orphan target", "--type", "task")
	dependent := bdProxiedCreate(t, bd, p.dir, "Orphaned dependent", "--type", "task", "--deps", "depends-on:"+target.ID)
	descendant := bdProxiedCreate(t, bd, p.dir, "Orphan descendant", "--type", "task", "--parent", dependent.ID)

	deleted := bdProxiedDeleteJSON(t, bd, p.dir, "--json", target.ID, "--force")
	if got, want := deleted["deleted_count"], float64(1); got != want {
		t.Errorf("deleted_count: got %v, want %v (force without cascade must delete only the named ID)", got, want)
	}
	if got, want := deleted["orphaned_issues"], []any{dependent.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("orphaned_issues: got %#v, want %#v", got, want)
	}

	db := openProxiedDB(t, p)
	ctx := context.Background()
	assertRowAbsent(t, db, "issues", target.ID)
	assertRowExists(t, db, "issues", dependent.ID)
	assertRowExists(t, db, "issues", descendant.ID)

	var depRows int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? OR depends_on_issue_id = ?",
		target.ID, target.ID).Scan(&depRows); err != nil {
		t.Fatalf("count dependency rows touching deleted id: %v", err)
	}
	if depRows != 0 {
		t.Errorf("dependency links touching deleted %s: got %d, want 0 (orphan cleanup)", target.ID, depRows)
	}
	var survivingDeps int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", descendant.ID).Scan(&survivingDeps); err != nil {
		t.Fatalf("count surviving dependency rows: %v", err)
	}
	if survivingDeps != 1 {
		t.Errorf("descendant->dependent link: got %d rows, want 1 (untouched)", survivingDeps)
	}
}

// The blocked refusal in --json mode must put exactly ONE JSON document on
// stdout — the preview payload, carrying the refusal in its "error" key. The
// jsonStdoutError doc used to be emitted on top of it (#5371 review).
func TestProxiedServerDeleteBlockedJSONSingleDoc(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "delete-blocked-json")

	target := bdProxiedCreate(t, bd, p.dir, "Blocked target", "--type", "task")
	bdProxiedCreate(t, bd, p.dir, "External dependent", "--type", "task", "--deps", "depends-on:"+target.ID)

	stdout, stderr, err := bdProxiedDeleteRaw(t, bd, p.dir, "--json", target.ID)
	if err == nil {
		t.Fatalf("blocked --json delete should fail; stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("no JSON object in blocked --json output:\n%s", stdout)
	}
	dec := json.NewDecoder(strings.NewReader(stdout[start:]))
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("parse blocked --json payload: %v\nraw: %s", err, stdout[start:])
	}
	errMsg, _ := payload["error"].(string)
	if !strings.Contains(errMsg, "has dependents not in deletion set") {
		t.Errorf("blocked --json payload error: got %q, want the dependents refusal", errMsg)
	}
	var extra any
	if decErr := dec.Decode(&extra); decErr != io.EOF {
		t.Errorf("blocked --json emitted more than one JSON doc (second decode: err=%v doc=%v)\nraw: %s",
			decErr, extra, stdout[start:])
	}
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
