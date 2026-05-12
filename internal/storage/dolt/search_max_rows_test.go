// be-x42v.3: storage-layer behavioral coverage of SearchIssues + MaxRows
// against the dolt-server backend.
//
// Parity with the embedded backend is implicit: both SearchIssues entry
// points are thin wrappers around issueops.SearchIssuesInTx, so the cap
// behavior is shared by construction. The embedded counterpart of these
// tests lives in internal/storage/embeddeddolt/search_max_rows_test.go
// and exercises the same fixture and assertions; if either backend
// diverges from the cap contract, both tests will not stay in sync.

package dolt

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedSearchMaxRowsIssues creates n open task issues for SearchIssues fixtures.
func seedSearchMaxRowsIssues(t *testing.T, ctx context.Context, store *DoltStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("smr-%d", i),
			Title:     fmt.Sprintf("Search max-rows fixture %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", iss.ID, err)
		}
	}
}

func TestSearchIssues_MaxRows_NotExceeded(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchMaxRowsIssues(t, ctx, store, 3)

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       5,
		MaxRowsSource: "--max-rows",
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 rows under cap, got %d", len(results))
	}
}

func TestSearchIssues_MaxRows_Exceeded_ReturnsErrTooManyRows(t *testing.T) {
	t.Run("FlagSource", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
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

	t.Run("EnvSource", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
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
		if typed.Cap != 2 {
			t.Errorf("ErrTooManyRows.Cap = %d, want 2", typed.Cap)
		}
	})
}

func TestSearchIssues_MaxRows_Zero_NoCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchMaxRowsIssues(t, ctx, store, 6)

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       0,
		MaxRowsSource: "",
	})
	if err != nil {
		t.Fatalf("SearchIssues with MaxRows=0: %v", err)
	}
	if len(results) != 6 {
		t.Errorf("expected 6 rows with cap disabled, got %d", len(results))
	}
}

// TestSearchIssues_MaxRows_WithLimit covers the four-quadrant (Limit,
// MaxRows) matrix end-to-end against a 6-row fixture. The helper-level
// shape selection is covered by issueops.TestEffectiveSearchLimit.
func TestSearchIssues_MaxRows_WithLimit(t *testing.T) {
	t.Run("LimitUnderCap_NoError", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			Limit:         3,
			MaxRows:       100,
			MaxRowsSource: "--max-rows",
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 rows with limit=3 under cap, got %d", len(results))
		}
	})

	t.Run("LimitEqualsCap_NoError", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			Limit:         3,
			MaxRows:       3,
			MaxRowsSource: "--max-rows",
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 rows with limit==cap, got %d", len(results))
		}
	})

	t.Run("LimitOverCap_FiresCap", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
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

	t.Run("OnlyCap_FiresOnOverage", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		seedSearchMaxRowsIssues(t, ctx, store, 6)

		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MaxRows:       4,
			MaxRowsSource: "--max-rows",
		})
		if err == nil {
			t.Fatal("expected ErrTooManyRows, got nil")
		}
		var typed *issueops.ErrTooManyRows
		if !errors.As(err, &typed) {
			t.Fatalf("expected *ErrTooManyRows, got %T: %v", err, err)
		}
		if typed.Cap != 4 {
			t.Errorf("ErrTooManyRows.Cap = %d, want 4", typed.Cap)
		}
	})
}
