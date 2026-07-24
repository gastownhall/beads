//go:build cgo

// be-u8z9: GetReadyWork honors WorkFilter.MaxRows. These integration tests
// exercise the storage path via newTestStore (matches the pattern in
// metadata_ready_test.go); the *Filter cap is not exercised by the helper
// unit tests in internal/storage/issueops/search_test.go, which only cover
// EffectiveSearchLimit + EnforceMaxRowsCap in isolation.

package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestGetReadyWork_MaxRowsSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	const totalReady = 6
	for i := 0; i < totalReady; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("mr-rw-%d", i),
			Title:     fmt.Sprintf("Ready max-rows %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", iss.ID, err)
		}
	}

	t.Run("UnderCap", func(t *testing.T) {
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			MaxRows:       100,
			MaxRowsSource: "--max-rows",
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) < totalReady {
			t.Errorf("expected at least %d results, got %d", totalReady, len(results))
		}
	})

	t.Run("OverCap", func(t *testing.T) {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			MaxRows:       3,
			MaxRowsSource: "--max-rows",
		})
		if err == nil {
			t.Fatal("expected ErrTooManyRows, got nil")
		}
		var typed *issueops.ErrTooManyRows
		if !errors.As(err, &typed) {
			t.Fatalf("expected *ErrTooManyRows, got %T: %v", err, err)
		}
		if typed.Found <= 3 {
			t.Errorf("ErrTooManyRows.Found = %d, want > 3", typed.Found)
		}
		if typed.Cap != 3 {
			t.Errorf("ErrTooManyRows.Cap = %d, want 3", typed.Cap)
		}
		if typed.Source != "--max-rows" {
			t.Errorf("ErrTooManyRows.Source = %q, want %q", typed.Source, "--max-rows")
		}
	})

	t.Run("OverCap_EnvSource", func(t *testing.T) {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			MaxRows:       2,
			MaxRowsSource: "BEADS_MAX_ROWS",
		})
		if err == nil {
			t.Fatal("expected ErrTooManyRows, got nil")
		}
		var typed *issueops.ErrTooManyRows
		if !errors.As(err, &typed) {
			t.Fatalf("expected *ErrTooManyRows, got %T: %v", err, err)
		}
		if typed.Source != "BEADS_MAX_ROWS" {
			t.Errorf("ErrTooManyRows.Source = %q, want %q", typed.Source, "BEADS_MAX_ROWS")
		}
	})

	t.Run("Zero_Disabled", func(t *testing.T) {
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			MaxRows:       0,
			MaxRowsSource: "",
		})
		if err != nil {
			t.Fatalf("GetReadyWork with MaxRows=0: %v", err)
		}
		if len(results) < totalReady {
			t.Errorf("expected at least %d results with disabled cap, got %d", totalReady, len(results))
		}
	})

	t.Run("WithLimit_LimitUnderCap_NoError", func(t *testing.T) {
		// Limit caps at 2 well below MaxRows=100: cap never fires.
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			Limit:         2,
			MaxRows:       100,
			MaxRowsSource: "--max-rows",
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected exactly 2 results, got %d", len(results))
		}
	})

	t.Run("WithLimit_LimitOverCap_FiresCap", func(t *testing.T) {
		// Limit=10, MaxRows=3 with 6 rows in DB: EffectiveSearchLimit returns
		// 4 (=cap+1), GetReadyWork scans 4 and EnforceMaxRowsCap fires.
		_, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			Limit:         10,
			MaxRows:       3,
			MaxRowsSource: "BEADS_MAX_ROWS",
		})
		if err == nil {
			t.Fatal("expected ErrTooManyRows, got nil")
		}
		var typed *issueops.ErrTooManyRows
		if !errors.As(err, &typed) {
			t.Fatalf("expected *ErrTooManyRows, got %T: %v", err, err)
		}
		if typed.Cap != 3 {
			t.Errorf("ErrTooManyRows.Cap = %d, want 3", typed.Cap)
		}
		if typed.Source != "BEADS_MAX_ROWS" {
			t.Errorf("ErrTooManyRows.Source = %q, want %q", typed.Source, "BEADS_MAX_ROWS")
		}
	})

	t.Run("WithLimit_LimitAtCap_NoError", func(t *testing.T) {
		// Limit==MaxRows: storage emits LIMIT N (no +1 sniff), returns N rows
		// without overage detection. Matches EffectiveSearchLimit's "limit
		// equals cap → limit" branch.
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:        "open",
			Limit:         3,
			MaxRows:       3,
			MaxRowsSource: "--max-rows",
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected exactly 3 results at limit==cap, got %d", len(results))
		}
	})
}

// TestClaimReadyIssue_MaxRows_LargePoolUnderLowCap is the be-x42v.4-follow-up
// regression for ClaimReadyIssueInTx: a rig-wide BEADS_MAX_ROWS cap (sized
// for bulk list/ready reads) must never fail a claim, which only ever
// consumes a single row regardless of how large the ready pool is.
// Before the claim.go:206 fix, ClaimReadyIssueInTx forwarded MaxRows into
// its internal unbounded GetReadyWorkInTx scan, so a ready pool bigger than
// the cap made every claim attempt exit with ErrTooManyRows.
func TestClaimReadyIssue_MaxRows_LargePoolUnderLowCap(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	// Ready pool (10) is well above the cap (3) — this is exactly the
	// agent-rig scenario the flag targets: a low defensive cap on bulk
	// reads, but claim must still succeed.
	const totalReady = 10
	for i := 0; i < totalReady; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("mr-claim-%d", i),
			Title:     fmt.Sprintf("Claim max-rows %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", iss.ID, err)
		}
	}

	claimed, err := store.ClaimReadyIssue(ctx, types.WorkFilter{
		Status:        "open",
		MaxRows:       3,
		MaxRowsSource: "BEADS_MAX_ROWS",
	}, "claimant")
	if err != nil {
		t.Fatalf("ClaimReadyIssue under cap=3 with %d-issue ready pool: unexpected error: %v", totalReady, err)
	}
	if claimed == nil {
		t.Fatal("ClaimReadyIssue returned nil issue, want a claimed issue")
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("claimed issue status = %q, want %q", claimed.Status, types.StatusInProgress)
	}
}
