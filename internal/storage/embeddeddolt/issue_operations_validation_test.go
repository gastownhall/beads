//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedIssueOperationsRejectsInvalidUpdatesWithoutMutation pins the
// shared update validation on this backend's own lifecycle, so a request the
// unit-of-work backend refuses is refused here too — and refused before any
// field in the same patch reaches the row.
func TestEmbeddedIssueOperationsRejectsInvalidUpdatesWithoutMutation(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_validation")
	ctx := t.Context()
	operations, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	issue := &types.Issue{ID: "ops_validation-1", Title: "original", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"keep":"yes"}`)}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mutation := publicops.Field[string]{Set: true, Value: "mutated"}
	for _, tc := range []struct {
		name  string
		patch publicops.IssuePatch
	}{
		{"priority above range", publicops.IssuePatch{Title: mutation, Priority: publicops.Field[int]{Set: true, Value: 9}}},
		{"metadata set key with unsafe characters", publicops.IssuePatch{Title: mutation, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"bad-key": json.RawMessage(`"value"`)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: tc.patch}); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("Update error = %v, want ErrValidation", err)
			}
			stored, err := te.store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if stored.Title != "original" || stored.Priority != 2 {
				t.Fatalf("rejected update mutated the row: title %q, priority %d", stored.Title, stored.Priority)
			}
			metadata := map[string]json.RawMessage{}
			if len(stored.Metadata) > 0 {
				if err := json.Unmarshal(stored.Metadata, &metadata); err != nil {
					t.Fatalf("unmarshal stored metadata %s: %v", stored.Metadata, err)
				}
			}
			if _, ok := metadata["bad-key"]; ok {
				t.Fatalf("rejected update wrote metadata: %s", stored.Metadata)
			}
		})
	}
}
