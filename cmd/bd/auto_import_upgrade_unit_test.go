package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeFallbackStore satisfies storage.DoltStorage via an embedded nil
// interface (any unimplemented method panics) and returns a configurable
// Statistics. It does NOT implement jsonlImporter, so the type assertion
// in maybeAutoImportJSONL fails and the server-mode fallback path is taken
// — exactly the path that lacked an emptiness guard prior to the fix.
type fakeFallbackStore struct {
	storage.DoltStorage // nil — panics on any non-overridden method
	statsTotalIssues    int
}

func (f *fakeFallbackStore) GetStatistics(_ context.Context) (*types.Statistics, error) {
	return &types.Statistics{TotalIssues: f.statsTotalIssues}, nil
}

func writeAutoImportFixtureJSONL(t *testing.T, dir string) {
	t.Helper()
	// Minimal valid issue line. Contents are irrelevant for the
	// skip-when-non-empty test (returns before parseJSONLFile) and are
	// only required to be parseable for the negative-control test.
	line := `{"_type":"issue","id":"unit-1","title":"unit-test-fixture","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func swapFallbackImporter(t *testing.T, returnErr error) *atomic.Int32 {
	t.Helper()
	orig := fallbackImporter
	var count atomic.Int32
	fallbackImporter = func(_ context.Context, _ storage.DoltStorage, _ string) (*importLocalResult, error) {
		count.Add(1)
		if returnErr != nil {
			return nil, returnErr
		}
		return &importLocalResult{}, nil
	}
	t.Cleanup(func() { fallbackImporter = orig })
	return &count
}

// TestMaybeAutoImportJSONL_ServerModeFallback_SkipsWhenNonEmpty is the
// regression test for the auto-import-on-non-empty data-clobber bug
// introduced upstream by PR #3630.
//
// Pre-fix, maybeAutoImportJSONL had no top-level emptiness guard for
// stores that did not implement jsonlImporter (i.e. server-mode dolt).
// Every command unconditionally invoked importFromLocalJSONLFull, which
// UPSERTs JSONL contents on top of live Dolt rows — silently clobbering
// recent partial-update writes whose values had not yet been re-exported
// to JSONL.
//
// This test fails on the buggy code (counter == 1) and passes after the
// guard is restored (counter == 0).
func TestMaybeAutoImportJSONL_ServerModeFallback_SkipsWhenNonEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer should not run"))

	store := &fakeFallbackStore{statsTotalIssues: 5}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 0 {
		t.Fatalf("regression: server-mode fallback importer was invoked %d time(s) on a non-empty store; expected 0 (top-level emptiness guard missing or broken)", got)
	}
}

// TestMaybeAutoImportJSONL_ServerModeFallback_RunsWhenEmpty is the negative
// control. Without it, a future change that always short-circuits would
// leave the regression test above passing vacuously.
//
// The substituted importer returns an error to short-circuit before
// s.Commit is called, so the bare fakeFallbackStore (which panics on
// every other method) does not need the full DoltStorage surface.
func TestMaybeAutoImportJSONL_ServerModeFallback_RunsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer: short-circuit before s.Commit"))

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 1 {
		t.Fatalf("server-mode fallback importer invoked %d time(s) on empty store; expected exactly 1", got)
	}
}
