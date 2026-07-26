package beads_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads"
)

// TestParseClaimConflictAgainstRealDolt is the end-to-end producer→parser proof
// for the string-coupled claim shim: it drives a REAL Dolt-backed claim into
// both conflict branches through the public surface and asserts
// beads.ParseClaimConflict recovers the embedded assignee/status. It doubles as
// the AsIssueClaimer drift exercise (finding: assert the claim surface resolves
// off a live Storage), so a decorator that dropped ClaimIssue would fail here.
func TestParseClaimConflictAgainstRealDolt(t *testing.T) {
	skipIfNoDoltServer(t)

	ctx := context.Background()
	store, err := beads.Open(ctx, filepath.Join(t.TempDir(), "rt-dolt"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "rt"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	claimer, ok := beads.AsIssueClaimer(store)
	if !ok {
		t.Fatalf("AsIssueClaimer returned ok=false for a live Dolt Storage")
	}

	mk := func(id string) {
		iss := &beads.Issue{ID: id, Title: id, Status: beads.StatusOpen, Priority: 2, IssueType: beads.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}

	// Already-claimed: alice holds rt-1; bob's claim must recover assignee="alice".
	mk("rt-1")
	if err := claimer.ClaimIssue(ctx, "rt-1", "alice"); err != nil {
		t.Fatalf("claim rt-1 as alice: %v", err)
	}
	err = claimer.ClaimIssue(ctx, "rt-1", "bob")
	if !errors.Is(err, beads.ErrAlreadyClaimed) {
		t.Fatalf("bob's claim error does not wrap ErrAlreadyClaimed: %v", err)
	}
	cc, ok := beads.ParseClaimConflict(err)
	if !ok {
		t.Fatalf("ParseClaimConflict returned ok=false for an ErrAlreadyClaimed error")
	}
	if cc.CurrentAssignee != "alice" {
		t.Fatalf("CurrentAssignee = %q, want %q (from %v)", cc.CurrentAssignee, "alice", err)
	}
	if cc.CurrentStatus != "" {
		t.Fatalf("CurrentStatus = %q, want empty on an already-claimed conflict", cc.CurrentStatus)
	}

	// Not-claimable: a closed issue's claim must recover status="closed".
	mk("rt-2")
	if err := store.CloseIssue(ctx, "rt-2", "done", "tester", ""); err != nil {
		t.Fatalf("close rt-2: %v", err)
	}
	err = claimer.ClaimIssue(ctx, "rt-2", "carol")
	if !errors.Is(err, beads.ErrNotClaimable) {
		t.Fatalf("closed-issue claim error does not wrap ErrNotClaimable: %v", err)
	}
	cc, ok = beads.ParseClaimConflict(err)
	if !ok {
		t.Fatalf("ParseClaimConflict returned ok=false for an ErrNotClaimable error")
	}
	if cc.CurrentStatus != string(beads.StatusClosed) {
		t.Fatalf("CurrentStatus = %q, want %q (from %v)", cc.CurrentStatus, beads.StatusClosed, err)
	}
}
