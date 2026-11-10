//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// wispGCPreviewJSON decodes the --json shape an age-mode wisp GC preview
// emits, starting at the first "{" in out (anything before it is not part of
// the object).
func wispGCPreviewJSON(t *testing.T, out string) WispGCResult {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output:\n%s", out)
	}
	var res WispGCResult
	if err := json.Unmarshal([]byte(out[start:]), &res); err != nil {
		t.Fatalf("parse wisp gc --json output: %v\nraw:\n%s", err, out)
	}
	return res
}

// backdateWispSQL ages a wisp's updated_at backwards through embedded SQL:
// the age GC reads updated_at, and here that column is what is under test.
// The wisps table is dolt-ignored, so the write needs no Dolt commit; the
// handle closes before any bd command runs (an OpenSQL handle held across a
// bd commit wedges the database - see bd-k20r85).
func backdateWispSQL(t *testing.T, beadsDir, database, id string, age time.Duration) {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, beadsDir+"/embeddeddolt", database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if _, err := db.ExecContext(ctx,
		"UPDATE wisps SET updated_at = DATE_SUB(NOW(), INTERVAL ? HOUR) WHERE id = ?",
		int(age.Hours()), id); err != nil {
		t.Fatalf("backdate wisp %s: %v", id, err)
	}
}

// TestEmbeddedWispGCAgeRequiresForce covers the GH#5152 age-mode contract on
// the direct embedded route: `bd mol wisp gc` (bare, explicit --age, or --all)
// previews without --force and deletes only with it, an explicit --dry-run
// winning over --force. The single-wisp deletion case is pinned separately by
// TestEmbeddedWispGCAgeWithoutForceDoesNotDelete.
//
// Each subtest owns a fresh workspace: the counts it asserts are counts of
// what it created, so the subtests stay order-independent by not sharing one.
func TestEmbeddedWispGCAgeRequiresForce(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("force_deletes_exactly_the_eligible_set", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gcfe")
		idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
		pinned := bdCreate(t, bd, dir, "pinned wisp", "--ephemeral").ID
		bdCommand(t, bd, dir, "update", pinned, "--status", "pinned")
		fresh := bdCreate(t, bd, dir, "fresh wisp", "--ephemeral").ID
		backdateWispSQL(t, beadsDir, "gcfe", idle, 2*time.Hour)
		// Pinned is age-eligible too, so the selector alone must protect it.
		backdateWispSQL(t, beadsDir, "gcfe", pinned, 2*time.Hour)

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "1h", "--force", "--json")
		var del struct {
			Deleted      []string `json:"deleted"`
			DeletedCount int      `json:"deleted_count"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &del); err != nil {
			t.Fatalf("parse gc --force --json output: %v\nraw:\n%s", err, out)
		}
		if len(del.Deleted) != 1 || del.Deleted[0] != idle || del.DeletedCount != 1 {
			t.Fatalf("want --force to delete only %s, got %+v\n%s", idle, del, out)
		}
		for _, id := range []string{pinned, fresh} {
			if !bdShowSucceeds(t, bd, dir, id) {
				t.Errorf("wisp %s must survive a forced gc (pinned/too-young)", id)
			}
		}
	})

	t.Run("force_with_dry_run_deletes_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "gcff")
		idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--force", "--dry-run", "--json")
		res := wispGCPreviewJSON(t, out)
		if !res.DryRun || res.CleanedCount != 0 || res.Candidates != 1 || len(res.CleanedIDs) != 1 || res.CleanedIDs[0] != idle {
			t.Fatalf("want --force --dry-run to preview %s and delete nothing, got %+v\n%s", idle, res, out)
		}
		if !bdShowSucceeds(t, bd, dir, idle) {
			t.Fatalf("wisp %s was deleted by --force --dry-run", idle)
		}
	})

	t.Run("exclude_type_still_applies_under_force", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gcfg")
		bdCommand(t, bd, dir, "config", "set", "types.custom", "agent")
		protected := bdCreate(t, bd, dir, "agent wisp", "--ephemeral", "--type", "agent").ID
		unprotected := bdCreate(t, bd, dir, "plain wisp", "--ephemeral").ID
		backdateWispSQL(t, beadsDir, "gcfg", protected, 2*time.Hour)
		backdateWispSQL(t, beadsDir, "gcfg", unprotected, 2*time.Hour)

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "1h", "--force", "--exclude-type", "agent", "--json")
		var del struct {
			Deleted []string `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &del); err != nil {
			t.Fatalf("parse gc --force --exclude-type --json output: %v\nraw:\n%s", err, out)
		}
		for _, id := range del.Deleted {
			if id == protected {
				t.Errorf("excluded-type wisp %s was deleted under --force", protected)
			}
		}
		if len(del.Deleted) != 1 || del.Deleted[0] != unprotected {
			t.Fatalf("want --force --exclude-type to delete only %s, got %v\n%s", unprotected, del.Deleted, out)
		}
		if !bdShowSucceeds(t, bd, dir, protected) {
			t.Errorf("excluded-type wisp %s must survive a forced gc", protected)
		}
	})

	t.Run("explicit_age_preview_names_ids_types_and_force_footer", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "gcfa")
		idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")
		if !strings.Contains(out, "would clean 1 abandoned wisp(s)") {
			t.Fatalf("preview header missing from output:\n%s", out)
		}
		if !strings.Contains(out, idle+" (task): idle wisp") {
			t.Errorf("preview line must name the id and its type, got:\n%s", out)
		}
		if !strings.Contains(out, "Re-run with --force to delete") {
			t.Errorf("preview footer must instruct to re-run with --force, got:\n%s", out)
		}
		if !bdShowSucceeds(t, bd, dir, idle) {
			t.Fatalf("wisp %s was deleted by a preview-only gc; output:\n%s", idle, out)
		}
	})

	t.Run("bare_gc_previews_beyond_default_age", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gcfb")
		stale := bdCreate(t, bd, dir, "stale wisp", "--ephemeral").ID
		backdateWispSQL(t, beadsDir, "gcfb", stale, 2*time.Hour)

		// Bare `bd mol wisp gc` uses the default 1h threshold and must
		// preview, not delete (GH#5152: patrol automation ran it bare).
		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--json")
		res := wispGCPreviewJSON(t, out)
		if !res.DryRun || res.CleanedCount != 0 || res.Candidates != 1 {
			t.Fatalf("want preview (dry_run=true, cleaned_count=0, candidates=1), got %+v\n%s", res, out)
		}
		if len(res.CleanedIDs) != 1 || res.CleanedIDs[0] != stale {
			t.Fatalf("want preview to list only %s, got %v", stale, res.CleanedIDs)
		}
		if !bdShowSucceeds(t, bd, dir, stale) {
			t.Fatalf("wisp %s was deleted by a bare preview-only gc", stale)
		}
	})

	t.Run("empty_preview_reports_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "gcfc")
		// A pinned-status wisp is protected (CategoryFrozen), so the selector
		// finds nothing and the empty-result preview shape is what is asserted.
		pinned := bdCreate(t, bd, dir, "pinned wisp", "--ephemeral").ID
		bdCommand(t, bd, dir, "update", pinned, "--status", "pinned")

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--json")
		res := wispGCPreviewJSON(t, out)
		if !res.DryRun || res.CleanedCount != 0 || len(res.CleanedIDs) != 0 || res.Candidates != 0 {
			t.Fatalf("want empty preview with dry_run=true and no candidates, got %+v\n%s", res, out)
		}
		if !bdShowSucceeds(t, bd, dir, pinned) {
			t.Fatalf("pinned wisp %s must survive gc", pinned)
		}
	})

	t.Run("json_preview_matches_dry_run_shape", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "gcfd")
		idle := bdCreate(t, bd, dir, "idle wisp json", "--ephemeral").ID

		for _, args := range [][]string{
			{"--age", "0s", "--json"},              // preview by missing --force
			{"--age", "0s", "--dry-run", "--json"}, // explicit dry-run
		} {
			out := bdCommand(t, bd, dir, append([]string{"mol", "wisp", "gc"}, args...)...)
			res := wispGCPreviewJSON(t, out)
			if !res.DryRun || res.CleanedCount != 0 || res.Candidates != 1 || len(res.CleanedIDs) != 1 || res.CleanedIDs[0] != idle {
				t.Fatalf("args %v: want preview listing %s, got %+v\n%s", args, idle, res, out)
			}
		}
		if !bdShowSucceeds(t, bd, dir, idle) {
			t.Fatalf("wisp %s was deleted by a preview-only gc", idle)
		}
	})
}
