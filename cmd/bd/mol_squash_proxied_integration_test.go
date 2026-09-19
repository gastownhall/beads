//go:build cgo

package main

import (
	"encoding/json"
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
}
