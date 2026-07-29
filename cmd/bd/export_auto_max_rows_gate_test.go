//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestExportAuto_FilterHasZeroMaxRows is the be-x42v.4 opt-out gate for
// `bd export --auto` (the incremental auto-export run from
// PersistentPostRun). Asserts that exportToFile — the shared helper
// behind both `bd export -o` and auto-export, at cmd/bd/export_auto.go:140
// — never constructs an IssueFilter with MaxRows>0 or a non-empty
// MaxRowsSource.
//
// The auto-export path is particularly sensitive: it runs as a side
// effect of nearly every bd command and writes a git-tracked JSONL
// snapshot. If a stray BEADS_MAX_ROWS in CI tipped the filter into the
// cap branch, the auto-export would fail and every subsequent bd
// invocation would re-attempt it, propagating the error to every
// command-completion. This gate prevents that regression at write time.
//
// We invoke exportToFile directly (rather than maybeAutoExport, which
// runs guard checks first) so the test exercises the round-trip path
// without depending on config.export.auto being set in the test rig.
//
// Build tags: cgo (matches existing cgo cmd/bd tests).
func TestExportAuto_FilterHasZeroMaxRows(t *testing.T) {
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

	if _, err := inner.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gate-auto-mr-1", "Auto MaxRows gate seed", "body", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	outPath := filepath.Join(tmpDir, "auto-export.jsonl")
	if _, _, err := exportToFile(ctx, outPath, false); err != nil {
		t.Fatalf("exportToFile: %v", err)
	}

	if spy.Calls() == 0 {
		t.Fatal("exportToFile did not invoke SearchIssues; gate did not exercise the filter")
	}

	for i, f := range spy.AllFilters() {
		if f.MaxRows != 0 {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRows=%d; bd export --auto must opt out (MaxRows=0) — designer §4.1 snapshot integrity", i, f.MaxRows)
		}
		if f.MaxRowsSource != "" {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRowsSource=%q; bd export --auto must opt out (MaxRowsSource=\"\")", i, f.MaxRowsSource)
		}
	}
}
