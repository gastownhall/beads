//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestRenameAcceptsSameNamespaceTargetForDigitEndingPrefix reproduces GH#6068:
// bd init accepts a prefix ending in a digit ("probe8") and mints IDs with it,
// but bd rename rejects a same-namespace target such as "probe8-abc".
func TestRenameAcceptsSameNamespaceTargetForDigitEndingPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	s := newTestStoreWithPrefix(t, filepath.Join(tmpDir, "dolt"), "probe8")

	store = s
	actor = "test"
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		actor = ""
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()
	rootCtx = ctx

	issue := &types.Issue{
		ID:        "probe8-4b0",
		Title:     "throwaway",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	if err := runRename(renameCmd, []string{"probe8-4b0", "probe8-abc"}); err != nil {
		t.Fatalf("bd rename probe8-4b0 probe8-abc: %v (same-namespace rename rejected for a digit-ending prefix)", err)
	}

	got, err := s.GetIssue(ctx, "probe8-abc")
	if err != nil || got == nil {
		t.Fatalf("renamed issue probe8-abc not found: %v", err)
	}
}
