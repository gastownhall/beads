package dolt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func readHolderToken(t *testing.T, ctx context.Context, store *DoltStore, table, id string) string {
	t.Helper()
	var tok string
	//nolint:gosec // table is a test-controlled constant
	if err := store.db.QueryRowContext(ctx,
		"SELECT holder_token FROM "+table+" WHERE id = ?", id).Scan(&tok); err != nil {
		t.Fatalf("read holder_token %s.%s: %v", table, id, err)
	}
	return tok
}

// TestClaimRecordsHolderToken: a claim stamps the caller's ambient holder
// token (from context) onto the row.
func TestClaimRecordsHolderToken(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "ht-claim")
	claimCtx := issueops.WithHolderToken(ctx, "tok-incarnation-1")
	if err := store.ClaimIssue(claimCtx, "ht-claim", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "ht-claim"); got != "tok-incarnation-1" {
		t.Errorf("holder_token = %q, want tok-incarnation-1", got)
	}
}

// TestUnclaimClearsHolderToken: releasing ownership clears the token so a
// stale process can never match a later claim.
func TestUnclaimClearsHolderToken(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "ht-unclaim")
	if err := store.ClaimIssue(issueops.WithHolderToken(ctx, "tok-1"), "ht-unclaim", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.UnclaimIssue(ctx, "ht-unclaim", "alice", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "ht-unclaim"); got != "" {
		t.Errorf("holder_token after unclaim = %q, want empty", got)
	}
}

// TestReclaimClearsHolderToken: a lease reclaim takes ownership away and
// clears the token, so the reclaimed-out worker's token no longer matches.
func TestReclaimClearsHolderToken(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "ht-reclaim")
	claimCtx := issueops.WithHolderToken(issueops.WithLeaseTTL(ctx, time.Second), "tok-dead")
	if err := store.ClaimIssue(claimCtx, "ht-reclaim", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	time.Sleep(3 * time.Second)
	if _, err := store.ReclaimExpiredLeases(ctx, 0, "reaper"); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "ht-reclaim"); got != "" {
		t.Errorf("holder_token after reclaim = %q, want empty", got)
	}
}

// TestHolderTokenNeverSurfaced: the token is enforcement-only state — it must
// not appear in the hydrated issue's JSON or any read surface, so a
// fenced-out zombie cannot recover the current token via `bd show`.
func TestHolderTokenNeverSurfaced(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "ht-hidden")
	if err := store.ClaimIssue(issueops.WithHolderToken(ctx, "secret-token"), "ht-hidden", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err := store.GetIssue(ctx, "ht-hidden")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob := string(b)
	if strings.Contains(blob, "secret-token") || strings.Contains(blob, "holder_token") {
		t.Errorf("holder_token leaked into hydrated issue: %s", blob)
	}
	if got.ClaimFence == 0 {
		t.Error("sanity: claim should have bumped the fence")
	}
}

// TestImportAssigneeChangeClearsHolderToken: an import that changes the owner
// clears the stale token (a foreign import cannot carry a valid incarnation
// token for the new owner).
func TestImportAssigneeChangeClearsHolderToken(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedOpenIssue(t, ctx, store, "ht-import")
	if err := store.ClaimIssue(issueops.WithHolderToken(ctx, "tok-alice"), "ht-import", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// An import upsert that reassigns to bob (strictly newer) clears the token.
	snapshot := &types.Issue{
		ID:        "ht-import",
		Title:     "fence ht-import",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "bob",
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		UpdatedAt: time.Now().UTC().Add(time.Minute),
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := issueops.InsertIssueIntoTable(ctx, tx, "issues", snapshot); err != nil {
		_ = tx.Rollback()
		t.Fatalf("import upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "ht-import"); got != "" {
		t.Errorf("holder_token after reassigning import = %q, want empty", got)
	}
}
