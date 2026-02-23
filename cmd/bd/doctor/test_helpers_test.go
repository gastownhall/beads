//go:build cgo

package doctor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// newTestDoltStore creates a DoltStore for testing in the doctor package.
// Each test gets an isolated database to prevent cross-test pollution.
func newTestDoltStore(t *testing.T, prefix string) *dolt.DoltStore {
	t.Helper()
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Skipf("skipping: Dolt not available: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	// Configure Gas Town custom types for compatibility
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		store.Close()
		t.Fatalf("Failed to set types.custom: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newTestIssue creates a minimal test issue with the given ID.
func newTestIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "Test issue " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
}

// insertIssueDirectly inserts an issue with a pre-set ID into the dolt store.
// This simulates cross-rig contamination where foreign-prefix issues end up in the store.
func insertIssueDirectly(t *testing.T, store *dolt.DoltStore, id string) {
	t.Helper()
	ctx := context.Background()
	issue := newTestIssue(id)
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to insert issue %s: %v", id, err)
	}
}

// ptrTime returns a pointer to a time.Time value.
func ptrTime(t time.Time) *time.Time {
	return &t
}
