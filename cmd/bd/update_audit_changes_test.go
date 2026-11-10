package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestUpdateAuditChanges covers auditUpdateFieldChanges' row selection
// (GH#6146): explicit patch fields keep their precedence, a claim's implied
// status/assignee move journals from the advisory pre-read and the returned
// post-state, priority stays explicit-only, equal values are suppressed, and
// a missing snapshot or an unchanged update journals nothing. These guard
// the helper; the embedded and proxied suites carry the CLI wiring
// regressions.
func TestUpdateAuditChanges(t *testing.T) {
	auditCase := func(t *testing.T, run func()) (rows []claimAuditRow) {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// beads.FindBeadsDir() validates that the directory holds project
		// files; metadata.json satisfies it, as internal/audit's own tests do.
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		t.Setenv("BEADS_DIR", beadsDir)
		t.Setenv("BD_AUDIT_ENABLED", "1")

		run()

		data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("read interactions.jsonl: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var e claimAuditRow
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("bad journal line %q: %v", line, err)
			}
			rows = append(rows, e)
		}
		return rows
	}

	openIssue := func() *types.Issue {
		return &types.Issue{ID: "bd-aa1", Status: types.StatusOpen, Assignee: "", Priority: 2}
	}

	cases := []struct {
		name    string
		issueID string
		before  *types.Issue
		after   *types.Issue
		patch   issueops.IssuePatch
		claim   bool
		changed bool
		want    []claimAuditRow
	}{
		{
			name:    "explicit_fields_only",
			issueID: "bd-aa1",
			before:  openIssue(),
			patch: issueops.IssuePatch{
				Status:   issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusInProgress},
				Assignee: issueops.Field[string]{Set: true, Value: "bob"},
				Priority: issueops.Field[int]{Set: true, Value: 1},
			},
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "in_progress"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "bob"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "priority", OldValue: "2", NewValue: "1"},
			},
		},
		{
			name:    "claim_journals_implied_move",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "alice", Priority: 2},
			claim:   true,
			changed: true,
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "in_progress"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "alice"},
			},
		},
		{
			name:    "claim_with_explicit_status_journals_once",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusBlocked, Assignee: "alice", Priority: 2},
			patch:   issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusBlocked}},
			claim:   true,
			changed: true,
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "blocked"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "alice"},
			},
		},
		{
			name:    "claim_with_explicit_assignee_takes_explicit_value",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "bob", Priority: 2},
			patch:   issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true, Value: "bob"}},
			claim:   true,
			changed: true,
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "in_progress"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "bob"},
			},
		},
		{
			name:    "claim_with_explicit_priority_journals_priority_too",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "alice", Priority: 1},
			patch:   issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: 1}},
			claim:   true,
			changed: true,
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "in_progress"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "alice"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "priority", OldValue: "2", NewValue: "1"},
			},
		},
		{
			name:    "equal_values_are_suppressed",
			issueID: "bd-aa1",
			before:  openIssue(),
			patch:   issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: types.StatusOpen}},
			want:    nil,
		},
		{
			name:    "claim_uses_pre_read_and_returned_post_state",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "alice", Priority: 2},
			claim:   true,
			changed: true,
			// The pre-read is one transaction earlier than the write, so a
			// concurrent writer's move lands as the journaled old value: the
			// helper records what it saw, not a re-read.
			want: []claimAuditRow{
				{Kind: "field_change", IssueID: "bd-aa1", Field: "status", OldValue: "open", NewValue: "in_progress"},
				{Kind: "field_change", IssueID: "bd-aa1", Field: "assignee", OldValue: "", NewValue: "alice"},
			},
		},
		{
			name:    "unchanged_update_journals_no_claim_rows",
			issueID: "bd-aa1",
			before:  openIssue(),
			after:   &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "alice", Priority: 2},
			claim:   true,
			changed: false,
			want:    nil,
		},
		{
			name:    "claim_without_post_state_journals_nothing",
			issueID: "bd-aa1",
			before:  openIssue(),
			claim:   true,
			changed: true,
			want:    nil,
		},
		{
			name:   "nil_pre_read_journals_nothing",
			before: nil,
			after:  &types.Issue{ID: "bd-aa1", Status: types.StatusInProgress, Assignee: "alice", Priority: 2},
			claim:  true,
			changed: true,
			want:   nil,
		},
		{
			name:    "empty_issue_id_journals_nothing",
			issueID: "",
			before:  openIssue(),
			after:   &types.Issue{Status: types.StatusInProgress, Assignee: "alice", Priority: 2},
			claim:   true,
			changed: true,
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after, patch, claim, changed := tc.before, tc.after, tc.patch, tc.claim, tc.changed
			got := auditCase(t, func() {
				auditUpdateFieldChanges(tc.issueID, before, after, patch, claim, changed, "alice")
			})
			for i := range got {
				if got[i].Actor != "alice" {
					t.Errorf("row %d has actor %q, want alice", i, got[i].Actor)
				}
				got[i].Actor = "" // shared by every row; asserted above
			}
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// claimAuditRow is the flattened journal row TestUpdateAuditChanges
// compares.
type claimAuditRow struct {
	Kind     string
	IssueID  string
	Actor    string
	Field    string
	OldValue string
	NewValue string
}

func (r *claimAuditRow) UnmarshalJSON(data []byte) error {
	var e struct {
		Kind    string         `json:"kind"`
		IssueID string         `json:"issue_id"`
		Actor   string         `json:"actor"`
		Extra   map[string]any `json:"extra"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	r.Kind, r.IssueID, r.Actor = e.Kind, e.IssueID, e.Actor
	if e.Extra != nil {
		r.Field, _ = e.Extra["field"].(string)
		r.OldValue, _ = e.Extra["old_value"].(string)
		r.NewValue, _ = e.Extra["new_value"].(string)
	}
	return nil
}
