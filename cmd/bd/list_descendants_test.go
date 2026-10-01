package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The embedded cost is one engine open per storage call, not per SQL statement.
// This double allows ID resolution but rejects the old per-node parent searches.
type descendantRecordingStore struct {
	storage.DoltStorage
	parent          *types.Issue
	descendants     []*types.Issue
	descendantErr   error
	descendantCalls int
	rootID          string
	filter          types.IssueFilter
}

func (s *descendantRecordingStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if filter.ParentID != nil {
		return nil, errors.New("per-node descendant search must not run")
	}
	return []*types.Issue{s.parent}, nil
}

func (s *descendantRecordingStore) GetIssue(context.Context, string) (*types.Issue, error) {
	return s.parent, nil
}

func (s *descendantRecordingStore) GetDescendants(_ context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.descendantCalls++
	s.rootID, s.filter = rootID, filter
	return s.descendants, s.descendantErr
}

func TestGetHierarchicalChildrenSingleDescendantCall(t *testing.T) {
	status := types.StatusOpen
	parentID := "test-root"
	filter := types.IssueFilter{Status: &status, ParentID: &parentID, Limit: 1, MaxRows: 5, SkipWisps: true}
	failure := errors.New("descendant query failed")
	capFailure := &issueops.ErrTooManyRows{Found: 6, Cap: 5, Source: "--max-rows"}
	for _, tc := range []struct {
		name     string
		children []*types.Issue
		err      error
		wantIDs  []string
	}{
		{"subtree", []*types.Issue{{ID: "test-child"}, {ID: "test-grandchild"}}, nil, []string{"test-root", "test-child", "test-grandchild"}},
		{"childless", nil, nil, nil},
		{"query_error", nil, failure, nil},
		{"cap_error", nil, capFailure, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &descendantRecordingStore{parent: &types.Issue{ID: parentID}, descendants: tc.children, descendantErr: tc.err}
			got, err := getHierarchicalChildren(t.Context(), s, "", parentID, filter)
			if tc.err != nil {
				if err == nil || err.Error() != "error finding descendants: "+tc.err.Error() {
					t.Fatalf("error = %v, want existing descendant error message", err)
				}
				// handleMaxRowsError needs the typed error for exit code 2.
				if !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want it to wrap %v", err, tc.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if s.descendantCalls != 1 || s.rootID != parentID || !reflect.DeepEqual(s.filter, filter) {
				t.Fatalf("descendant calls=%d root=%q filter=%+v, want one call for %q with unchanged filter", s.descendantCalls, s.rootID, s.filter, parentID)
			}
			var ids []string
			for _, issue := range got {
				ids = append(ids, issue.ID)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("tree IDs = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}
