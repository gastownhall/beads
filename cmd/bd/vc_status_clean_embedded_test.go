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

// TestEmbeddedVCStatusCleanAndBookkeepingOnly pins the empty half of the
// GH#7783 working-set contract: a clean working set reports "no uncommitted
// changes" in text and EMPTY (present, length-zero) staged and unstaged JSON
// arrays, and so does a working set whose only changes touch the shared
// bookkeeping set — the same tables `bd doctor`'s uncommitted-changes checks
// filter (wisps, leases, local_metadata, repo_mtimes, events, wisp_*).
func TestEmbeddedVCStatusCleanAndBookkeepingOnly(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	assertClean := func(t *testing.T, dir string) {
		t.Helper()
		text := bdVC(t, bd, dir, "status")
		if !strings.Contains(text, "Uncommitted changes: none") {
			t.Errorf("text output must report no uncommitted changes:\n%s", text)
		}
		if strings.Contains(text, "modified") {
			t.Errorf("text output must not name modified tables:\n%s", text)
		}

		var out struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
			Staged []struct {
				Table string `json:"table"`
			} `json:"staged"`
			Unstaged []struct {
				Table string `json:"table"`
			} `json:"unstaged"`
		}
		js := bdVC(t, bd, dir, "status", "--json")
		if err := json.Unmarshal([]byte(js), &out); err != nil {
			t.Fatalf("parse vc status --json: %v\n%s", err, js)
		}
		if out.Branch == "" || out.Commit == "" {
			t.Errorf("json branch and commit must stay present, got %q/%q", out.Branch, out.Commit)
		}
		// A MISSING key or a JSON null unmarshals to a nil slice; only a real
		// empty array gives a non-nil, length-zero one. Both lists must be
		// present and empty.
		if out.Staged == nil || len(out.Staged) != 0 {
			t.Errorf("json staged = %+v, want a present empty array", out.Staged)
		}
		if out.Unstaged == nil || len(out.Unstaged) != 0 {
			t.Errorf("json unstaged = %+v, want a present empty array", out.Unstaged)
		}
	}

	t.Run("clean working set", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcclean")
		assertClean(t, dir)
	})

	t.Run("bookkeeping-only changes stay hidden", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "vcbk")
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		// A fresh store creates local_metadata dolt_ignore'd BEFORE it
		// exists, so its writes are invisible to dolt_status. The shape the
		// filter exists for is a bookkeeping table dolt_status DOES report —
		// one that got tracked at some point (a store bootstrapped before the
		// ignore, or force-staged). Force-stage and commit it once, then
		// modify it: from then on dolt reports every change to it dirty
		// forever (dolt_ignore only matches newly added tables), which is
		// exactly the self-fulfilling warning the shared filter suppresses.
		if _, err := db.ExecContext(t.Context(),
			"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", "vcstatus-test", "seed"); err != nil {
			cleanup()
			t.Fatalf("seed local_metadata: %v", err)
		}
		if _, err := db.ExecContext(t.Context(), "CALL DOLT_ADD('-f', 'local_metadata')"); err != nil {
			cleanup()
			t.Fatalf("force-stage local_metadata: %v", err)
		}
		if _, err := db.ExecContext(t.Context(),
			"CALL DOLT_COMMIT('-m', 'track local_metadata once', '--skip-empty')"); err != nil {
			cleanup()
			t.Fatalf("commit tracked local_metadata: %v", err)
		}
		if _, err := db.ExecContext(t.Context(),
			"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", "vcstatus-test", "dirty"); err != nil {
			cleanup()
			t.Fatalf("dirty local_metadata: %v", err)
		}
		var dirty int
		if err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'local_metadata'").Scan(&dirty); err != nil {
			cleanup()
			t.Fatalf("read dolt_status: %v", err)
		}
		cleanup()
		if dirty == 0 {
			t.Fatal("precondition: local_metadata is not modified in the working set")
		}
		assertClean(t, dir)
	})
}
