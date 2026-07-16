package dolt

import (
	"context"
	"testing"
)

// TestWholeRowWritesBumpRevisionOnDolt is the runtime companion to the
// source-scan guard (issueops.TestAllIssueRowWritesBumpRevision): it drives each
// public DoltStore write op against a real Dolt server and asserts every one
// stamps a fresh, non-zero revision distinct from the prior value.
func TestWholeRowWritesBumpRevisionOnDolt(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	rev := func(id string) int64 {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return iss.Revision
	}

	// CreateIssue must stamp a real (non-zero) token, not leave the DEFAULT 0
	// sentinel — a freshly created bead is immediately CAS-able.
	makeCASIssue(t, ctx, store, "rev-op")
	r0 := rev("rev-op")
	if r0 == 0 {
		t.Fatal("CreateIssue left revision = 0; a created row must carry a real token")
	}

	steps := []struct {
		name string
		do   func()
	}{
		{"UpdateIssue", func() {
			if err := store.UpdateIssue(ctx, "rev-op", map[string]interface{}{"title": "changed"}, "tester"); err != nil {
				t.Fatalf("UpdateIssue: %v", err)
			}
		}},
		{"ClaimIssue", func() {
			if err := store.ClaimIssue(ctx, "rev-op", "worker"); err != nil {
				t.Fatalf("ClaimIssue: %v", err)
			}
		}},
		{"CloseIssue", func() {
			if err := store.CloseIssue(ctx, "rev-op", "done", "tester", "sess"); err != nil {
				t.Fatalf("CloseIssue: %v", err)
			}
		}},
		{"ReopenIssue", func() {
			if err := store.ReopenIssue(ctx, "rev-op", "reopen", "tester"); err != nil {
				t.Fatalf("ReopenIssue: %v", err)
			}
		}},
		{"CompareAndSetMetadataKey", func() {
			if _, err := store.CompareAndSetMetadataKey(ctx, "rev-op", "gc.probe", nil, "v", "tester"); err != nil {
				t.Fatalf("CompareAndSetMetadataKey: %v", err)
			}
		}},
	}

	prev := r0
	for _, s := range steps {
		s.do()
		cur := rev("rev-op")
		if cur == 0 {
			t.Errorf("%s: revision is 0 after write", s.name)
		}
		if cur == prev {
			t.Errorf("%s: revision unchanged (%d) — write did not stamp a fresh nonce", s.name, cur)
		}
		prev = cur
	}
}
