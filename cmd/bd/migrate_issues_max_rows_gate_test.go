//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestMigrateIssues_FilterHasZeroMaxRows is the be-x42v.4 opt-out gate
// for `bd migrate-issues`. Asserts that the migration's candidate-find
// filter — built in findCandidateIssues at cmd/bd/migrate_issues.go:269
// — never carries MaxRows>0 or a non-empty MaxRowsSource. A
// BEADS_MAX_ROWS-tripped migration would leave the source and destination
// repos in inconsistent states; the cap must be hard-opt-out here.
//
// Why findCandidateIssues and not executeMigrateIssues end-to-end:
//
//   - executeMigrateIssues also calls validateRepos (lines 233/254),
//     which intentionally uses Limit=1 for existence probing. Those
//     calls also opt out of the cap (MaxRows=0) and would be covered
//     by AllFilters() — but the candidate-find call is the principal
//     round-trip path, and isolating it from the migration plumbing
//     (orphan checks, plan display, confirmation gate) keeps this gate
//     focused on the property under test.
//   - findCandidateIssues IS the round-trip candidate scan. Its filter
//     has MaxRows=0 (no cap, by design) and a SourceRepo discriminator.
//
// If a future refactor moves the candidate-find SearchIssues call into
// a different function, the gate's import line will fail to compile —
// that's a load-bearing signal to re-target the test.
//
// Build tags: cgo (matches existing cgo cmd/bd tests).
func TestMigrateIssues_FilterHasZeroMaxRows(t *testing.T) {
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

	// Seed one row in the source repo so findCandidateIssues has
	// something to find. The migration uses SourceRepo as the
	// filter discriminator.
	if _, err := inner.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, source_repo) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"mig-mr-1", "Migration MaxRows gate seed", "body", "", "", "", "open", 1, "task", "source-repo-a"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	params := migrateIssuesParams{
		from:     "source-repo-a",
		to:       "source-repo-b",
		priority: -1, // sentinel: "any priority"
	}
	if _, err := findCandidateIssues(ctx, spy, params); err != nil {
		t.Fatalf("findCandidateIssues: %v", err)
	}

	if spy.Calls() == 0 {
		t.Fatal("findCandidateIssues did not invoke SearchIssues; gate did not exercise the filter")
	}

	for i, f := range spy.AllFilters() {
		if f.MaxRows != 0 {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRows=%d; bd migrate-issues must opt out (MaxRows=0) — designer §4.1 cross-backend round-trip", i, f.MaxRows)
		}
		if f.MaxRowsSource != "" {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRowsSource=%q; bd migrate-issues must opt out (MaxRowsSource=\"\")", i, f.MaxRowsSource)
		}
	}
}
