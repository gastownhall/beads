//go:build cgo

package main

import (
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

	t.Run("cascade_sweeps_dependent_step_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wgc")
		parentWisp := bdProxiedCreate(t, bd, p.dir, "Abandoned parent wisp", "--ephemeral")
		childWisp := bdProxiedCreate(t, bd, p.dir, "Fresh child wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		if _, err := db.Exec(
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			childWisp.ID, parentWisp.ID, "blocks"); err != nil {
			t.Fatalf("plant child->parent wisp edge: %v", err)
		}
		if _, err := db.Exec("UPDATE wisps SET updated_at = ? WHERE id = ?",
			time.Now().UTC().Add(-2*time.Hour), parentWisp.ID); err != nil {
			t.Fatalf("backdate parent wisp: %v", err)
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
		if !cleaned[childWisp.ID] {
			t.Errorf("expected dependent child %s cascaded even though it's not itself old", childWisp.ID)
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
