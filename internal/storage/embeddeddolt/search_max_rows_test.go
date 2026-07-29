//go:build cgo

// be-x42v.3: storage-layer behavioral coverage of SearchIssues + MaxRows
// against the embedded Dolt backend. The cap enforcement itself lives in
// issueops.SearchIssuesInTx / EnforceMaxRowsCap (see search_test.go in
// issueops); these tests confirm the cap fires end-to-end through the
// embedded backend's real SQL path on a real fixture.
//
// Per-backend parity vs the dolt-server backend is implicit: both
// SearchIssues entry points are thin wrappers around the same
// issueops.SearchIssuesInTx, so cap behavior is shared by construction.
// The dolt-server-side counterpart of these tests lives in
// internal/storage/dolt/search_max_rows_test.go and uses the same
// fixture (6 open task issues) and the same assertions.

package embeddeddolt_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedSearchIssues creates n open task issues for SearchIssues fixtures.
// Returns nothing (the caller queries via SearchIssues).
func seedSearchIssues(t *testing.T, te *testEnv, n int) {
	t.Helper()
	ctx := t.Context()
	for i := 0; i < n; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("%s-mr-%d", te.database, i),
			Title:     fmt.Sprintf("Search max-rows fixture %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", iss.ID, err)
		}
	}
}

func TestSearchIssues_MaxRows_NotExceeded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mrne")
	seedSearchIssues(t, te, 3)

	// 3 rows, cap=5 → success, no error, all 3 returned.
	results, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
	skipUnlessEmbeddedDolt(t)

	t.Run("FlagSource", func(t *testing.T) {
		te := newTestEnv(t, "mref")
		seedSearchIssues(t, te, 6)

		_, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
		// Storage layer issues LIMIT cap+1 to sniff overage — Found is the
		// observed row count when the cap fired, which equals cap+1 in
		// practice (the +1 sniff probe).
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
		te := newTestEnv(t, "mree")
		seedSearchIssues(t, te, 6)

		_, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mrzc")
	seedSearchIssues(t, te, 6)

	// MaxRows=0 is the disabled form. With 6 rows and cap=0, no error.
	results, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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

// TestSearchIssues_MaxRows_WithLimit exercises the four-quadrant matrix
// (Limit, MaxRows) from designer §3.1:
//
//	Limit   MaxRows   Effective                                  Cap fires?
//	─────   ───────   ─────────                                  ──────────
//	0       0         unlimited                                  no
//	0       N         LIMIT N+1 (sniff)                          yes when >N
//	L       0         LIMIT L                                    no
//	L<=N    N         LIMIT L (limit wins, no overage detection) no
//	L>N     N         LIMIT N+1 (sniff)                          yes when >N
//
// The helper-level cases (Limit, MaxRows shape selection) are covered by
// TestEffectiveSearchLimit; these cases verify the end-to-end behavior
// against a real 6-row fixture.
func TestSearchIssues_MaxRows_WithLimit(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("LimitUnderCap_NoError", func(t *testing.T) {
		te := newTestEnv(t, "mrlu")
		seedSearchIssues(t, te, 6)

		results, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
		te := newTestEnv(t, "mrle")
		seedSearchIssues(t, te, 6)

		// Limit == MaxRows: storage emits LIMIT N (no +1 sniff), so no
		// overage detection. EffectiveSearchLimit's "limit equals cap →
		// limit" branch.
		results, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
		te := newTestEnv(t, "mrlo")
		seedSearchIssues(t, te, 6)

		// Limit=10, MaxRows=3 with 6 rows: EffectiveSearchLimit picks
		// MaxRows+1=4, scan returns 4, cap fires.
		_, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
		te := newTestEnv(t, "mroc")
		seedSearchIssues(t, te, 6)

		// No Limit set, MaxRows=4 with 6 rows: storage issues LIMIT 5
		// (cap+1), gets 5 rows, cap fires.
		_, err := te.store.SearchIssues(t.Context(), "", types.IssueFilter{
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
