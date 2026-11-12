//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerMolSquash(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("basic_squash_deletes_ephemeral_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msq")
		root := bdProxiedCreate(t, bd, p.dir, "Squash root", "--type", "epic", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Squash child", "--type", "task", "--parent", root.ID, "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "squash", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol squash --json: %v\n%s", err, out)
		}
		var got struct {
			DigestID      string `json:"digest_id"`
			SquashedCount int    `json:"squashed_count"`
			DeletedCount  int    `json:"deleted_count"`
			WispSquash    bool   `json:"wisp_squash"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.SquashedCount != 1 || got.DeletedCount != 1 {
			t.Errorf("squashed/deleted = %d/%d, want 1/1", got.SquashedCount, got.DeletedCount)
		}
		if got.DigestID == "" {
			t.Fatal("expected non-empty digest_id")
		}
		if !got.WispSquash {
			t.Error("expected wisp_squash=true for an ephemeral root")
		}

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", child.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 0 {
			t.Errorf("expected squashed child %s to be deleted from wisps table", child.ID)
		}

		var rootStatus string
		var rootEphemeral bool
		if err := db.QueryRow("SELECT status, ephemeral FROM wisps WHERE id = ?", root.ID).Scan(&rootStatus, &rootEphemeral); err != nil {
			t.Fatalf("query root wisp row: %v", err)
		}
		if rootStatus != "closed" {
			t.Errorf("root status = %s, want closed", rootStatus)
		}
		if rootEphemeral {
			t.Error("root wisp flag should be cleared after squash")
		}
	})

	t.Run("keep_children_preserves_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msqk")
		root := bdProxiedCreate(t, bd, p.dir, "Keep-children root", "--type", "epic", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Keep-children child", "--type", "task", "--parent", root.ID, "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "squash", root.ID, "--keep-children", "--json")
		if err != nil {
			t.Fatalf("bd mol squash --keep-children --json: %v\n%s", err, out)
		}
		var got struct {
			DeletedCount int  `json:"deleted_count"`
			KeptChildren bool `json:"kept_children"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.DeletedCount != 0 || !got.KeptChildren {
			t.Errorf("deleted_count/kept_children = %d/%v, want 0/true", got.DeletedCount, got.KeptChildren)
		}

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", child.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 1 {
			t.Errorf("expected kept child %s to survive squash", child.ID)
		}
	})

	t.Run("agent_summary_used_verbatim", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msqs")
		root := bdProxiedCreate(t, bd, p.dir, "Summary root", "--type", "epic", "--ephemeral")
		bdProxiedCreate(t, bd, p.dir, "Summary child", "--type", "task", "--parent", root.ID, "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "squash", root.ID, "--summary", "Agent-provided summary text", "--json")
		if err != nil {
			t.Fatalf("bd mol squash --summary --json: %v\n%s", err, out)
		}
		var got struct {
			DigestID string `json:"digest_id"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		digest := bdProxiedShow(t, bd, p.dir, got.DigestID)
		if digest.Description != "Agent-provided summary text" {
			t.Errorf("digest description = %q, want %q", digest.Description, "Agent-provided summary text")
		}
	})

	// GH#5577 on the proxied route's own squash transaction: a step closed with
	// a reason that is not a failure still holds its conditional-blocks
	// fallback back, so the squash keeps it and the fallback stays blocked,
	// while the step nothing waits on is deleted. The dry run, which reads on
	// its own unit of work, names the step it would keep.
	t.Run("keeps_a_step_a_conditional_fallback_waits_on", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msqh")
		root := bdProxiedCreate(t, bd, p.dir, "Ship root", "--type", "epic", "--ephemeral")
		deploy := bdProxiedCreate(t, bd, p.dir, "Deploy", "--type", "task", "--parent", root.ID, "--ephemeral")
		announce := bdProxiedCreate(t, bd, p.dir, "Announce", "--type", "task", "--parent", root.ID, "--ephemeral")
		rollback := bdProxiedCreate(t, bd, p.dir, "Rollback", "--type", "task", "--ephemeral")
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", rollback.ID, deploy.ID, "--type", "conditional-blocks"); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}
		for id, reason := range map[string]string{deploy.ID: "deployed ok", announce.ID: "announced"} {
			if out, err := bdProxiedRun(t, bd, p.dir, "close", id, "--reason", reason); err != nil {
				t.Fatalf("bd close %s: %v\n%s", id, err, out)
			}
		}

		dry, err := bdProxiedRun(t, bd, p.dir, "mol", "squash", root.ID, "--dry-run")
		if err != nil {
			t.Fatalf("bd mol squash --dry-run: %v\n%s", err, dry)
		}
		if !strings.Contains(string(dry), "Kept: 1 closed wisp(s)") || !strings.Contains(string(dry), deploy.ID) {
			t.Errorf("bd mol squash --dry-run does not say it keeps %s:\n%s", deploy.ID, dry)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "squash", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol squash --json: %v\n%s", err, out)
		}
		var got struct {
			DeletedCount int      `json:"deleted_count"`
			KeptIDs      []string `json:"kept_ids"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.DeletedCount != 1 || len(got.KeptIDs) != 1 || got.KeptIDs[0] != deploy.ID {
			t.Errorf("deleted_count=%d kept_ids=%v, want %s kept and only %s deleted", got.DeletedCount, got.KeptIDs, deploy.ID, announce.ID)
		}

		db := openProxiedDB(t, p)
		count := func(id string) int {
			t.Helper()
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&n); err != nil {
				t.Fatalf("query wisps: %v", err)
			}
			return n
		}
		if count(deploy.ID) != 1 {
			t.Errorf("expected %s kept: its conditional-blocks fallback %s still waits on it", deploy.ID, rollback.ID)
		}
		if count(announce.ID) != 0 {
			t.Errorf("expected %s deleted: nothing waits on it", announce.ID)
		}
		var blocked int
		if err := db.QueryRow("SELECT is_blocked FROM wisps WHERE id = ?", rollback.ID).Scan(&blocked); err != nil {
			t.Fatalf("query fallback: %v", err)
		}
		if blocked != 1 {
			t.Errorf("fallback %s is_blocked = %d, want 1: squash released it after %s succeeded", rollback.ID, blocked, deploy.ID)
		}
	})
}
