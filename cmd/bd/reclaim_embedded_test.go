//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedReclaimRunsTheLeaseReclaimerRole drives `bd reclaim` on the
// direct route, where it now runs issueops.LeaseReclaimer off the decorated
// store: the stale lease it names is reverted with a fresh revision, a live
// lease and an absent id it names are left out without an error, the sweep
// records its own version commit, and the workspace's on_update hook fires once
// for the reverted row.
func TestEmbeddedReclaimRunsTheLeaseReclaimerRole(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rc")

	hookLog := filepath.Join(t.TempDir(), "on_update.log")
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/bin/sh\necho \"$1\" >> " + hookLog + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "on_update"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant on_update: %v", err)
	}

	stale := bdCreate(t, bd, dir, "Stale lease", "--type", "task")
	live := bdCreate(t, bd, dir, "Live lease", "--type", "task")
	bdUpdate(t, bd, dir, stale.ID, "--claim")
	bdUpdate(t, bd, dir, live.ID, "--claim")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	withDB := func(fn func(exec func(string, ...any))) {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		fn(func(query string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
		})
	}
	countCommits := func() int {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'bd: reclaim%'").Scan(&n); err != nil {
			t.Fatalf("count reclaim commits: %v", err)
		}
		return n
	}
	withDB(func(exec func(string, ...any)) {
		exec("UPDATE leases SET lease_expires_at = ? WHERE issue_id = ?", time.Now().UTC().Add(-time.Hour), stale.ID)
	})
	before := countCommits()

	out, err := bdRunWithFlockRetry(t, bd, dir, "reclaim", "--older-than", "0s", "--json",
		"--id", stale.ID, "--id", live.ID, "--id", "rc-absent")
	if err != nil {
		t.Fatalf("bd reclaim: %v\n%s", err, out)
	}
	var got struct {
		Count     int                    `json:"count"`
		Scoped    bool                   `json:"scoped"`
		Reclaimed []types.ReclaimedLease `json:"reclaimed"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.Count != 1 || len(got.Reclaimed) != 1 || got.Reclaimed[0].ID != stale.ID || !got.Scoped {
		t.Fatalf("reclaim answered %+v, want exactly %s, scoped", got, stale.ID)
	}
	if _, err := types.ParseRevisionToken(got.Reclaimed[0].Revision); err != nil || got.Reclaimed[0].Revision == "" {
		t.Fatalf("reclaimed revision %q is not a revision token: %v", got.Reclaimed[0].Revision, err)
	}

	if after := countCommits(); after != before+1 {
		t.Fatalf("reclaim version commits %d -> %d, want exactly one more", before, after)
	}
	if iss := bdShow(t, bd, dir, stale.ID); iss.Status != types.StatusOpen || iss.Assignee != "" {
		t.Fatalf("%s after reclaim: status=%s assignee=%q, want open and unassigned", stale.ID, iss.Status, iss.Assignee)
	}
	if iss := bdShow(t, bd, dir, live.ID); iss.Status != types.StatusInProgress {
		t.Fatalf("%s after reclaim: status=%s, want it still in_progress", live.ID, iss.Status)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := os.ReadFile(hookLog) // #nosec G304 -- this test's own temp file
		fired := map[string]int{}
		for _, line := range strings.Fields(string(data)) {
			fired[line]++
		}
		// The claims fired on_update for both ids already; the reclaim adds
		// one more for the reverted id and none for the live one.
		if fired[stale.ID] == 2 && fired[live.ID] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("on_update fired %v, want %s twice (claim, reclaim) and %s once (claim)", fired, stale.ID, live.ID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
