//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestJiraSync_FilterHasZeroMaxRows is the be-x42v.4 opt-out gate for
// the Jira sync path. Asserts that the sync engine's SearchIssues calls
// (internal/tracker/engine.go:246, :315, etc.) never carry MaxRows>0
// or a non-empty MaxRowsSource. A BEADS_MAX_ROWS-tripped sync would
// abort mid-conflict-detection or mid-push, leaving partial state on
// either side of the boundary.
//
// Path under test: tracker.Engine.DetectConflicts at engine.go:226. The
// engine builds its filter with `types.IssueFilter{}` (zero value),
// which gives MaxRows=0 / MaxRowsSource="" implicitly. This gate
// catches a future refactor that swaps in a builder helper that pulls
// MaxRows from the resolver — silent corruption of the sync contract.
//
// We construct a minimal stubMaxRowsTracker satisfying the
// tracker.IssueTracker interface (13 methods) rather than wiring a real
// Jira tracker. The tracker's only role in DetectConflicts is to scope
// external-ref matching; returning IsExternalRef=true makes every
// seeded row a candidate and exercises the SearchIssues call.
//
// Build tags: cgo (matches existing cgo cmd/bd tests).
func TestJiraSync_FilterHasZeroMaxRows(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saved := saveAndRestoreGlobals(t)
	_ = saved

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	inner := newTestStore(t, testDBPath)
	spy := newFilterCapturingStore(inner)
	store = spy
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()
	rootCtx = ctx

	// last_sync seeded so DetectConflicts proceeds past the early
	// "no previous sync" return at engine.go:235.
	if err := inner.SetLocalMetadata(ctx, "stubmr.last_sync", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetLocalMetadata: %v", err)
	}

	// Seed one row with an external ref so DetectConflicts has
	// something to consider (defending against an early bail-out that
	// would skip the spy capture).
	if _, err := inner.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, external_ref) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"jira-mr-1", "Jira MaxRows gate seed", "", "", "", "", "open", 1, "task", "https://stubmr.test/EXT-1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	engine := tracker.NewEngine(&stubMaxRowsTracker{}, spy, "tester")
	if _, err := engine.DetectConflicts(ctx); err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}

	if spy.Calls() == 0 {
		t.Fatal("DetectConflicts did not invoke SearchIssues; gate did not exercise the filter")
	}

	for i, f := range spy.AllFilters() {
		if f.MaxRows != 0 {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRows=%d; jira sync must opt out (MaxRows=0) — designer §4.1 cross-system round-trip", i, f.MaxRows)
		}
		if f.MaxRowsSource != "" {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRowsSource=%q; jira sync must opt out (MaxRowsSource=\"\")", i, f.MaxRowsSource)
		}
	}
}

// stubMaxRowsTracker satisfies tracker.IssueTracker with no-op
// behavior. It exists solely to feed tracker.NewEngine in the
// be-x42v.4 MaxRows gate test; only the methods DetectConflicts
// actually touches (ConfigPrefix, IsExternalRef, DisplayName) produce
// non-trivial values. Every other method is a tracker-side no-op so
// the spy's storage-side capture is the only side effect of running
// DetectConflicts.
//
// Named distinctly from any be-uwvs.5 stubTracker so the two gate
// tests coexist in the same package once both beads land.
type stubMaxRowsTracker struct{}

func (*stubMaxRowsTracker) Name() string                                    { return "stubmr" }
func (*stubMaxRowsTracker) DisplayName() string                             { return "StubMR" }
func (*stubMaxRowsTracker) ConfigPrefix() string                            { return "stubmr" }
func (*stubMaxRowsTracker) Init(_ context.Context, _ storage.Storage) error { return nil }
func (*stubMaxRowsTracker) Validate() error                                 { return nil }
func (*stubMaxRowsTracker) Close() error                                    { return nil }
func (*stubMaxRowsTracker) FieldMapper() tracker.FieldMapper                { return nil }
func (*stubMaxRowsTracker) IsExternalRef(_ string) bool                     { return true }
func (*stubMaxRowsTracker) ExtractIdentifier(_ string) string               { return "" }
func (*stubMaxRowsTracker) BuildExternalRef(_ *tracker.TrackerIssue) string { return "" }

func (*stubMaxRowsTracker) FetchIssues(_ context.Context, _ tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	return nil, nil
}
func (*stubMaxRowsTracker) FetchIssue(_ context.Context, _ string) (*tracker.TrackerIssue, error) {
	return nil, nil
}
func (*stubMaxRowsTracker) CreateIssue(_ context.Context, _ *types.Issue) (*tracker.TrackerIssue, error) {
	return nil, nil
}
func (*stubMaxRowsTracker) UpdateIssue(_ context.Context, _ string, _ *types.Issue) (*tracker.TrackerIssue, error) {
	return nil, nil
}
