//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProxiedServerWispCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("bare_wisp_and_wisp_create_materialize_same_dag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wcd")
		proto := bdProxiedCreate(t, bd, p.dir, "DAG proto", "--type", "epic", "--label", "template")
		bdProxiedCreate(t, bd, p.dir, "DAG step one", "--type", "task", "--parent", proto.ID)
		bdProxiedCreate(t, bd, p.dir, "DAG step two", "--type", "task", "--parent", proto.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", proto.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol wisp --json: %v\n%s", err, out)
		}
		var got struct {
			Created int    `json:"created"`
			Phase   string `json:"phase"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Created != 3 {
			t.Errorf("created = %d, want 3 (root + 2 steps)", got.Created)
		}
		if got.Phase != "vapor" {
			t.Errorf("phase = %s, want vapor", got.Phase)
		}

		out2, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "create", proto.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol wisp create --json: %v\n%s", err, out2)
		}
		var got2 struct {
			Created int `json:"created"`
		}
		if err := json.Unmarshal(out2, &got2); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out2)
		}
		if got2.Created != got.Created {
			t.Errorf("bd mol wisp create --json created %d, want %d (parity with bare wisp)", got2.Created, got.Created)
		}
	})

	t.Run("root_only_skips_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wcr")
		proto := bdProxiedCreate(t, bd, p.dir, "Root-only proto", "--type", "epic", "--label", "template")
		bdProxiedCreate(t, bd, p.dir, "Skipped step", "--type", "task", "--parent", proto.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "create", proto.ID, "--root-only", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp create --root-only --json: %v\n%s", err, out)
		}
		var got struct {
			Created int `json:"created"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Created != 1 {
			t.Errorf("created = %d, want 1 (root only)", got.Created)
		}
	})

	t.Run("dry_run_creates_nothing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wcdr")
		proto := bdProxiedCreate(t, bd, p.dir, "Dry wisp proto", "--type", "epic", "--label", "template")
		bdProxiedCreate(t, bd, p.dir, "Dry wisp step", "--type", "task", "--parent", proto.ID)

		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "wisp", "create", proto.ID, "--dry-run")
		if err != nil {
			t.Fatalf("bd mol wisp create --dry-run: %v\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "Dry run") {
			t.Errorf("expected dry-run message, got: %s", stdout)
		}

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps").Scan(&count); err != nil {
			t.Fatalf("count wisps: %v", err)
		}
		if count != 0 {
			t.Errorf("expected no wisps created by --dry-run, found %d", count)
		}
	})

	t.Run("wisp_type_stamps_every_created_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wcwt")
		proto := bdProxiedCreate(t, bd, p.dir, "Typed wisp proto", "--type", "epic", "--label", "template")
		step := bdProxiedCreate(t, bd, p.dir, "Typed wisp step", "--type", "task", "--parent", proto.ID)
		db := openProxiedDB(t, p)

		rootAndStep := []string{proto.ID, step.ID}
		for _, c := range []struct {
			name string
			args []string
			srcs []string
			want string
		}{
			{"create patrol", []string{"mol", "wisp", "create", proto.ID, "--wisp-type", "patrol"}, rootAndStep, "patrol"},
			{"bare heartbeat", []string{"mol", "wisp", proto.ID, "--wisp-type", "heartbeat"}, rootAndStep, "heartbeat"},
			{"create root-only patrol", []string{"mol", "wisp", "create", proto.ID, "--root-only", "--wisp-type", "patrol"}, []string{proto.ID}, "patrol"},
			{"create omitted", []string{"mol", "wisp", "create", proto.ID}, rootAndStep, ""},
			{"create explicit empty", []string{"mol", "wisp", "create", proto.ID, "--wisp-type", ""}, rootAndStep, ""},
		} {
			idMapping := proxiedWispCreateMapping(t, bd, p.dir, c.srcs, append(c.args, "--json")...)
			for _, src := range c.srcs {
				id := idMapping[src]
				if got := proxiedWispType(t, db, id); got != c.want {
					t.Errorf("%s: wisp %s (from %s) wisp_type = %q, want %q", c.name, id, src, got, c.want)
				}
			}
		}
	})

	t.Run("invalid_wisp_type_creates_nothing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wcwi")
		proto := bdProxiedCreate(t, bd, p.dir, "Invalid type proto", "--type", "epic", "--label", "template")
		bdProxiedCreate(t, bd, p.dir, "Invalid type step", "--type", "task", "--parent", proto.ID)
		db := openProxiedDB(t, p)

		for _, args := range [][]string{
			{"mol", "wisp", "create", proto.ID, "--wisp-type", "bogus"},
			{"mol", "wisp", "create", proto.ID, "--wisp-type", "bogus", "--dry-run"},
			{"mol", "wisp", proto.ID, "--wisp-type", "default"},
			{"mol", "wisp", proto.ID, "--wisp-type", "default", "--dry-run"},
		} {
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
			if err == nil {
				t.Errorf("bd %s: expected an error, got none\n%s", strings.Join(args, " "), stdout)
			}
			if !strings.Contains(stderr, "invalid wisp-type") {
				t.Errorf("bd %s: stderr = %q, want the invalid wisp-type diagnostic", strings.Join(args, " "), stderr)
			}
			if strings.Contains(stdout, "Dry run") {
				t.Errorf("bd %s: invalid --wisp-type produced a dry-run preview:\n%s", strings.Join(args, " "), stdout)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM wisps").Scan(&count); err != nil {
				t.Fatalf("count wisps: %v", err)
			}
			if count != 0 {
				t.Errorf("bd %s: invalid --wisp-type left %d wisps behind, want 0", strings.Join(args, " "), count)
			}
		}
	})
}

// proxiedWispCreateMapping runs a wisp creation command with --json, checks
// it created exactly one wisp for each of srcs (the proto issue IDs it should
// clone), and returns its id_mapping (proto ID -> created wisp ID).
func proxiedWispCreateMapping(t *testing.T, bd, dir string, srcs []string, args ...string) map[string]string {
	t.Helper()
	out, err := bdProxiedRun(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	var got struct {
		Created   int               `json:"created"`
		IDMapping map[string]string `json:"id_mapping"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.Created != len(srcs) || len(got.IDMapping) != len(srcs) {
		t.Fatalf("bd %s: created = %d (%d mapped), want %d", strings.Join(args, " "), got.Created, len(got.IDMapping), len(srcs))
	}
	for _, src := range srcs {
		if _, ok := got.IDMapping[src]; !ok {
			t.Fatalf("bd %s: id_mapping has no entry for proto issue %s: %v", strings.Join(args, " "), src, got.IDMapping)
		}
	}
	return got.IDMapping
}

// proxiedWispType reads wisps.wisp_type for id, failing when the row is
// missing.
func proxiedWispType(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var wispType string
	if err := db.QueryRow("SELECT COALESCE(wisp_type, '') FROM wisps WHERE id = ?", id).Scan(&wispType); err != nil {
		t.Fatalf("read wisp_type for %s: %v", id, err)
	}
	return wispType
}

func TestProxiedServerWispList(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("default_excludes_closed_all_includes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wl")
		openWisp := bdProxiedCreate(t, bd, p.dir, "Open wisp", "--ephemeral")
		closedWisp := bdProxiedCreate(t, bd, p.dir, "Closed wisp", "--ephemeral")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", closedWisp.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "list", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp list --json: %v\n%s", err, out)
		}
		var got struct {
			Wisps []struct {
				ID string `json:"id"`
			} `json:"wisps"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		ids := map[string]bool{}
		for _, w := range got.Wisps {
			ids[w.ID] = true
		}
		if !ids[openWisp.ID] {
			t.Errorf("expected open wisp %s in default list", openWisp.ID)
		}
		if ids[closedWisp.ID] {
			t.Errorf("closed wisp %s should be excluded by default", closedWisp.ID)
		}

		outAll, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "list", "--all", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp list --all --json: %v\n%s", err, outAll)
		}
		var gotAll struct {
			Wisps []struct {
				ID string `json:"id"`
			} `json:"wisps"`
		}
		if err := json.Unmarshal(outAll, &gotAll); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, outAll)
		}
		idsAll := map[string]bool{}
		for _, w := range gotAll.Wisps {
			idsAll[w.ID] = true
		}
		if !idsAll[closedWisp.ID] {
			t.Errorf("expected closed wisp %s in --all list", closedWisp.ID)
		}
	})

	t.Run("type_filter", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wlt")
		taskWisp := bdProxiedCreate(t, bd, p.dir, "Task wisp", "--ephemeral", "--type", "task")
		bdProxiedCreate(t, bd, p.dir, "Chore wisp", "--ephemeral", "--type", "chore")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "list", "--type", "task", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp list --type task --json: %v\n%s", err, out)
		}
		var got struct {
			Wisps []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"wisps"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(got.Wisps) != 1 || got.Wisps[0].ID != taskWisp.ID {
			t.Fatalf("expected only task wisp %s, got %+v", taskWisp.ID, got.Wisps)
		}
	})

	t.Run("old_wisp_flagged", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wlo")
		oldWisp := bdProxiedCreate(t, bd, p.dir, "Old wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id = ?",
			time.Now().UTC().Add(-48*time.Hour), oldWisp.ID); err != nil {
			t.Fatalf("backdate wisp: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "list", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp list --json: %v\n%s", err, out)
		}
		var got struct {
			Wisps []struct {
				ID  string `json:"id"`
				Old bool   `json:"old"`
			} `json:"wisps"`
			OldCount int `json:"old_count"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.OldCount != 1 {
			t.Errorf("old_count = %d, want 1", got.OldCount)
		}
		found := false
		for _, w := range got.Wisps {
			if w.ID == oldWisp.ID && w.Old {
				found = true
			}
		}
		if !found {
			t.Errorf("expected wisp %s flagged old, got %+v", oldWisp.ID, got.Wisps)
		}
	})
}

func TestProxiedServerWispGC(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("age_threshold_cleans_abandoned_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wg")
		fresh := bdProxiedCreate(t, bd, p.dir, "Fresh wisp", "--ephemeral")
		abandoned := bdProxiedCreate(t, bd, p.dir, "Abandoned wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id = ?",
			time.Now().UTC().Add(-2*time.Hour), abandoned.ID); err != nil {
			t.Fatalf("backdate wisp: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--age", "1h", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --age 1h --json: %v\n%s", err, out)
		}
		var got struct {
			Deleted      []string `json:"deleted"`
			DeletedCount int      `json:"deleted_count"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.DeletedCount != 1 || got.Deleted[0] != abandoned.ID {
			t.Fatalf("expected only %s cleaned, got %+v", abandoned.ID, got)
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", fresh.ID).Scan(&count); err != nil {
			t.Fatalf("query fresh wisp: %v", err)
		}
		if count != 1 {
			t.Errorf("expected fresh wisp %s to survive gc", fresh.ID)
		}
	})

	t.Run("cascade_respects_age_for_dependent_step_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wgc")
		parentWisp := bdProxiedCreate(t, bd, p.dir, "Abandoned parent wisp", "--ephemeral")
		staleChild := bdProxiedCreate(t, bd, p.dir, "Stale child wisp", "--ephemeral")
		freshChild := bdProxiedCreate(t, bd, p.dir, "Fresh child wisp", "--ephemeral")
		// Closed so that only the cascade can collect it: without --all the
		// parent scan passes over closed wisps.
		if out, err := bdProxiedRun(t, bd, p.dir, "close", staleChild.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		db := openProxiedDB(t, p)
		for _, childID := range []string{staleChild.ID, freshChild.ID} {
			if _, err := db.Exec(
				"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
				childID, parentWisp.ID, "blocks"); err != nil {
				t.Fatalf("plant child->parent wisp edge: %v", err)
			}
		}
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id IN (?, ?)",
			time.Now().UTC().Add(-2*time.Hour), parentWisp.ID, staleChild.ID); err != nil {
			t.Fatalf("backdate parent and stale child wisps: %v", err)
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
		cleaned := map[string]bool{}
		for _, id := range got.Deleted {
			cleaned[id] = true
		}
		if !cleaned[parentWisp.ID] {
			t.Errorf("expected abandoned parent %s cleaned", parentWisp.ID)
		}
		if !cleaned[staleChild.ID] {
			t.Errorf("expected stale dependent child %s cascaded", staleChild.ID)
		}
		if cleaned[freshChild.ID] {
			t.Errorf("fresh child %s must survive gc despite stale parent, deleted: %v", freshChild.ID, got.Deleted)
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", freshChild.ID).Scan(&count); err != nil {
			t.Fatalf("query fresh child wisp: %v", err)
		}
		if count != 1 {
			t.Errorf("expected fresh child %s to remain in wisps table", freshChild.ID)
		}
	})

	t.Run("exclude_type_protects_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wge")
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "types.custom", "agent"); err != nil {
			t.Fatalf("bd config set types.custom agent: %v\n%s", err, out)
		}
		protected := bdProxiedCreate(t, bd, p.dir, "Protected agent wisp", "--ephemeral", "--type", "agent")
		unprotected := bdProxiedCreate(t, bd, p.dir, "Unprotected wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		old := time.Now().UTC().Add(-2 * time.Hour)
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id IN (?, ?)", old, protected.ID, unprotected.ID); err != nil {
			t.Fatalf("backdate wisps: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--age", "1h", "--exclude-type", "agent", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --exclude-type agent --json: %v\n%s", err, out)
		}
		var got struct {
			Deleted []string `json:"deleted"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		cleaned := map[string]bool{}
		for _, id := range got.Deleted {
			cleaned[id] = true
		}
		if cleaned[protected.ID] {
			t.Errorf("expected agent-type wisp %s to be protected", protected.ID)
		}
		if !cleaned[unprotected.ID] {
			t.Errorf("expected unprotected wisp %s to be cleaned", unprotected.ID)
		}
	})

	t.Run("closed_force_purges_regardless_of_age", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wgcl")
		closedWisp := bdProxiedCreate(t, bd, p.dir, "Just-closed wisp", "--ephemeral")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", closedWisp.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
		if err != nil {
			t.Fatalf("bd mol wisp gc --closed --force --json: %v\n%s", err, out)
		}
		var got struct {
			DeletedCount int `json:"deleted_count"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.DeletedCount != 1 {
			t.Errorf("deleted_count = %d, want 1", got.DeletedCount)
		}

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", closedWisp.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 0 {
			t.Errorf("expected closed wisp %s purged", closedWisp.ID)
		}
	})
}
