//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// renameLabelPublishFailureStore wraps a real *dolt.DoltStore and makes
// RenameLabel report a publish failure (renamed>0, err!=nil) after the SQL
// side has genuinely committed - reproducing the "SQL committed, Dolt
// publication failed" shape runLabelRename's commandDidWrite ordering must
// survive: the working-set write landed (labels actually changed) but the
// call still returns an error.
type renameLabelPublishFailureStore struct {
	*dolt.DoltStore
}

func (s *renameLabelPublishFailureStore) RenameLabel(ctx context.Context, oldLabel, newLabel, actor string) (renamed, merged int, ids []string, err error) {
	renamed, merged, ids, err = s.DoltStore.RenameLabel(ctx, oldLabel, newLabel, actor)
	if err == nil && renamed > 0 {
		err = errors.New("simulated Dolt publication failure")
	}
	return renamed, merged, ids, err
}

// TestRunLabelRename_CommandDidWriteSetBeforeErrorReturn is the regression
// test for the finding: runLabelRename read renamed>0 into commandDidWrite
// only AFTER the `if err != nil` early return, so a rename whose SQL side
// committed but whose store call also returned an error (a Dolt publication
// failure after the working-set write landed) never marked the command as
// having written - the caller's deferred auto-commit would then skip
// creating a Dolt commit for a write that, on disk, actually happened.
func TestRunLabelRename_CommandDidWriteSetBeforeErrorReturn(t *testing.T) {
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
	spy := &renameLabelPublishFailureStore{DoltStore: inner}
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

	// Seed one issue carrying the label so RenameLabel actually touches
	// something (renamed>0 is the precondition for the defect to bite).
	if _, err := inner.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rlw-1", "Rename publish-failure seed", "body", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := inner.AddLabel(ctx, "rlw-1", "old-label", "tester"); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	commandDidWrite.Store(false)
	usesProxiedServerOverride := usesProxiedServer()
	if usesProxiedServerOverride {
		t.Fatal("test assumes the direct (non-proxied) store.RenameLabel path; proxiedServerMode leaked from another test")
	}

	err := runLabelRename(ctx, []string{"old-label", "new-label"}, false)
	if err == nil {
		t.Fatal("expected the simulated publish failure to surface as an error")
	}

	if !commandDidWrite.Load() {
		t.Error("commandDidWrite must be set once RenameLabel reports renamed>0, even though it also " +
			"returned an error (SQL committed, Dolt publication failed) - it was being read only after " +
			"the early `if err != nil` return, so this write was silently invisible to the caller's " +
			"deferred auto-commit")
	}

	// The rename's SQL side genuinely landed despite the reported error - the
	// premise the whole test rests on.
	labels, err := inner.GetLabels(ctx, "rlw-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "new-label" {
		t.Fatalf("GetLabels = %v, want [new-label] (the rename's SQL side must have landed for this test to mean anything)", labels)
	}
}
