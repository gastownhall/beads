//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The proxied-server leg of the wisp GC protected-label guard (bd-961adp).
// The proxied route shares the direct route's selection (findAbandonedWisps
// for the age sweep, filterClosedPurgeCandidates for the closed purge), so it
// must produce the direct route's result: a wisp carrying a configured
// protected label survives, an eligible unprotected peer does not.

// TestProxiedServerWispGCProtectedLabels carries the guard across the
// proxied-server route for every mode the sweep runs in.
func TestProxiedServerWispGCProtectedLabels(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("closed_purge_spares_labeled_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wgl")
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`); err != nil {
			t.Fatalf("bd config set wisp.protected_labels: %v\n%s", err, out)
		}
		kept := bdProxiedCreate(t, bd, p.dir, "kept closed wisp", "--ephemeral", "--label", "keep:mail")
		plain := bdProxiedCreate(t, bd, p.dir, "plain closed wisp", "--ephemeral")
		for _, id := range []string{kept.ID, plain.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "close", id); err != nil {
				t.Fatalf("bd close %s: %v\n%s", id, err, out)
			}
		}

		db := openProxiedDB(t, p)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--closed", "--dry-run")
		if err != nil {
			t.Fatalf("bd mol wisp gc --closed --dry-run: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Found 1 closed wisp(s)") {
			t.Errorf("preview counted more than the unlabeled closed wisp as a candidate:\n%s", out)
		}

		out, err = bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --closed --force --json: %v\n%s", err, out)
		}
		var got struct {
			Deleted      []string `json:"deleted"`
			DeletedCount int      `json:"deleted_count"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		deleted := map[string]bool{}
		for _, id := range got.Deleted {
			deleted[id] = true
		}
		if deleted[kept.ID] {
			t.Errorf("labeled closed wisp %s was purged over the proxied route; want it spared (deleted=%v)", kept.ID, got.Deleted)
		}
		if !deleted[plain.ID] {
			t.Errorf("unlabeled closed wisp %s not purged over the proxied route (deleted=%v)", plain.ID, got.Deleted)
		}

		for id, want := range map[string]int{kept.ID: 1, plain.ID: 0} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&count); err != nil {
				t.Fatalf("query %s: %v", id, err)
			}
			if count != want {
				t.Errorf("wisp %s row count = %d, want %d", id, count, want)
			}
		}
	})
}
