//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

	// age_sweep_spares_labeled_wisp: the proxied age sweep runs the same
	// selection as the direct route (findAbandonedWisps), so a stale wisp
	// carrying the protected label must survive it exactly as it does in
	// TestEmbeddedWispGCProtectedLabel, while a stale unlabeled peer is
	// reclaimed over the same wire.
	t.Run("age_sweep_spares_labeled_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wga")
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`); err != nil {
			t.Fatalf("bd config set wisp.protected_labels: %v\n%s", err, out)
		}
		kept := bdProxiedCreate(t, bd, p.dir, "kept stale wisp", "--ephemeral", "--label", "keep:mail")
		plain := bdProxiedCreate(t, bd, p.dir, "stale noise wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		for _, id := range []string{kept.ID, plain.ID} {
			if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id = ?",
				time.Now().UTC().Add(-2*time.Hour), id); err != nil {
				t.Fatalf("backdate wisp %s: %v", id, err)
			}
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--age", "1h", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --age 1h --json: %v\n%s", err, out)
		}
		var got struct {
			Deleted []string `json:"deleted"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		deleted := map[string]bool{}
		for _, id := range got.Deleted {
			deleted[id] = true
		}
		if deleted[kept.ID] {
			t.Errorf("labeled stale wisp %s was reclaimed over the proxied age sweep; want it spared (deleted=%v)", kept.ID, got.Deleted)
		}
		if !deleted[plain.ID] {
			t.Errorf("unlabeled stale wisp %s not reclaimed over the proxied age sweep (deleted=%v)", plain.ID, got.Deleted)
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

	// cascade_spares_labeled_dependent: the proxied cascade expansion also
	// runs the shared selection, so a labeled closed dependent — invisible to
	// the parent scan without --all — must be spared there too while an
	// unlabeled closed dependent goes with its parent, matching
	// TestEmbeddedWispGCCascadeSparesLabeledDependent.
	t.Run("cascade_spares_labeled_dependent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wgr")
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`); err != nil {
			t.Fatalf("bd config set wisp.protected_labels: %v\n%s", err, out)
		}
		parent := bdProxiedCreate(t, bd, p.dir, "abandoned parent wisp", "--ephemeral")
		keptChild := bdProxiedCreate(t, bd, p.dir, "kept dependent wisp", "--ephemeral", "--label", "keep:mail")
		plainChild := bdProxiedCreate(t, bd, p.dir, "gone dependent wisp", "--ephemeral")
		// Closed so only the cascade can collect them: without --all the
		// parent scan passes over closed wisps.
		for _, id := range []string{keptChild.ID, plainChild.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "close", id); err != nil {
				t.Fatalf("bd close %s: %v\n%s", id, err, out)
			}
		}

		db := openProxiedDB(t, p)
		for _, childID := range []string{keptChild.ID, plainChild.ID} {
			if _, err := db.Exec(
				"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
				childID, parent.ID, "blocks"); err != nil {
				t.Fatalf("plant child->parent wisp edge: %v", err)
			}
		}
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id IN (?, ?, ?)",
			time.Now().UTC().Add(-2*time.Hour), parent.ID, keptChild.ID, plainChild.ID); err != nil {
			t.Fatalf("backdate parent and dependents: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--age", "1h", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --age 1h --json: %v\n%s", err, out)
		}
		var got struct {
			Deleted []string `json:"deleted"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		deleted := map[string]bool{}
		for _, id := range got.Deleted {
			deleted[id] = true
		}
		if !deleted[parent.ID] {
			t.Errorf("abandoned parent %s not reclaimed (deleted=%v)", parent.ID, got.Deleted)
		}
		if !deleted[plainChild.ID] {
			t.Errorf("unlabeled closed dependent %s not cascaded with its parent (deleted=%v)", plainChild.ID, got.Deleted)
		}
		if deleted[keptChild.ID] {
			t.Errorf("labeled closed dependent %s was cascaded over the proxied route; want it spared (deleted=%v)", keptChild.ID, got.Deleted)
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", keptChild.ID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", keptChild.ID, err)
		}
		if count != 1 {
			t.Errorf("labeled dependent %s row count = %d, want 1 (survives, orphaned)", keptChild.ID, count)
		}
	})

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
