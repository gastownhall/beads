//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestProxiedServerDelete(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	t.Run("delete_single_issue", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dsi")
		issue := bdProxiedCreate(t, bd, p.dir, "Delete me", "-t", "task")
		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")
		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", issue.ID)
	})

	t.Run("delete_without_force_shows_preview", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dwf")
		issue := bdProxiedCreate(t, bd, p.dir, "Preview only", "-t", "task")

		out := bdProxiedDelete(t, bd, p.dir, issue.ID)
		if !strings.Contains(out, "PREVIEW") && !strings.Contains(out, "preview") {
			t.Errorf("expected preview prose, got: %s", out)
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "issues", issue.ID)
	})

	t.Run("delete_clears_aux_tables", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dcat")
		neighbor := bdProxiedCreate(t, bd, p.dir, "Neighbor", "-t", "task")
		issue := bdProxiedCreate(t, bd, p.dir, "Aux cleanup", "-t", "task",
			"--label", "alpha",
			"--deps", "depends-on:"+neighbor.ID)

		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		assertRowAbsent(t, db, "issues", issue.ID)

		for _, q := range []struct {
			table, where string
		}{
			{"labels", "issue_id = ?"},
			{"events", "issue_id = ?"},
			{"dependencies", "issue_id = ? OR depends_on_issue_id = ?"},
		} {
			var count int
			args := []any{issue.ID}
			if strings.Count(q.where, "?") == 2 {
				args = append(args, issue.ID)
			}
			query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", q.table, q.where)
			if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
				t.Fatalf("count %s for %s: %v", q.table, issue.ID, err)
			}
			if count != 0 {
				t.Errorf("%s rows for deleted %s: got %d, want 0", q.table, issue.ID, count)
			}
		}
	})

	t.Run("delete_json_returns_result_shape", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "djs")
		issue := bdProxiedCreate(t, bd, p.dir, "JSON shape", "-t", "task")

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", issue.ID, "--force")
		for _, key := range []string{
			"deleted", "deleted_count", "dependencies_removed",
			"labels_removed", "events_removed", "references_updated",
		} {
			if _, ok := got[key]; !ok {
				t.Errorf("delete JSON output missing key %q; got keys: %v", key, mapKeys(got))
			}
		}

		deleted, ok := got["deleted"].([]any)
		if !ok {
			t.Fatalf("`deleted` is not a slice; got %T: %v", got["deleted"], got["deleted"])
		}
		if len(deleted) != 1 || deleted[0] != issue.ID {
			t.Errorf("`deleted`: got %v, want [%s]", deleted, issue.ID)
		}
	})

	t.Run("delete_leaf_clears_outbound_dep_rows", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dlc")
		a := bdProxiedCreate(t, bd, p.dir, "Survivor A", "-t", "task")
		b := bdProxiedCreate(t, bd, p.dir, "Will be deleted", "-t", "task",
			"--deps", "depends-on:"+a.ID)

		bdProxiedDelete(t, bd, p.dir, b.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", b.ID)
		assertRowExists(t, db, "issues", a.ID)

		var refs int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? OR depends_on_issue_id = ?",
			b.ID, b.ID).Scan(&refs); err != nil {
			t.Fatalf("count dep rows referencing %s: %v", b.ID, err)
		}
		if refs != 0 {
			t.Errorf("dependencies referencing deleted %s: got %d rows, want 0", b.ID, refs)
		}
	})

	t.Run("delete_creates_dolt_commit", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "ddc")
		issue := bdProxiedCreate(t, bd, p.dir, "Dolt commit test", "-t", "task")

		db := openProxiedDB(t, p)
		var before string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&before); err != nil {
			t.Fatalf("read HEAD before: %v", err)
		}

		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

		var after string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&after); err != nil {
			t.Fatalf("read HEAD after: %v", err)
		}
		if after == before {
			t.Errorf("HEAD did not advance: before=%s after=%s (uw.Commit should produce a Dolt commit)",
				before, after)
		}
	})

	t.Run("delete_bulk_no_resurrection", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dbr")

		const total = 20
		const toDelete = 10
		ids := make([]string, 0, total)
		for i := 0; i < total; i++ {
			issue := bdProxiedCreate(t, bd, p.dir,
				fmt.Sprintf("Bulk %d", i), "-t", "task")
			ids = append(ids, issue.ID)
		}

		deleteArgs := append([]string{}, ids[:toDelete]...)
		deleteArgs = append(deleteArgs, "--force")
		bdProxiedDelete(t, bd, p.dir, deleteArgs...)

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if count != total-toDelete {
			t.Errorf("issues count after bulk delete: got %d, want %d", count, total-toDelete)
		}
		for _, id := range ids[:toDelete] {
			assertRowAbsent(t, db, "issues", id)
		}
		for _, id := range ids[toDelete:] {
			assertRowExists(t, db, "issues", id)
		}
	})

	t.Run("delete_nonexistent", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dne")
		out := bdProxiedDeleteFail(t, bd, p.dir, "dne-doesnotexist", "--force")
		if !strings.Contains(strings.ToLower(out), "not found") &&
			!strings.Contains(strings.ToLower(out), "error") {
			t.Errorf("expected not-found / error message, got: %s", out)
		}
	})

	t.Run("delete_batch", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dbt")
		a := bdProxiedCreate(t, bd, p.dir, "Batch 1", "-t", "task")
		b := bdProxiedCreate(t, bd, p.dir, "Batch 2", "-t", "task")
		c := bdProxiedCreate(t, bd, p.dir, "Batch 3", "-t", "task")

		bdProxiedDelete(t, bd, p.dir, a.ID, b.ID, c.ID, "--force")

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "issues", id)
		}
	})

	t.Run("delete_force_cascades_dependents", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dfc")
		parent := bdProxiedCreate(t, bd, p.dir, "Force parent", "-t", "task")
		child := bdProxiedCreate(t, bd, p.dir, "Force child", "-t", "task",
			"--deps", "depends-on:"+parent.ID)

		bdProxiedDelete(t, bd, p.dir, parent.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", parent.ID)
		assertRowAbsent(t, db, "issues", child.ID)
	})

	t.Run("delete_cleans_up_dependencies", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dcd")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "-t", "task")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "-t", "task",
			"--deps", "depends-on:"+parent.ID)

		bdProxiedDelete(t, bd, p.dir, child.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", child.ID)
		assertRowExists(t, db, "issues", parent.ID)

		var status string
		if err := db.QueryRowContext(context.Background(),
			"SELECT status FROM issues WHERE id = ?", parent.ID).Scan(&status); err != nil {
			t.Fatalf("read parent status: %v", err)
		}
		if status == "closed" {
			t.Errorf("parent status after deleting child: got %q, want non-closed", status)
		}
	})

	t.Run("delete_always_cascades_dependents", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dac")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "-t", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "-t", "task",
			"--parent", parent.ID)

		bdProxiedDelete(t, bd, p.dir, parent.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", parent.ID)
		assertRowAbsent(t, db, "issues", child.ID)
	})

	t.Run("delete_cascade_spans_all_dep_types", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dcs")
		a := bdProxiedCreate(t, bd, p.dir, "A", "-t", "task")
		b := bdProxiedCreate(t, bd, p.dir, "B", "-t", "task",
			"--deps", "depends-on:"+a.ID)
		c := bdProxiedCreate(t, bd, p.dir, "C", "-t", "task",
			"--parent", b.ID)

		bdProxiedDelete(t, bd, p.dir, a.ID, "--force")

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "issues", id)
		}
	})

	t.Run("delete_json_counts_match_cascade", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "djc")
		a := bdProxiedCreate(t, bd, p.dir, "Chain A", "-t", "task")
		b := bdProxiedCreate(t, bd, p.dir, "Chain B", "-t", "task",
			"--deps", "depends-on:"+a.ID)
		c := bdProxiedCreate(t, bd, p.dir, "Chain C", "-t", "task",
			"--deps", "depends-on:"+b.ID)

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", a.ID, "--force")

		count, ok := got["deleted_count"].(float64)
		if !ok {
			t.Fatalf("deleted_count not a number: %T %v", got["deleted_count"], got["deleted_count"])
		}
		if int(count) != 3 {
			t.Errorf("deleted_count: got %v, want 3 (A,B,C cascade)", count)
		}

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "issues", id)
		}
	})

	t.Run("delete_dry_run_does_not_mutate", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "ddr")
		issue := bdProxiedCreate(t, bd, p.dir, "Dry run target", "-t", "task")

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", issue.ID, "--dry-run")
		if _, ok := got["would_delete"]; !ok {
			t.Errorf("dry-run JSON missing `would_delete`; got keys: %v", mapKeys(got))
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "issues", issue.ID)
	})

	t.Run("delete_preview_lists_not_found", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dpn")
		issue := bdProxiedCreate(t, bd, p.dir, "Preview NotFound", "-t", "task")

		out := bdProxiedDeleteFail(t, bd, p.dir, issue.ID, "dpn-bogus")
		if !strings.Contains(strings.ToLower(out), "not found") {
			t.Errorf("expected `not found` in preview error, got: %s", out)
		}
		if !strings.Contains(out, "dpn-bogus") {
			t.Errorf("expected bogus id in preview error, got: %s", out)
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "issues", issue.ID)
	})

	t.Run("delete_preview_lists_connected", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dpc")
		neighbor := bdProxiedCreate(t, bd, p.dir, "Preview neighbor", "-t", "task")
		target := bdProxiedCreate(t, bd, p.dir, "Preview target", "-t", "task",
			"--deps", "depends-on:"+neighbor.ID)

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", target.ID)

		connectedRaw, ok := got["connected"]
		if !ok {
			t.Fatalf("preview JSON missing `connected` key; got keys: %v", mapKeys(got))
		}
		connected, ok := connectedRaw.([]any)
		if !ok {
			t.Fatalf("`connected` is not a slice; got %T", connectedRaw)
		}
		var found bool
		for _, v := range connected {
			if s, ok := v.(string); ok && s == neighbor.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("connected does not contain neighbor %q: %v", neighbor.ID, connected)
		}
	})

	t.Run("delete_rewrites_text_references", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "drt")
		neighbor := bdProxiedCreate(t, bd, p.dir, "Rewrite neighbor", "-t", "task")
		target := bdProxiedCreate(t, bd, p.dir, "Rewrite target", "-t", "task",
			"--deps", "depends-on:"+neighbor.ID)
		bdProxiedUpdateOne(t, bd, p.dir, neighbor.ID, "--description", "see "+target.ID+" for context")

		bdProxiedDelete(t, bd, p.dir, target.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", target.ID)
		assertRowExists(t, db, "issues", neighbor.ID)

		var desc string
		if err := db.QueryRowContext(context.Background(),
			"SELECT description FROM issues WHERE id = ?", neighbor.ID).Scan(&desc); err != nil {
			t.Fatalf("read neighbor description: %v", err)
		}
		want := "[deleted:" + target.ID + "]"
		if !strings.Contains(desc, want) {
			t.Errorf("neighbor description: got %q, want substring %q", desc, want)
		}
	})

	t.Run("delete_references_updated_count_reported", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dru")
		neighbor := bdProxiedCreate(t, bd, p.dir, "Refs neighbor", "-t", "task")
		target := bdProxiedCreate(t, bd, p.dir, "Refs target", "-t", "task",
			"--deps", "depends-on:"+neighbor.ID)
		bdProxiedUpdateOne(t, bd, p.dir, neighbor.ID, "--description", "see "+target.ID)

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", target.ID, "--force")
		refs, ok := got["references_updated"].(float64)
		if !ok {
			t.Fatalf("references_updated not a number: %T %v",
				got["references_updated"], got["references_updated"])
		}
		if int(refs) < 1 {
			t.Errorf("references_updated: got %v, want >= 1", refs)
		}
	})

	t.Run("delete_zero_aux_rows_after", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dza")
		outbound := bdProxiedCreate(t, bd, p.dir, "Outbound dependee", "-t", "task")
		issue := bdProxiedCreate(t, bd, p.dir, "Zero aux target", "-t", "task",
			"--label", "alpha",
			"--deps", "depends-on:"+outbound.ID)
		_ = bdProxiedCreate(t, bd, p.dir, "Inbound depender", "-t", "task",
			"--deps", "depends-on:"+issue.ID)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--add-label", "beta")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "-s", "in_progress")

		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		assertRowAbsent(t, db, "issues", issue.ID)

		for _, q := range []struct {
			table, where string
		}{
			{"labels", "issue_id = ?"},
			{"events", "issue_id = ?"},
			{"dependencies", "issue_id = ? OR depends_on_issue_id = ?"},
		} {
			var count int
			args := []any{issue.ID}
			if strings.Count(q.where, "?") == 2 {
				args = append(args, issue.ID)
			}
			query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", q.table, q.where)
			if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
				t.Fatalf("count %s for %s: %v", q.table, issue.ID, err)
			}
			if count != 0 {
				t.Errorf("%s rows for deleted %s: got %d, want 0", q.table, issue.ID, count)
			}
		}
	})

	t.Run("delete_from_file", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dff")
		a := bdProxiedCreate(t, bd, p.dir, "From-file 1", "-t", "task")
		b := bdProxiedCreate(t, bd, p.dir, "From-file 2", "-t", "task")
		c := bdProxiedCreate(t, bd, p.dir, "From-file 3", "-t", "task")

		idsPath := filepath.Join(p.dir, "ids.txt")
		body := strings.Join([]string{a.ID, b.ID, c.ID}, "\n") + "\n"
		if err := os.WriteFile(idsPath, []byte(body), 0o600); err != nil {
			t.Fatalf("write ids file: %v", err)
		}

		bdProxiedDelete(t, bd, p.dir, "--from-file", idsPath, "--force")

		db := openProxiedDB(t, p)
		for _, id := range []string{a.ID, b.ID, c.ID} {
			assertRowAbsent(t, db, "issues", id)
		}
	})

	t.Run("delete_cascade_flag_is_error", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dcn")
		issue := bdProxiedCreate(t, bd, p.dir, "Cascade flag", "-t", "task")

		out := bdProxiedDeleteFail(t, bd, p.dir, issue.ID, "--force", "--cascade")
		if !strings.Contains(out, "--cascade") {
			t.Errorf("expected error mentioning --cascade, got: %s", out)
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "issues", issue.ID)
	})
}

func TestProxiedServerDeleteWisp(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	t.Run("delete_mixed_wisp_and_issue_partition", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dmp")
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
		p := bdProxiedInit(t, bd, "dwc")
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
		p := bdProxiedInit(t, bd, "dwr")
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
		p := bdProxiedInit(t, bd, "dwb")
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
		p := bdProxiedInit(t, bd, "dwc")
		parent := bdProxiedCreate(t, bd, p.dir, "Wisp parent", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Wisp child", "--ephemeral",
			"--deps", "depends-on:"+parent.ID)

		bdProxiedDelete(t, bd, p.dir, parent.ID, "--force")

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "wisps", parent.ID)
		assertRowAbsent(t, db, "wisps", child.ID)
	})

	t.Run("delete_wisp_cascade_spans_all_dep_types", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dws")
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
		p := bdProxiedInit(t, bd, "dwdc")
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
		p := bdProxiedInit(t, bd, "dwdr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp dry-run target", "--ephemeral")

		got := bdProxiedDeleteJSON(t, bd, p.dir, "--json", wisp.ID, "--dry-run")
		if _, ok := got["would_delete"]; !ok {
			t.Errorf("dry-run JSON missing `would_delete`; got keys: %v", mapKeys(got))
		}

		db := openProxiedDB(t, p)
		assertRowExists(t, db, "wisps", wisp.ID)
	})

	t.Run("delete_wisp_rewrites_text_references", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "dwrt")
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
		p := bdProxiedInit(t, bd, "dwn")
		out := bdProxiedDeleteFail(t, bd, p.dir, "dwn-doesnotexist", "--force")
		if !strings.Contains(strings.ToLower(out), "not found") {
			t.Errorf("expected `not found` error for bogus wisp id, got: %s", out)
		}
	})
}

func TestProxiedServerDeleteConcurrent(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	p := bdProxiedInit(t, bd, "ddc")
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
