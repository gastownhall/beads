package uow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestIssueOperationsTypedMetadataPatchUpdateWithRealDolt(t *testing.T) {
	ctx := context.Background()
	operations := newMetadataPatchOperations(t, ctx)

	patch := func() issueops.MetadataPatch {
		return issueops.MetadataPatch{
			Merge: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"merged":{"source":"merge"},"overlap":"merged"}`)},
			Set: map[string]json.RawMessage{
				"nested":  json.RawMessage(`{"enabled":true}`),
				"number":  json.RawMessage(`7`),
				"bool":    json.RawMessage(`true`),
				"overlap": json.RawMessage(`"set"`),
			},
			Unset: []string{"overlap", "remove"},
		}
	}

	updatedID := createMetadataPatchIssue(t, ctx, operations, "bd-metadata-update")
	updated, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor: "tester", IssueID: updatedID, Patch: issueops.IssuePatch{Metadata: patch()},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !updated.Changed {
		t.Fatal("Update() Changed = false, want true")
	}
	assertTypedMetadata(t, updated.Issue.Metadata)
}

func TestIssueOperationsMetadataPatchRejectsInvalidInputWithRealDolt(t *testing.T) {
	ctx := context.Background()
	operations := newMetadataPatchOperations(t, ctx)

	cases := []struct {
		name string
		call func(string) error
	}{
		{
			name: "update rejects invalid typed set JSON",
			call: func(id string) error {
				_, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
					Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{"bad": json.RawMessage(`{`)}},
				}})
				return err
			},
		},
		{
			name: "update rejects invalid typed set key",
			call: func(id string) error {
				_, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
					Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{"bad key": json.RawMessage(`true`)}},
				}})
				return err
			},
		},
		{
			name: "update rejects non-object merge",
			call: func(id string) error {
				_, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
					Metadata: issueops.MetadataPatch{Merge: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`[1]`)}},
				}})
				return err
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := createMetadataPatchIssue(t, ctx, operations, fmt.Sprintf("bd-metadata-invalid-%d", i))
			err := tc.call(id)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("operation error = %v, want ErrValidation", err)
			}
			issue := readMetadataPatchIssue(t, ctx, operations, id)
			if issue.Status != issueops.StatusOpen {
				t.Fatalf("invalid operation changed status = %q, want open", issue.Status)
			}
			var metadata map[string]any
			if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
				t.Fatalf("unmarshal unchanged metadata: %v", err)
			}
			if len(metadata) != 4 || metadata["stable"] != true || metadata["keep"] != "yes" || metadata["remove"] != "gone" || metadata["overlap"] != "old" {
				t.Fatalf("invalid operation changed metadata = %#v", metadata)
			}
		})
	}
}

func newMetadataPatchOperations(t *testing.T, ctx context.Context) issueops.Lifecycle {
	t.Helper()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			return "", err
		}
		return "initialize metadata patch fixture", nil
	}); err != nil {
		t.Fatalf("initialize metadata patch fixture: %v", err)
	}
	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	return operations
}

func createMetadataPatchIssue(t *testing.T, ctx context.Context, operations issueops.Lifecycle, id string) string {
	t.Helper()
	created, err := operations.Create(ctx, issueops.CreateRequest{Actor: "tester", Issue: &issueops.Issue{
		ID: id, Title: id, IssueType: types.TypeTask, Priority: 2, Metadata: json.RawMessage(`{"keep":"yes","remove":"gone","overlap":"old","stable":true}`),
	}})
	if err != nil {
		t.Fatalf("Create(%q) error = %v", id, err)
	}
	return created.Issue.ID
}

func readMetadataPatchIssue(t *testing.T, ctx context.Context, operations issueops.Lifecycle, id string) *issueops.Issue {
	t.Helper()
	issue, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: id})
	if err != nil {
		t.Fatalf("read issue %q: %v", id, err)
	}
	return issue.Issue
}

func assertTypedMetadata(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata["keep"] != "yes" || metadata["number"] != float64(7) || metadata["bool"] != true {
		t.Fatalf("typed metadata = %#v", metadata)
	}
	nested, ok := metadata["nested"].(map[string]any)
	if !ok || nested["enabled"] != true {
		t.Fatalf("nested metadata = %#v", metadata["nested"])
	}
	merged, ok := metadata["merged"].(map[string]any)
	if !ok || merged["source"] != "merge" {
		t.Fatalf("merged metadata = %#v", metadata["merged"])
	}
	if _, ok := metadata["overlap"]; ok {
		t.Fatalf("overlap survived unset: %#v", metadata)
	}
	if _, ok := metadata["remove"]; ok {
		t.Fatalf("remove survived unset: %#v", metadata)
	}
}
