package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestTxReadYourWritesWithComment exercises the full composite-view
// read-your-writes cycle on real Dolt: an issue graph plus a comment created
// inside one transaction, read back through the new snapshot-read methods
// BEFORE commit. The comment leg is Dolt-specific because it writes through
// doltTransaction.ImportIssueComment (the embedded transaction stubs it), so it
// lives here rather than in the backend-agnostic conformance suite.
func TestTxReadYourWritesWithComment(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	err := store.RunInTransaction(ctx, "bd: tx read-your-writes", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, &types.Issue{ID: "txr-1", Title: "Blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, &types.Issue{ID: "txr-2", Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			return err
		}
		if err := tx.AddDependency(ctx, &types.Dependency{IssueID: "txr-2", DependsOnID: "txr-1", Type: types.DepBlocks}, "tester"); err != nil {
			return err
		}
		if _, err := tx.ImportIssueComment(ctx, "txr-1", "tester", "note", time.Now().UTC()); err != nil {
			return err
		}

		// Dependent-edge read-your-writes.
		dependents, err := tx.GetDependentRecords(ctx, "txr-1", "", 0, "")
		if err != nil {
			return err
		}
		if len(dependents) != 1 || dependents[0].IssueID != "txr-2" {
			t.Errorf("in-tx GetDependentRecords(txr-1) = %v, want one edge from txr-2", dependents)
		}
		nDep, err := tx.CountDependentRecords(ctx, "txr-1", "")
		if err != nil {
			return err
		}
		if nDep != 1 {
			t.Errorf("in-tx CountDependentRecords(txr-1) = %d, want 1", nDep)
		}

		// Blocked read-your-writes.
		batch, err := tx.IsBlockedBatch(ctx, []string{"txr-1", "txr-2"})
		if err != nil {
			return err
		}
		if !batch["txr-2"] || batch["txr-1"] {
			t.Errorf("in-tx IsBlockedBatch = %v, want txr-2 blocked, txr-1 not", batch)
		}

		// Comment read-your-writes through the new page method.
		page, err := tx.GetIssueCommentsPage(ctx, "txr-1", storage.CommentPageCursor{}, 0)
		if err != nil {
			return err
		}
		if len(page) != 1 || page[0].Text != "note" {
			t.Errorf("in-tx GetIssueCommentsPage(txr-1) = %v, want one comment 'note'", page)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
}

// TestTxReadYourWritesWispTier pins the two-session wisp behavior of the new
// reads on the classic Dolt store (wisp rows live on ignoredTx, durable on
// regularTx). It proves:
//   - Single-tier reads route to the owning session and ARE read-your-writes for
//     an uncommitted wisp (GetIssueCommentsPage, IsBlocked, IsBlockedBatch).
//   - Both-tiers-spanning reads run on regularTx and, per the documented
//     two-session caveat, do NOT yet see a wisp edge written in the same open
//     transaction (GetDependentRecords/CountDependentRecords) — and DO see it
//     once the transaction commits.
func TestTxReadYourWritesWispTier(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Committed durable blocker (its open status must be visible cross-session).
	blocker := crossTierRegularIssue("txw-block", "committed durable blocker")
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	// Committed wisp: pins the classic-Dolt count-vs-search asymmetry — the tx's
	// SearchIssues is durable-tier, while CountIssuesByGroup merges wisps.
	committedWisp := crossTierWispIssue("txw-cwisp", "committed wisp")
	if err := store.CreateIssue(ctx, committedWisp, "tester"); err != nil {
		t.Fatalf("CreateIssue committed wisp: %v", err)
	}

	err := store.RunInTransaction(ctx, "bd: wisp read-your-writes", func(tx storage.Transaction) error {
		// Count-vs-search asymmetry on the classic Dolt tx (committed data only,
		// before this tx mutates anything): SearchIssues sees the durable blocker
		// but not the committed wisp; CountIssuesByGroup counts both.
		search, err := tx.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return err
		}
		if len(search) != 1 || search[0].ID != "txw-block" {
			t.Errorf("tx.SearchIssues = %v, want [txw-block] only (durable-tier; committed wisp excluded)", search)
		}
		groupCounts, err := tx.CountIssuesByGroup(ctx, types.IssueFilter{}, "status")
		if err != nil {
			return err
		}
		total := 0
		for _, n := range groupCounts {
			total += n
		}
		if total != 2 {
			t.Errorf("tx.CountIssuesByGroup total = %d, want 2 (durable blocker + committed wisp merged)", total)
		}

		wisp := crossTierWispIssue("txw-wisp", "wisp created in tx")
		if err := tx.CreateIssue(ctx, wisp, "tester"); err != nil {
			return err
		}
		// Wisp-sourced blocks edge → wisp_dependencies (ignoredTx); wisp blocked.
		if err := tx.AddDependency(ctx, &types.Dependency{IssueID: "txw-wisp", DependsOnID: "txw-block", Type: types.DepBlocks}, "tester"); err != nil {
			return err
		}
		if _, err := tx.ImportIssueComment(ctx, "txw-wisp", "tester", "wisp note", time.Now().UTC()); err != nil {
			return err
		}

		// Single-tier reads ARE read-your-writes on the wisp tier.
		page, err := tx.GetIssueCommentsPage(ctx, "txw-wisp", storage.CommentPageCursor{}, 0)
		if err != nil {
			return err
		}
		if len(page) != 1 || page[0].Text != "wisp note" {
			t.Errorf("in-tx GetIssueCommentsPage(txw-wisp) = %v, want one comment 'wisp note'", page)
		}
		isBlocked, blockers, err := tx.IsBlocked(ctx, "txw-wisp")
		if err != nil {
			return err
		}
		if !isBlocked || len(blockers) != 1 || blockers[0] != "txw-block" {
			t.Errorf("in-tx IsBlocked(txw-wisp) = %v, %v, want true, [txw-block]", isBlocked, blockers)
		}
		batch, err := tx.IsBlockedBatch(ctx, []string{"txw-wisp", "txw-block"})
		if err != nil {
			return err
		}
		if !batch["txw-wisp"] || batch["txw-block"] {
			t.Errorf("in-tx IsBlockedBatch = %v, want txw-wisp blocked, txw-block not", batch)
		}

		// Both-tiers-spanning reads do NOT yet see the uncommitted wisp edge
		// (documented two-session caveat): txw-block's only inbound edge is the
		// wisp edge, uncommitted on the ignored session.
		dependents, err := tx.GetDependentRecords(ctx, "txw-block", "", 0, "")
		if err != nil {
			return err
		}
		if len(dependents) != 0 {
			t.Errorf("in-tx GetDependentRecords(txw-block) = %v, want empty (uncommitted wisp edge not visible on regularTx)", dependents)
		}
		nDep, err := tx.CountDependentRecords(ctx, "txw-block", "")
		if err != nil {
			return err
		}
		if nDep != 0 {
			t.Errorf("in-tx CountDependentRecords(txw-block) = %d, want 0 (documented caveat)", nDep)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	// After commit, the wisp edge and comment are visible through the store.
	dependents, err := store.GetDependentRecords(ctx, "txw-block", "", 0, "")
	if err != nil {
		t.Fatalf("post-commit GetDependentRecords: %v", err)
	}
	if len(dependents) != 1 || dependents[0].IssueID != "txw-wisp" {
		t.Errorf("post-commit GetDependentRecords(txw-block) = %v, want one edge from txw-wisp", dependents)
	}
	nDep, err := store.CountDependentRecords(ctx, "txw-block", "")
	if err != nil {
		t.Fatalf("post-commit CountDependentRecords: %v", err)
	}
	if nDep != 1 {
		t.Errorf("post-commit CountDependentRecords(txw-block) = %d, want 1", nDep)
	}
	page, err := store.GetIssueCommentsPage(ctx, "txw-wisp", storage.CommentPageCursor{}, 0)
	if err != nil {
		t.Fatalf("post-commit GetIssueCommentsPage: %v", err)
	}
	if len(page) != 1 || page[0].Text != "wisp note" {
		t.Errorf("post-commit GetIssueCommentsPage(txw-wisp) = %v, want one comment 'wisp note'", page)
	}
}
