//go:build cgo

package embeddeddolt_test

import (
	"testing"
)

// EmbeddedDoltStore.GetMergeBlockers had ZERO coverage (wy-wrq9o F5). It is
// the embedded engine's half of the wy-36ilm F12 surface — the schema
// conflicts and constraint violations `bd conflicts` cannot show as rows —
// and the CLI reaches it through storage.UnwrapStore exactly as it reaches
// the server-mode one. The reads run against the embedded engine's own
// dolt_merge_status / dolt_schema_conflicts / dolt_constraint_violations, so
// a system table or column the embedded engine names differently is caught
// here. Missing optional system tables are intentionally tolerated and appear
// as empty blocker classes.

// TestEmbeddedGetMergeBlockersOnAQuietStore is the baseline every other
// caller depends on: with no merge open, GetMergeBlockers must report an
// unblocked, non-merging state and — critically — no ERROR. Every read is
// wrapped in missing-system-table tolerance precisely so this diagnosis
// helper can never be the thing that fails `bd conflicts list`.
func TestEmbeddedGetMergeBlockersOnAQuietStore(t *testing.T) {
	te := newTestEnv(t, "emb")
	ctx := t.Context()

	blockers, err := te.store.GetMergeBlockers(ctx)
	if err != nil {
		t.Fatalf("GetMergeBlockers on a quiet store must not error: %v", err)
	}
	if blockers.Merging {
		t.Error("no merge is open, but GetMergeBlockers reported one in progress")
	}
	if blockers.Blocked() {
		t.Errorf("a quiet store must have nothing blocking a merge, got %+v", blockers)
	}
	if len(blockers.SchemaConflictTables) != 0 || len(blockers.ConstraintViolations) != 0 {
		t.Errorf("unexpected blockers on a quiet store: %+v", blockers)
	}
}
