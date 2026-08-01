package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type recordingDirectIssueUpdater struct {
	requests []issueops.UpdateRequest
	result   issueops.UpdateResult
	err      error
}

func (u *recordingDirectIssueUpdater) Update(_ context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	u.requests = append(u.requests, request)
	return u.result, u.err
}

func TestRunDirectUpdateMutationBuildsLifecycleRequest(t *testing.T) {
	expectedAssignee := "current-owner"
	expectedStatus := issueops.StatusOpen
	tests := []struct {
		name     string
		mutation directUpdateMutation
		want     issueops.UpdateRequest
	}{
		{
			name: "force without assignee only overrides close policy",
			mutation: directUpdateMutation{
				actor:            "writer",
				issueID:          "bd-1",
				patch:            issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusClosed}},
				claim:            true,
				force:            true,
				expectedAssignee: &expectedAssignee,
				expectedStatus:   &expectedStatus,
			},
			want: issueops.UpdateRequest{
				Actor:            "writer",
				IssueID:          "bd-1",
				Patch:            issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusClosed}},
				Claim:            true,
				ForceClosePolicy: true,
				ExpectedAssignee: &expectedAssignee,
				ExpectedStatus:   &expectedStatus,
			},
		},
		{
			name: "force with assignee overrides both policies",
			mutation: directUpdateMutation{
				actor:   "writer",
				issueID: "bd-2",
				patch: issueops.IssuePatch{
					Status:   issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusClosed},
					Assignee: issueops.Field[string]{Set: true, Value: "next-owner"},
				},
				force: true,
			},
			want: issueops.UpdateRequest{
				Actor:                 "writer",
				IssueID:               "bd-2",
				Patch:                 issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusClosed}, Assignee: issueops.Field[string]{Set: true, Value: "next-owner"}},
				ForceAssigneeTransfer: true,
				ForceClosePolicy:      true,
			},
		},
		{
			name: "unforced update preserves guards without overrides",
			mutation: directUpdateMutation{
				actor:            "writer",
				issueID:          "bd-3",
				patch:            issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true, Value: "next-owner"}},
				expectedAssignee: &expectedAssignee,
				expectedStatus:   &expectedStatus,
			},
			want: issueops.UpdateRequest{
				Actor:            "writer",
				IssueID:          "bd-3",
				Patch:            issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true, Value: "next-owner"}},
				ExpectedAssignee: &expectedAssignee,
				ExpectedStatus:   &expectedStatus,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updater := &recordingDirectIssueUpdater{}
			if _, err := runDirectUpdateMutation(context.Background(), updater, tt.mutation); err != nil {
				t.Fatalf("runDirectUpdateMutation: %v", err)
			}
			if len(updater.requests) != 1 {
				t.Fatalf("Update calls = %d, want 1", len(updater.requests))
			}
			if !reflect.DeepEqual(updater.requests[0], tt.want) {
				t.Errorf("Update request = %#v, want %#v", updater.requests[0], tt.want)
			}
		})
	}
}

func TestRunDirectUpdateMutationPropagatesErrorUnchanged(t *testing.T) {
	wantErr := errors.New("update failed")
	wantIssue := &issueops.Issue{ID: "bd-result"}
	updater := &recordingDirectIssueUpdater{
		result: issueops.UpdateResult{Issue: wantIssue, Changed: true},
		err:    wantErr,
	}

	result, err := runDirectUpdateMutation(context.Background(), updater, directUpdateMutation{})
	if result.Issue != wantIssue || !result.Changed {
		t.Errorf("result = %#v, want issue %p and Changed true", result, wantIssue)
	}
	if err != wantErr {
		t.Fatalf("error = %v, want identity with %v", err, wantErr)
	}
	if len(updater.requests) != 1 {
		t.Errorf("Update calls = %d, want 1", len(updater.requests))
	}
}
