//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// wispGCAgeJSON is the age-mode `bd mol wisp gc --json` answer: the preview
// shape (WispGCResult) and the deletion shape (deleteBatch) decoded together.
type wispGCAgeJSON struct {
	CleanedIDs   []string `json:"cleaned_ids"`
	CleanedCount int      `json:"cleaned_count"`
	Candidates   int      `json:"candidates"`
	DryRun       bool     `json:"dry_run"`
	Deleted      []string `json:"deleted"`
	DeletedCount int      `json:"deleted_count"`
}

func runWispGCAgeJSON(t *testing.T, bd, dir string, args ...string) wispGCAgeJSON {
	t.Helper()
	full := append([]string{"mol", "wisp", "gc"}, args...)
	full = append(full, "--json")
	stdout, _ := bdRunSplit(t, bd, dir, full...)
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("bd %s produced no JSON object:\n%s", strings.Join(full, " "), stdout)
	}
	var got wispGCAgeJSON
	if err := json.NewDecoder(strings.NewReader(stdout[start:])).Decode(&got); err != nil {
		t.Fatalf("parse bd %s: %v\n%s", strings.Join(full, " "), err, stdout)
	}
	return got
}

// backdateWispSQL moves a wisp's updated_at hours into the past through
// embedded SQL, so bare gc's default 1h threshold selects it without a sleep.
// The wisps table is dolt-ignored, so the write needs no Dolt commit; the
// handle closes before any bd command runs (an OpenSQL handle held across a
// bd commit wedges the database - see bd-k20r85).
func backdateWispSQL(t *testing.T, beadsDir, id string, hours int) {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	if _, err := db.ExecContext(t.Context(),
		"UPDATE wisps SET updated_at = DATE_SUB(NOW(), INTERVAL ? HOUR) WHERE id = ?", hours, id); err != nil {
		t.Fatalf("backdate wisp %s: %v", id, err)
	}
}

// TestEmbeddedWispGCAgeRequiresForce is the regression test for GH#5152:
// age-mode `bd mol wisp gc` deleted abandoned wisps without --force, although
// --force is documented as "Actually delete (default: preview only)" and the
// --closed path already previews without it. Without --force every age-mode
// form (bare, explicit --age, --all, empty selection) must exit 0, delete
// nothing, and answer a preview: candidate ids and their types in text,
// dry_run true in JSON, and a footer saying to re-run with --force.
func TestEmbeddedWispGCAgeRequiresForce(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gf")

	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral", "--type", "chore").ID

	// Bare gc: the default 1h threshold finds nothing in a fresh wisp, and
	// the empty answer is still a preview, not a zero-row deletion.
	if got := runWispGCAgeJSON(t, bd, dir); !got.DryRun || len(got.CleanedIDs) != 0 || got.CleanedCount != 0 {
		t.Errorf("bare gc --json = %+v, want an empty preview with dry_run true", got)
	}

	// Text preview: the candidate, its type and the --force footer.
	stdout, _ := bdRunSplit(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")
	for _, want := range []string{idle, "chore", "Re-run with --force to delete", "keeping the same selection flags"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("gc --age 0s text preview missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Deleted") {
		t.Errorf("gc --age 0s without --force reported a deletion:\n%s", stdout)
	}
	bdShow(t, bd, dir, idle)

	// JSON preview, for explicit --age and for --all.
	for _, args := range [][]string{{"--age", "0s"}, {"--all", "--age", "0s"}} {
		got := runWispGCAgeJSON(t, bd, dir, args...)
		if !got.DryRun || got.CleanedCount != 0 || got.Candidates != 1 || len(got.CleanedIDs) != 1 || got.CleanedIDs[0] != idle {
			t.Errorf("gc %v --json = %+v, want a preview naming only %s with dry_run true", args, got, idle)
		}
		if got.DeletedCount != 0 || len(got.Deleted) != 0 {
			t.Errorf("gc %v --json without --force deleted %v", args, got.Deleted)
		}
	}

	// An exclusion that empties the selection is still a preview.
	if got := runWispGCAgeJSON(t, bd, dir, "--age", "0s", "--exclude-type", "chore"); !got.DryRun || len(got.CleanedIDs) != 0 {
		t.Errorf("gc --age 0s --exclude-type chore --json = %+v, want an empty preview", got)
	}

	// Nothing above deleted the wisp.
	bdShow(t, bd, dir, idle)

	// Bare gc over a wisp past the default 1h threshold previews it rather
	// than deleting it (GH#5152: patrol automation ran gc bare). Its own
	// workspace keeps the single-candidate counts above exact.
	staleDir, staleBeadsDir, _ := bdInit(t, bd, "--prefix", "gfs")
	stale := bdCreate(t, bd, staleDir, "stale wisp", "--ephemeral").ID
	backdateWispSQL(t, staleBeadsDir, stale, 2)
	if got := runWispGCAgeJSON(t, bd, staleDir); !got.DryRun || got.CleanedCount != 0 || got.Candidates != 1 || len(got.CleanedIDs) != 1 || got.CleanedIDs[0] != stale {
		t.Errorf("bare gc --json over a 2h-old wisp = %+v, want a preview naming only %s with dry_run true", got, stale)
	}
	bdShow(t, bd, staleDir, stale)
}
