package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
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

func TestBuildUpdatePatchMapsSupportedFields(t *testing.T) {
	estimate := 0
	externalRef := "ext-123"
	dueAt := time.Date(2026, time.August, 3, 4, 5, 6, 0, time.UTC)
	deferUntil := time.Date(2026, time.August, 4, 5, 6, 7, 0, time.UTC)

	tests := []struct {
		name    string
		updates map[string]any
		want    issueops.IssuePatch
	}{
		{
			name:    "title",
			updates: map[string]any{"title": "new title"},
			want:    issueops.IssuePatch{Title: setField("new title")},
		},
		{
			name:    "description",
			updates: map[string]any{"description": "new description"},
			want:    issueops.IssuePatch{Description: setField("new description")},
		},
		{
			name:    "design",
			updates: map[string]any{"design": "new design"},
			want:    issueops.IssuePatch{Design: setField("new design")},
		},
		{
			name:    "acceptance criteria",
			updates: map[string]any{"acceptance_criteria": "new criteria"},
			want:    issueops.IssuePatch{AcceptanceCriteria: setField("new criteria")},
		},
		{
			name:    "notes",
			updates: map[string]any{"notes": "replacement notes"},
			want:    issueops.IssuePatch{Notes: setField("replacement notes")},
		},
		{
			name:    "append notes",
			updates: map[string]any{storageissueops.OpAppendNotes: "additional notes"},
			want:    issueops.IssuePatch{AppendNotes: setField("additional notes")},
		},
		{
			name:    "spec ID",
			updates: map[string]any{"spec_id": "spec-123"},
			want:    issueops.IssuePatch{SpecID: setField("spec-123")},
		},
		{
			name:    "await ID",
			updates: map[string]any{"await_id": "await-123"},
			want:    issueops.IssuePatch{AwaitID: setField("await-123")},
		},
		{
			name:    "closed by session",
			updates: map[string]any{"closed_by_session": "session-123"},
			want:    issueops.IssuePatch{ClosedBySession: setField("session-123")},
		},
		{
			name:    "assignee",
			updates: map[string]any{"assignee": "alice"},
			want:    issueops.IssuePatch{Assignee: setField("alice")},
		},
		{
			name:    "parent clear",
			updates: map[string]any{"parent": ""},
			want:    issueops.IssuePatch{ParentID: setField("")},
		},
		{
			name:    "status",
			updates: map[string]any{"status": "blocked"},
			want:    issueops.IssuePatch{Status: setField(issueops.StatusBlocked)},
		},
		{
			name:    "issue type",
			updates: map[string]any{"issue_type": "custom"},
			want:    issueops.IssuePatch{IssueType: setField(issueops.IssueType("custom"))},
		},
		{
			name:    "priority zero",
			updates: map[string]any{"priority": 0},
			want:    issueops.IssuePatch{Priority: setField(0)},
		},
		{
			name:    "estimated minutes zero",
			updates: map[string]any{"estimated_minutes": 0},
			want:    issueops.IssuePatch{EstimatedMinutes: setField(&estimate)},
		},
		{
			name:    "external reference",
			updates: map[string]any{"external_ref": externalRef},
			want:    issueops.IssuePatch{ExternalRef: setField(&externalRef)},
		},
		{
			name:    "external reference clear",
			updates: map[string]any{"external_ref": nil},
			want:    issueops.IssuePatch{ExternalRef: setField[*string](nil)},
		},
		{
			name:    "due at",
			updates: map[string]any{"due_at": dueAt},
			want:    issueops.IssuePatch{DueAt: setField(&dueAt)},
		},
		{
			name:    "due at clear",
			updates: map[string]any{"due_at": nil},
			want:    issueops.IssuePatch{DueAt: setField[*time.Time](nil)},
		},
		{
			name:    "defer until",
			updates: map[string]any{"defer_until": deferUntil},
			want:    issueops.IssuePatch{DeferUntil: setField(&deferUntil)},
		},
		{
			name:    "defer until clear",
			updates: map[string]any{"defer_until": nil},
			want:    issueops.IssuePatch{DeferUntil: setField[*time.Time](nil)},
		},
		{
			name:    "add labels",
			updates: map[string]any{"add_labels": []string{"one", "two"}},
			want:    issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{"one", "two"}}},
		},
		{
			name:    "remove labels",
			updates: map[string]any{"remove_labels": []string{"one", "two"}},
			want:    issueops.IssuePatch{Labels: issueops.LabelPatch{Remove: []string{"one", "two"}}},
		},
		{
			name:    "set labels",
			updates: map[string]any{"set_labels": []string{}},
			want:    issueops.IssuePatch{Labels: issueops.LabelPatch{Replace: setField([]string{})}},
		},
		{
			name:    "merge metadata",
			updates: map[string]any{storageissueops.OpMergeMetadata: json.RawMessage(`{"one":1}`)},
			want: issueops.IssuePatch{Metadata: issueops.MetadataPatch{
				Merge: setField(json.RawMessage(`{"one":1}`)),
			}},
		},
		{
			name:    "set metadata",
			updates: map[string]any{storageissueops.OpSetMetadata: []string{"one=value", "two=42"}},
			want: issueops.IssuePatch{Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{
				"one": json.RawMessage(`"value"`),
				"two": json.RawMessage(`"42"`),
			}}},
		},
		{
			name:    "unset metadata",
			updates: map[string]any{storageissueops.OpUnsetMetadata: []string{"one", "two"}},
			want:    issueops.IssuePatch{Metadata: issueops.MetadataPatch{Unset: []string{"one", "two"}}},
		},
		{
			name:    "wisp enabled",
			updates: map[string]any{"wisp": true},
			want:    issueops.IssuePatch{Persistence: setField(issueops.PersistenceModeEphemeral)},
		},
		{
			name:    "wisp disabled",
			updates: map[string]any{"wisp": false},
			want:    issueops.IssuePatch{Persistence: setField(issueops.PersistenceModePersistent)},
		},
		{
			name:    "history disabled",
			updates: map[string]any{"no_history": true},
			want:    issueops.IssuePatch{Persistence: setField(issueops.PersistenceModeNoHistory)},
		},
		{
			name:    "history enabled",
			updates: map[string]any{"no_history": false},
			want:    issueops.IssuePatch{Persistence: setField(issueops.PersistenceModePersistent)},
		},
		{
			name:    "wisp takes precedence over history",
			updates: map[string]any{"wisp": true, "no_history": true},
			want:    issueops.IssuePatch{Persistence: setField(issueops.PersistenceModeEphemeral)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildUpdatePatch(tt.updates)
			if err != nil {
				t.Fatalf("buildUpdatePatch: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildUpdatePatch() = %#v, want %#v", got, tt.want)
			}
		})
	}

	errorTests := []struct {
		name    string
		updates map[string]any
		wantErr string
	}{
		{
			name:    "unsupported field",
			updates: map[string]any{"unknown": "value"},
			wantErr: `unsupported update field "unknown"`,
		},
		{
			name:    "unsupported value type",
			updates: map[string]any{"priority": "zero"},
			wantErr: `unsupported value string for update field "priority"`,
		},
		{
			name:    "malformed set metadata",
			updates: map[string]any{storageissueops.OpSetMetadata: []string{"missing-separator"}},
			wantErr: `invalid --set-metadata: expected key=value, got "missing-separator"`,
		},
	}

	for _, tt := range errorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildUpdatePatch(tt.updates)
			if !reflect.DeepEqual(got, issueops.IssuePatch{}) {
				t.Errorf("buildUpdatePatch() patch = %#v, want empty patch", got)
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("buildUpdatePatch() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestReplacesExistingNotes(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		fields   map[string]any
		want     bool
	}{
		{
			name:     "overwrite",
			existing: "old notes",
			fields:   map[string]any{"notes": "new notes"},
			want:     true,
		},
		{
			name:     "empty existing notes",
			existing: "",
			fields:   map[string]any{"notes": "new notes"},
			want:     false,
		},
		{
			name:     "append only",
			existing: "old notes",
			fields:   map[string]any{storageissueops.OpAppendNotes: "new notes"},
			want:     false,
		},
		{
			name:     "same value",
			existing: "unchanged notes",
			fields:   map[string]any{"notes": "unchanged notes"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replacesExistingNotes(tt.existing, tt.fields); got != tt.want {
				t.Errorf("replacesExistingNotes(%q, %#v) = %t, want %t", tt.existing, tt.fields, got, tt.want)
			}
		})
	}
}
