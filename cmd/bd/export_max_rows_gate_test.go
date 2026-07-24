//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestExport_FilterHasZeroMaxRows is the be-x42v.4 opt-out gate for
// `bd export`. Asserts that runExport never constructs an IssueFilter
// with MaxRows>0 or a non-empty MaxRowsSource — both would let an
// inherited BEADS_MAX_ROWS env var abort the export mid-run and leave
// an incomplete JSONL backup on disk.
//
// Today's filter construction lives at cmd/bd/export.go:94-98:
//
//	filter := types.IssueFilter{
//	    Limit:         0,
//	    MaxRows:       0,
//	    MaxRowsSource: "",
//	}
//
// The two MaxRows fields are explicitly initialized to the zero values
// per the designer §4.1 opt-out rule for data-integrity paths. A future
// refactor that removes them — or pipes BEADS_MAX_ROWS through some
// shared resolver — would silently break round-trip integrity. This
// gate prevents that regression at SearchIssues call time.
//
// Failure modes this gate prevents:
//
//   - A future "smart export" change starts honoring BEADS_MAX_ROWS to
//     "warn the user on huge backups," silently corrupting backups when
//     the env var is set in CI.
//   - A future shared `IssueFilter` builder helper defaults MaxRows to
//     the resolver value rather than 0.
//   - A future MaxRowsSource normalization step prepends a non-empty
//     source string and tips ErrTooManyRows into the "cap fired" branch.
//
// Build tags: cgo (matches existing cgo cmd/bd tests at e.g.
// export_test.go:1).
func TestExport_FilterHasZeroMaxRows(t *testing.T) {
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

	// Seed one row so runExport has something to iterate (defending
	// against an early-return bail-out that would skip the spy capture).
	if _, err := inner.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gate-mr-1", "MaxRows gate seed", "body", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	exportOutput = filepath.Join(tmpDir, "out.jsonl")
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	exportIncludeMemories = false
	exportNoMemories = false
	t.Cleanup(func() { exportOutput = "" })

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	if spy.Calls() == 0 {
		t.Fatal("runExport did not invoke SearchIssues; gate did not exercise the filter")
	}

	for i, f := range spy.AllFilters() {
		if f.MaxRows != 0 {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRows=%d; bd export must opt out (MaxRows=0) — designer §4.1 round-trip integrity", i, f.MaxRows)
		}
		if f.MaxRowsSource != "" {
			t.Errorf("be-x42v.4 gate: SearchIssues call %d had filter.MaxRowsSource=%q; bd export must opt out (MaxRowsSource=\"\") — a non-empty source would tip ErrTooManyRows into the cap-fired branch", i, f.MaxRowsSource)
		}
	}
}

// TestExport_BypassesBeadsMaxRows is the be-x42v.4 end-to-end behavioral
// test (designer §6.4) that verifies BEADS_MAX_ROWS=1 in the process
// environment does NOT limit the row count of `bd export`. Belt-and-
// suspenders alongside the filter spy above: the spy asserts on the
// internal filter construction; this test asserts on the observable
// output line count, catching any path where a shared resolver or an
// output-layer cap could still truncate the JSONL stream even when the
// SearchIssues filter is correct.
//
// The env var is set via t.Setenv so it is automatically restored after
// the test; it does NOT bleed into other tests in the suite.
func TestExport_BypassesBeadsMaxRows(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	t.Setenv("BEADS_MAX_ROWS", "1")

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
	s := newTestStore(t, testDBPath)
	store = s
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

	// Seed 5 regular task issues. bd export excludes infra types,
	// templates, and ephemeral wisps by default — "task" rows pass all
	// three filters and will appear in the JSONL output.
	const wantRows = 5
	for i := range wantRows {
		id := "bypass-mr-" + strconv.Itoa(i+1)
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "bypass seed "+strconv.Itoa(i+1), "body", "", "", "", "open", 1, "task",
		); err != nil {
			t.Fatalf("seed issue %d: %v", i+1, err)
		}
	}

	exportFile := filepath.Join(tmpDir, "bypass-out.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	exportIncludeMemories = false
	exportNoMemories = false
	t.Cleanup(func() { exportOutput = "" })

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	lines := splitJSONL(data)
	if len(lines) != wantRows {
		t.Errorf("be-x42v.4 bypass: BEADS_MAX_ROWS=1 truncated export: got %d JSONL lines, want %d — bd export must ignore the env cap (designer §4.1 opt-out)", len(lines), wantRows)
	}
}
