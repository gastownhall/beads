//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerReadyWisp(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("claim_include_ephemeral_claims_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwc")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp to claim", "--ephemeral", "--label", "wclaim")

		out, err := bdProxiedRun(t, bd, p.dir,
			"ready", "--claim", "--json", "--include-ephemeral", "--label", "wclaim")
		if err != nil {
			t.Fatalf("bd ready --claim --include-ephemeral failed: %v\n%s", err, out)
		}
		var claimed []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(out), &claimed); err != nil {
			t.Fatalf("parse claim JSON: %v\n%s", err, out)
		}
		if len(claimed) != 1 {
			t.Fatalf("expected one claimed wisp, got %d: %s", len(claimed), out)
		}
		if claimed[0].ID != wisp.ID {
			t.Fatalf("claimed ID = %s, want %s", claimed[0].ID, wisp.ID)
		}
		if claimed[0].Status != types.StatusInProgress {
			t.Errorf("Status = %s, want %s", claimed[0].Status, types.StatusInProgress)
		}
		if claimed[0].Assignee == "" {
			t.Error("Assignee should be set after claim")
		}

		db := openProxiedDB(t, p)
		var status, assignee string
		if err := db.QueryRowContext(context.Background(),
			"SELECT status, assignee FROM wisps WHERE id = ?", wisp.ID).Scan(&status, &assignee); err != nil {
			t.Fatalf("re-fetch wisp from wisps table: %v", err)
		}
		if status != string(types.StatusInProgress) {
			t.Errorf("persisted wisp status = %s, want in_progress", status)
		}
		if assignee == "" {
			t.Error("persisted wisp assignee empty")
		}
	})

	t.Run("mol_on_wisp_molecule_currently_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwm")
		mol := bdProxiedCreate(t, bd, p.dir, "Wisp mol", "--ephemeral", "--type", "molecule")
		bdProxiedCreate(t, bd, p.dir, "Wisp step", "--ephemeral", "--parent", mol.ID)
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--mol", mol.ID, "--json")
		if err == nil {
			t.Skipf("--mol on wisp molecule succeeded — implementation may now support this. stdout: %s", stdout)
		}
		combined := stdout + stderr
		if !strings.Contains(combined, "not found") && !strings.Contains(combined, "loading molecule") {
			t.Errorf("expected 'not found'/'loading molecule' error for wisp mol, got stdout:%s stderr:%s", stdout, stderr)
		}
	})

	t.Run("wisp_blocked_by_wisp_hidden_and_listed_in_blocked", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwwb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Wisp blocker", "--ephemeral")
		dependent := bdProxiedCreate(t, bd, p.dir, "Wisp dependent", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			dependent.ID, blocker.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant wisp blocks wisp edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE wisps SET is_blocked = 1 WHERE id = ?", dependent.ID); err != nil {
			t.Fatalf("mark wisp is_blocked: %v", err)
		}

		ready := bdProxiedReadyJSON(t, bd, p, "--include-ephemeral")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[blocker.ID] {
			t.Errorf("expected blocker wisp %s to be ready", blocker.ID)
		}
		if ids[dependent.ID] {
			t.Errorf("blocked wisp %s must not appear in ready --include-ephemeral", dependent.ID)
		}

		blocked := bdProxiedBlockedJSON(t, bd, p)
		blockedIDs := map[string]bool{}
		for _, bi := range blocked {
			blockedIDs[bi.ID] = true
		}
		if !blockedIDs[dependent.ID] {
			t.Errorf("expected wisp %s in bd blocked output, got: %+v", dependent.ID, blockedIDs)
		}
	})

	t.Run("cross_table_issue_blocked_by_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwci")
		wispBlocker := bdProxiedCreate(t, bd, p.dir, "Wisp blocker for issue", "--ephemeral")
		issue := bdProxiedCreate(t, bd, p.dir, "Issue blocked by wisp")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), 'test')",
			depid.New(issue.ID, wispBlocker.ID), issue.ID, wispBlocker.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant issue->wisp blocks edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE issues SET is_blocked = 1 WHERE id = ?", issue.ID); err != nil {
			t.Fatalf("mark issue is_blocked: %v", err)
		}

		ready := bdProxiedReadyJSON(t, bd, p)
		for _, r := range ready {
			if r.ID == issue.ID {
				t.Errorf("issue %s should be hidden (blocked by wisp)", issue.ID)
			}
		}

		blocked := bdProxiedBlockedJSON(t, bd, p)
		var entry *types.BlockedIssue
		for _, bi := range blocked {
			if bi.ID == issue.ID {
				entry = bi
				break
			}
		}
		if entry == nil {
			t.Fatalf("expected issue %s in bd blocked output", issue.ID)
		}
		foundBlocker := false
		for _, b := range entry.BlockedBy {
			if b == wispBlocker.ID {
				foundBlocker = true
				break
			}
		}
		if !foundBlocker {
			t.Errorf("expected wisp blocker %s in BlockedBy, got: %v", wispBlocker.ID, entry.BlockedBy)
		}
	})

	t.Run("cross_table_wisp_blocked_by_issue", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwcw")
		issueBlocker := bdProxiedCreate(t, bd, p.dir, "Issue blocker for wisp")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp blocked by issue", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			wisp.ID, issueBlocker.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant wisp->issue blocks edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE wisps SET is_blocked = 1 WHERE id = ?", wisp.ID); err != nil {
			t.Fatalf("mark wisp is_blocked: %v", err)
		}

		ready := bdProxiedReadyJSON(t, bd, p, "--include-ephemeral")
		for _, r := range ready {
			if r.ID == wisp.ID {
				t.Errorf("wisp %s should be hidden (blocked by issue)", wisp.ID)
			}
		}

		blocked := bdProxiedBlockedJSON(t, bd, p)
		var entry *types.BlockedIssue
		for _, bi := range blocked {
			if bi.ID == wisp.ID {
				entry = bi
				break
			}
		}
		if entry == nil {
			t.Fatalf("expected wisp %s in bd blocked output", wisp.ID)
		}
		foundBlocker := false
		for _, b := range entry.BlockedBy {
			if b == issueBlocker.ID {
				foundBlocker = true
				break
			}
		}
		if !foundBlocker {
			t.Errorf("expected issue blocker %s in BlockedBy, got: %v", issueBlocker.ID, entry.BlockedBy)
		}
	})

	t.Run("blocked_lists_blocked_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwbl")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker wisp", "--ephemeral")
		dependent := bdProxiedCreate(t, bd, p.dir, "Dependent wisp", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			dependent.ID, blocker.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant wisp blocks wisp edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE wisps SET is_blocked = 1 WHERE id = ?", dependent.ID); err != nil {
			t.Fatalf("mark wisp is_blocked: %v", err)
		}

		blocked := bdProxiedBlockedJSON(t, bd, p)
		var entry *types.BlockedIssue
		for _, bi := range blocked {
			if bi.ID == dependent.ID {
				entry = bi
				break
			}
		}
		if entry == nil {
			t.Fatalf("expected wisp %s in bd blocked output", dependent.ID)
		}
		if entry.BlockedByCount != 1 || len(entry.BlockedBy) != 1 || entry.BlockedBy[0] != blocker.ID {
			t.Errorf("BlockedBy = %v (count=%d), want [%s]", entry.BlockedBy, entry.BlockedByCount, blocker.ID)
		}
	})

	t.Run("explain_enriches_wisp_blocker_details", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwexb")
		wispBlocker := bdProxiedCreate(t, bd, p.dir,
			"Wisp blocker title", "--ephemeral", "-p", "1")
		issue := bdProxiedCreate(t, bd, p.dir, "Blocked issue")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), 'test')",
			depid.New(issue.ID, wispBlocker.ID), issue.ID, wispBlocker.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant issue->wisp blocks edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE issues SET is_blocked = 1 WHERE id = ?", issue.ID); err != nil {
			t.Fatalf("mark issue is_blocked: %v", err)
		}

		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--explain", "--json")
		if err != nil {
			t.Fatalf("bd ready --explain --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in --explain output: %s", stdout)
		}
		var exp types.ReadyExplanation
		if err := json.Unmarshal([]byte(s[start:]), &exp); err != nil {
			t.Fatalf("parse ReadyExplanation: %v\n%s", err, s[start:])
		}

		var blockedItem *types.BlockedItem
		for i := range exp.Blocked {
			if exp.Blocked[i].ID == issue.ID {
				blockedItem = &exp.Blocked[i]
				break
			}
		}
		if blockedItem == nil {
			t.Fatalf("blocked issue %s not in explain output: %+v", issue.ID, exp.Blocked)
		}

		var blocker *types.BlockerInfo
		for i := range blockedItem.BlockedBy {
			if blockedItem.BlockedBy[i].ID == wispBlocker.ID {
				blocker = &blockedItem.BlockedBy[i]
				break
			}
		}
		if blocker == nil {
			t.Fatalf("wisp blocker %s not in BlockedBy: %+v", wispBlocker.ID, blockedItem.BlockedBy)
		}
		if blocker.Title != "Wisp blocker title" {
			t.Errorf("wisp blocker Title = %q, want %q (GetIssuesByIDs doesn't read wisps)",
				blocker.Title, "Wisp blocker title")
		}
		if blocker.Status != types.StatusOpen {
			t.Errorf("wisp blocker Status = %q, want %q", blocker.Status, types.StatusOpen)
		}
		if blocker.Priority != 1 {
			t.Errorf("wisp blocker Priority = %d, want 1", blocker.Priority)
		}
	})

	t.Run("explain_detects_cycle_in_wisp_dependencies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwcy")
		a := bdProxiedCreate(t, bd, p.dir, "Wisp cycle A", "--ephemeral")
		b := bdProxiedCreate(t, bd, p.dir, "Wisp cycle B", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			a.ID, b.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant a->b wisp edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			b.ID, a.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant b->a wisp edge: %v", err)
		}

		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--explain", "--json")
		if err != nil {
			t.Fatalf("bd ready --explain --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in --explain output: %s", stdout)
		}
		var exp types.ReadyExplanation
		if err := json.Unmarshal([]byte(s[start:]), &exp); err != nil {
			t.Fatalf("parse ReadyExplanation: %v\n%s", err, s[start:])
		}
		if len(exp.Cycles) == 0 {
			t.Fatalf("expected non-empty Cycles for wisp_dependencies cycle, got: %+v", exp)
		}
		found := false
		for _, cycle := range exp.Cycles {
			cycleSet := map[string]bool{}
			for _, id := range cycle {
				cycleSet[id] = true
			}
			if cycleSet[a.ID] && cycleSet[b.ID] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected cycle containing %s and %s, got: %+v", a.ID, b.ID, exp.Cycles)
		}
	})

	t.Run("empty_state_hint_when_only_wisps_exist", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rwes")
		bdProxiedCreate(t, bd, p.dir, "Lonely wisp", "--ephemeral")
		stdout, _ := bdProxiedReadyCapture(t, bd, p)
		if !strings.Contains(stdout, "No open issues") {
			t.Errorf("expected 'No open issues' (stats ignores wisps), got: %s", stdout)
		}
	})
}
