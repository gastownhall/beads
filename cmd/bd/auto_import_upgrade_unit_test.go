package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeFallbackStore satisfies storage.DoltStorage via an embedded nil
// interface (any unimplemented method panics) and returns a configurable
// Statistics. It does NOT implement jsonlImporter, so the type assertion
// in maybeAutoImportJSONL fails and the fallback importer path is taken
// — exactly the path that lacked an emptiness guard prior to the fix.
type fakeFallbackStore struct {
	storage.DoltStorage // nil — panics on any non-overridden method
	statsTotalIssues    int
	statsNil            bool
	getStatistics       func(context.Context) (*types.Statistics, error)
}

func (f *fakeFallbackStore) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	if f.getStatistics != nil {
		return f.getStatistics(ctx)
	}
	if f.statsNil {
		return nil, nil
	}
	return &types.Statistics{TotalIssues: f.statsTotalIssues}, nil
}

func (f *fakeFallbackStore) Commit(_ context.Context, _ string) error { return nil }

func writeAutoImportFixtureJSONL(t *testing.T, dir string) {
	t.Helper()
	writeAutoImportFixtureJSONLNamed(t, dir, "issues.jsonl")
}

func writeAutoImportFixtureJSONLNamed(t *testing.T, dir, name string) {
	t.Helper()
	// Minimal valid issue line. Contents are irrelevant for the
	// skip-when-non-empty test (returns before parseJSONLFile) and are
	// only required to be parseable for the negative-control test.
	line := `{"_type":"issue","id":"unit-1","title":"unit-test-fixture","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line+"\n"), 0o600); err != nil {
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

// TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenNonEmpty is the
// regression test for the auto-import-on-non-empty data-clobber bug
// introduced upstream by PR #3630.
//
// Pre-fix, maybeAutoImportJSONL had no top-level emptiness guard for
// stores that did not implement jsonlImporter. Every command
// unconditionally invoked importFromLocalJSONLFull, which UPSERTs JSONL
// contents on top of live Dolt rows — silently clobbering recent
// partial-update writes whose values had not yet been re-exported to
// JSONL.
//
// This test fails on the buggy code (counter == 1) and passes after the
// guard is restored (counter == 0).
func TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenNonEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer should not run"))

	store := &fakeFallbackStore{statsTotalIssues: 5}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 0 {
		t.Fatalf("regression: fallback importer was invoked %d time(s) on a non-empty store; expected 0 (top-level emptiness guard missing or broken)", got)
	}
}

// TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenStatisticsReportNonEmptyLate
// asserts the top-level emptiness guard still fires when GetStatistics returns
// late (e.g. blocked on a busy store) rather than instantly. It is a timing
// variant of SkipsWhenNonEmpty: GetStatistics ultimately reports a populated
// store, so the fallback UPSERT path must not run.
//
// Scope note — this does NOT cover the gastownhall/beads#3948 root cause. #3948
// is the case where GetStatistics wrongly returns TotalIssues=0 on a populated
// DB (a count/transaction-isolation defect), which defeats the guard entirely;
// the dangerous fallback TOCTOU is empty-at-check -> concurrent writer commits
// -> UPSERT clobbers the new rows. This test feeds a *correct* non-empty count,
// so it exercises the happy path of the guard, not that hazard. The #3948 root
// cause is tracked separately (mybd-w02o); maybeAutoImportJSONL makes a single
// synchronous GetStatistics call, so there is no second observation point a
// writer could race against at this layer.
func TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenStatisticsReportNonEmptyLate(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer should not run"))

	statsCalled := make(chan struct{})
	statsUnblocked := make(chan struct{})

	store := &fakeFallbackStore{
		getStatistics: func(ctx context.Context) (*types.Statistics, error) {
			close(statsCalled)
			select {
			case <-statsUnblocked:
				return &types.Statistics{TotalIssues: 1}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		maybeAutoImportJSONL(ctx, store, dir)
	}()

	select {
	case <-statsCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-import did not check store statistics before considering fallback import")
	}

	// Release the delayed GetStatistics so it reports a populated store.
	close(statsUnblocked)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-import did not return after statistics reported a non-empty store")
	}

	if got := count.Load(); got != 0 {
		t.Fatalf("regression: fallback importer was invoked %d time(s) after statistics reported a non-empty store; expected 0", got)
	}
}

func TestShouldRunAutoImportJSONL(t *testing.T) {
	initConfigForTest(t)
	store := &fakeFallbackStore{}
	writeCmd := &cobra.Command{Use: "update"}

	tests := []struct {
		name        string
		cmd         *cobra.Command
		store       storage.DoltStorage
		useReadOnly bool
		globalFlag  bool
		serverMode  bool
		want        bool
	}{
		{name: "write command embedded", cmd: writeCmd, store: store, want: true},
		{name: "server mode", cmd: writeCmd, store: store, serverMode: true, want: false},
		{name: "read only", cmd: writeCmd, store: store, useReadOnly: true, want: false},
		{name: "global", cmd: writeCmd, store: store, globalFlag: true, want: false},
		{name: "import command", cmd: &cobra.Command{Use: "import"}, store: store, want: false},
		{name: "nil store", cmd: writeCmd, want: false},
		{name: "nil command", store: store, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunAutoImportJSONL(tt.cmd, tt.store, tt.useReadOnly, tt.globalFlag, tt.serverMode)
			if got != tt.want {
				t.Fatalf("shouldRunAutoImportJSONL() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShouldRunAutoImportJSONL_RespectsImportAutoFalse is the regression test
// for GH#4304: bd update (and any other write command) auto-imported
// issues.jsonl into an empty database even when import.auto was explicitly
// set to false (via config.yaml or BD_IMPORT_AUTO=false). shouldRunAutoImportJSONL
// previously never consulted the import.auto config key at all — only the
// separate git-hook sync path (importJSONLForSync) checked it. This test fails
// on the buggy code (want=true) and passes once the config check is restored.
func TestShouldRunAutoImportJSONL_RespectsImportAutoFalse(t *testing.T) {
	initConfigForTest(t)
	store := &fakeFallbackStore{}
	writeCmd := &cobra.Command{Use: "update"}
	importCmd := &cobra.Command{Use: "import"}

	config.Set("import.auto", false)
	if got := shouldRunAutoImportJSONL(writeCmd, store, false, false, false); got {
		t.Fatalf("regression GH#4304: shouldRunAutoImportJSONL() = %v with import.auto=false, want false", got)
	}
	// The explicit "bd import" command must remain unaffected either way —
	// confirm it's still gated off (for a different reason) so the config
	// check above isn't the only thing keeping it from double-running.
	if got := shouldRunAutoImportJSONL(importCmd, store, false, false, false); got {
		t.Fatalf("shouldRunAutoImportJSONL() = %v for the import command with import.auto=false, want false", got)
	}

	config.Set("import.auto", true)
	if got := shouldRunAutoImportJSONL(writeCmd, store, false, false, false); !got {
		t.Fatalf("shouldRunAutoImportJSONL() = %v with import.auto=true, want true (negative control)", got)
	}
}

// TestIsDisablingImportAutoViaConfigCommand is the regression test for the
// P2 finding on gastownhall/beads#4595 (Codex cross-vendor review,
// 2026-07-07): shouldRunAutoImportJSONL runs in PersistentPreRun before
// "bd config set import.auto false" writes the new value, so the command
// meant to disable auto-import triggered it on its own invocation whenever a
// stale .beads/issues.jsonl sat next to an empty database (GH#4304). This
// exemption must fire for "config set import.auto false"/"0" and for
// "config set-many ... import.auto=false", but not for unrelated config
// keys, truthy values, or commands outside the config subtree.
func TestIsDisablingImportAutoViaConfigCommand(t *testing.T) {
	configCmd := &cobra.Command{Use: "config"}
	setCmd := &cobra.Command{Use: "set"}
	setManyCmd := &cobra.Command{Use: "set-many"}
	configCmd.AddCommand(setCmd, setManyCmd)
	writeCmd := &cobra.Command{Use: "update"}

	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
		want bool
	}{
		{name: "config set import.auto false", cmd: setCmd, args: []string{"import.auto", "false"}, want: true},
		{name: "config set import.auto 0", cmd: setCmd, args: []string{"import.auto", "0"}, want: true},
		{name: "config set import.auto true", cmd: setCmd, args: []string{"import.auto", "true"}, want: false},
		{name: "config set other key", cmd: setCmd, args: []string{"export.auto", "false"}, want: false},
		{name: "config set-many import.auto=false", cmd: setManyCmd, args: []string{"jira.url=x", "import.auto=false"}, want: true},
		{name: "config set-many import.auto=true", cmd: setManyCmd, args: []string{"import.auto=true"}, want: false},
		{name: "unrelated command", cmd: writeCmd, args: []string{"import.auto", "false"}, want: false},
		{name: "set without config parent", cmd: &cobra.Command{Use: "set"}, args: []string{"import.auto", "false"}, want: false},
		{name: "nil command", cmd: nil, args: []string{"import.auto", "false"}, want: false},
		{name: "too few args", cmd: setCmd, args: []string{"import.auto"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDisablingImportAutoViaConfigCommand(tt.cmd, tt.args); got != tt.want {
				t.Fatalf("isDisablingImportAutoViaConfigCommand(%v, %v) = %v, want %v", tt.cmd, tt.args, got, tt.want)
			}
		})
	}
}

// TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenStatisticsNil covers
// the defensive nil-statistics guard: if the store reports no error but also
// no counts, auto-import should skip rather than panic or assume emptiness.
func TestMaybeAutoImportJSONL_FallbackImporter_SkipsWhenStatisticsNil(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer should not run"))

	store := &fakeFallbackStore{statsNil: true}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 0 {
		t.Fatalf("fallback importer was invoked %d time(s) when statistics were nil; expected 0", got)
	}
}

// TestMaybeAutoImportJSONL_FallbackImporter_RunsWhenEmpty is the negative
// control. Without it, a future change that always short-circuits would
// leave the regression test above passing vacuously.
//
// The substituted importer returns an error to short-circuit before
// s.Commit is called, so the bare fakeFallbackStore (which panics on
// every other method) does not need the full DoltStorage surface.
func TestMaybeAutoImportJSONL_FallbackImporter_RunsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer: short-circuit before s.Commit"))

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 1 {
		t.Fatalf("fallback importer invoked %d time(s) on empty store; expected exactly 1", got)
	}
}

func TestMaybeAutoImportJSONL_UsesConfiguredImportPath(t *testing.T) {
	initConfigForTest(t)
	config.Set("import.path", "beads.jsonl")

	dir := t.TempDir()
	writeAutoImportFixtureJSONLNamed(t, dir, "beads.jsonl")

	orig := fallbackImporter
	var gotPath string
	fallbackImporter = func(_ context.Context, _ storage.DoltStorage, path string) (*importLocalResult, error) {
		gotPath = path
		return nil, errors.New("test importer: short-circuit before s.Commit")
	}
	t.Cleanup(func() { fallbackImporter = orig })

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)

	wantPath := filepath.Join(dir, "beads.jsonl")
	if gotPath != wantPath {
		t.Fatalf("auto-import path = %q, want %q", gotPath, wantPath)
	}
}

func TestMaybeAutoImportJSONL_FailedImportStampPreventsRepeatedImport(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	count := swapFallbackImporter(t, errors.New("test importer failed"))

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 1 {
		t.Fatalf("fallback importer invoked %d time(s), want 1 for unchanged JSONL after failed attempt stamp", got)
	}
}

func TestMaybeAutoImportJSONL_SuccessStampPreventsRepeatedImport(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	orig := fallbackImporter
	var count atomic.Int32
	fallbackImporter = func(_ context.Context, _ storage.DoltStorage, _ string) (*importLocalResult, error) {
		count.Add(1)
		return &importLocalResult{Issues: 1}, nil
	}
	t.Cleanup(func() { fallbackImporter = orig })

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 1 {
		t.Fatalf("fallback importer invoked %d time(s), want 1 for unchanged JSONL after success stamp", got)
	}
}

func TestMaybeAutoImportJSONL_ChangedJSONLBypassesSuccessStamp(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	orig := fallbackImporter
	var count atomic.Int32
	fallbackImporter = func(_ context.Context, _ storage.DoltStorage, _ string) (*importLocalResult, error) {
		count.Add(1)
		return &importLocalResult{Issues: 1}, nil
	}
	t.Cleanup(func() { fallbackImporter = orig })

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)
	if err := os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(`{"_type":"issue","id":"unit-2","title":"changed","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := count.Load(); got != 2 {
		t.Fatalf("fallback importer invoked %d time(s), want 2 after JSONL changed", got)
	}
}

// captureOptsStore is a storage.DoltStorage that records the
// BatchCreateOptions handed to CreateIssuesWithFullOptions. Every other
// method panics (embedded nil interface); the import plumbing under test
// only touches CreateIssuesWithFullOptions, GetConfig and SetConfig.
type captureOptsStore struct {
	storage.DoltStorage // nil — panics on any non-overridden method
	prefix              string
	gotOpts             storage.BatchCreateOptions
}

func (c *captureOptsStore) CreateIssuesWithFullOptions(_ context.Context, _ []*types.Issue, _ string, opts storage.BatchCreateOptions) error {
	c.gotOpts = opts
	return nil
}

func (c *captureOptsStore) GetConfig(_ context.Context, _ string) (string, error) {
	return c.prefix, nil
}

func (c *captureOptsStore) SetConfig(_ context.Context, _, _ string) error {
	return nil
}

// GetIssuesByIDs reports no existing rows so importIssuesCore's stale-import
// filter (filterStaleImportIssues) is a no-op and the import proceeds to
// CreateIssuesWithFullOptions, where the options under test are recorded.
func (c *captureOptsStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return nil, nil
}

// TestImportIssuesCoreThreadsConflictSkip verifies the GH#3955 plumbing:
// ImportOptions.ConflictSkip maps onto storage.BatchCreateOptions.ConflictSkip,
// and the default (explicit `bd import`) path keeps UPSERT semantics.
func TestImportIssuesCoreThreadsConflictSkip(t *testing.T) {
	issues := []*types.Issue{{ID: "unit-1", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}}

	t.Run("ConflictSkip true threads through", func(t *testing.T) {
		s := &captureOptsStore{}
		if _, err := importIssuesCore(context.Background(), "", s, issues, ImportOptions{SkipPrefixValidation: true, ConflictSkip: true}); err != nil {
			t.Fatalf("importIssuesCore: %v", err)
		}
		if !s.gotOpts.ConflictSkip {
			t.Fatalf("ConflictSkip not threaded into BatchCreateOptions: got %+v", s.gotOpts)
		}
		if !s.gotOpts.SkipPrefixValidation {
			t.Errorf("SkipPrefixValidation should still thread through: got %+v", s.gotOpts)
		}
	})

	t.Run("default keeps UPSERT", func(t *testing.T) {
		s := &captureOptsStore{}
		if _, err := importIssuesCore(context.Background(), "", s, issues, ImportOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("importIssuesCore: %v", err)
		}
		if s.gotOpts.ConflictSkip {
			t.Fatalf("explicit-import path must keep UPSERT (ConflictSkip=false); got true")
		}
	})
}

// TestAutoImportFallbackSeamUsesConflictSkip verifies which importer each
// caller is wired to: the auto-import server-mode fallback
// (importFromLocalJSONLConflictSkip, the fallbackImporter seam) requests
// conflict-skip, while importFromLocalJSONLFull — used by `bd bootstrap`
// and `bd init --from-jsonl` — keeps UPSERT. This is the scope boundary
// for GH#3955.
func TestAutoImportFallbackSeamUsesConflictSkip(t *testing.T) {
	dir := t.TempDir()
	writeAutoImportFixtureJSONL(t, dir)
	jsonlPath := filepath.Join(dir, "issues.jsonl")

	t.Run("auto-import fallback uses conflict-skip", func(t *testing.T) {
		s := &captureOptsStore{prefix: "unit"}
		if _, err := importFromLocalJSONLConflictSkip(context.Background(), s, jsonlPath); err != nil {
			t.Fatalf("importFromLocalJSONLConflictSkip: %v", err)
		}
		if !s.gotOpts.ConflictSkip {
			t.Fatalf("auto-import fallback must set ConflictSkip=true; got %+v", s.gotOpts)
		}
	})

	t.Run("explicit recovery path keeps UPSERT", func(t *testing.T) {
		s := &captureOptsStore{prefix: "unit"}
		if _, err := importFromLocalJSONLFull(context.Background(), s, jsonlPath); err != nil {
			t.Fatalf("importFromLocalJSONLFull: %v", err)
		}
		if s.gotOpts.ConflictSkip {
			t.Fatalf("bd bootstrap / init --from-jsonl must keep UPSERT (ConflictSkip=false); got true")
		}
	})
}
