package issueops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// clauseValues pairs the SET clauses ManageClosedAt appended with their args so
// a test can assert on the column a clause writes rather than on ordering.
func clauseValues(t *testing.T, clauses []string, args []interface{}) map[string]interface{} {
	t.Helper()
	if len(clauses) != len(args) {
		t.Fatalf("clauses (%d) and args (%d) are out of step: %v / %v", len(clauses), len(args), clauses, args)
	}
	out := make(map[string]interface{}, len(clauses))
	for i, clause := range clauses {
		column := strings.TrimSpace(strings.TrimSuffix(clause, "= ?"))
		column = strings.Trim(column, "` ")
		if _, dup := out[column]; dup {
			t.Fatalf("column %q written twice by %v", column, clauses)
		}
		out[column] = args[i]
	}
	return out
}

// TestManageClosedAtMatchesCloseSemantics pins the close-crossing defaults
// (ga-kjkv1): closeIssueInTx always writes close_reason and closed_by_session,
// including the empty values a caller supplying neither produces, so a generic
// update that crosses into closed must write them too. Without this a re-close
// after a generic reopen keeps the PREVIOUS close's session and `bd show`
// misattributes it. Each default is suppressed by its own explicit key.
func TestManageClosedAtMatchesCloseSemantics(t *testing.T) {
	openIssue := &types.Issue{ID: "bd-1", Status: types.StatusOpen}
	closedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closedIssue := &types.Issue{ID: "bd-1", Status: types.StatusClosed, ClosedAt: &closedAt}

	tests := []struct {
		name     string
		oldIssue *types.Issue
		updates  map[string]interface{}
		want     map[string]interface{}
		// wantClosedAtNow marks the closed_at default as a fresh timestamp the
		// test cannot predict; want must omit the key when this is set.
		wantClosedAtNow bool
	}{
		{
			name:            "crossing into closed defaults reason and session to empty",
			oldIssue:        openIssue,
			updates:         map[string]interface{}{"status": "closed"},
			want:            map[string]interface{}{"close_reason": "", "closed_by_session": ""},
			wantClosedAtNow: true,
		},
		{
			name:            "typed status crosses the same way",
			oldIssue:        openIssue,
			updates:         map[string]interface{}{"status": types.StatusClosed},
			want:            map[string]interface{}{"close_reason": "", "closed_by_session": ""},
			wantClosedAtNow: true,
		},
		{
			name:            "explicit close_reason wins over its default",
			oldIssue:        openIssue,
			updates:         map[string]interface{}{"status": "closed", "close_reason": "shipped"},
			want:            map[string]interface{}{"closed_by_session": ""},
			wantClosedAtNow: true,
		},
		{
			name:            "explicit closed_by_session wins over its default",
			oldIssue:        openIssue,
			updates:         map[string]interface{}{"status": "closed", "closed_by_session": "sess-9"},
			want:            map[string]interface{}{"close_reason": ""},
			wantClosedAtNow: true,
		},
		{
			name:     "explicit closed_at suppresses only the closed_at default",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": "closed", "closed_at": closedAt},
			want:     map[string]interface{}{"close_reason": "", "closed_by_session": ""},
		},
		{
			name:     "reopen clears closed_at, reason and session",
			oldIssue: closedIssue,
			updates:  map[string]interface{}{"status": "open"},
			want:     map[string]interface{}{"closed_at": nil, "close_reason": "", "closed_by_session": ""},
		},
		{
			name:     "no status update writes nothing",
			oldIssue: closedIssue,
			updates:  map[string]interface{}{"priority": 1},
			want:     map[string]interface{}{},
		},
		{
			name:     "non-closed transition on an open row writes nothing",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": "in_progress"},
			want:     map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().UTC()
			clauses, args := ManageClosedAt(tt.oldIssue, tt.updates, nil, nil)
			got := clauseValues(t, clauses, args)

			if tt.wantClosedAtNow {
				stamp, ok := got["closed_at"].(time.Time)
				if !ok {
					t.Fatalf("closed_at = %#v, want a fresh time.Time", got["closed_at"])
				}
				if stamp.Before(before) {
					t.Errorf("closed_at = %v, want at or after %v", stamp, before)
				}
				delete(got, "closed_at")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("columns = %#v, want %#v", got, tt.want)
			}
			for column, want := range tt.want {
				if got[column] != want {
					t.Errorf("%s = %#v, want %#v", column, got[column], want)
				}
			}
		})
	}
}

// TestValidateClosedAtCoherence pins ruling 2's matrix (ga-kjkv1): an explicit
// closed_at write is allowed only when it leaves the row satisfying the
// closed-iff-closed_at invariant types.Issue.Validate enforces. The repair path
// — stamping closed_at on a row that is or becomes closed — stays open.
func TestValidateClosedAtCoherence(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	openIssue := &types.Issue{ID: "bd-open", Status: types.StatusOpen}
	closedIssue := &types.Issue{ID: "bd-closed", Status: types.StatusClosed, ClosedAt: &stamp}

	tests := []struct {
		name       string
		oldIssue   *types.Issue
		updates    map[string]interface{}
		wantRefuse bool
	}{
		{
			name:     "closed row repairs its closed_at",
			oldIssue: closedIssue,
			updates:  map[string]interface{}{"closed_at": stamp},
		},
		{
			name:     "row becoming closed carries its closed_at",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": "closed", "closed_at": stamp},
		},
		{
			name:     "row becoming closed via typed status carries its closed_at",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": types.StatusClosed, "closed_at": &stamp},
		},
		{
			name:     "reopen clears closed_at",
			oldIssue: closedIssue,
			updates:  map[string]interface{}{"status": "open", "closed_at": nil},
		},
		{
			name:     "open row clears closed_at",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"closed_at": nil},
		},
		{
			name:       "open row cannot stamp closed_at",
			oldIssue:   openIssue,
			updates:    map[string]interface{}{"closed_at": stamp},
			wantRefuse: true,
		},
		{
			name:       "non-closed transition cannot stamp closed_at",
			oldIssue:   openIssue,
			updates:    map[string]interface{}{"status": "in_progress", "closed_at": stamp},
			wantRefuse: true,
		},
		{
			name:       "closed row cannot clear closed_at",
			oldIssue:   closedIssue,
			updates:    map[string]interface{}{"closed_at": nil},
			wantRefuse: true,
		},
		{
			name:       "closed row cannot clear closed_at through a nil pointer",
			oldIssue:   closedIssue,
			updates:    map[string]interface{}{"closed_at": (*time.Time)(nil)},
			wantRefuse: true,
		},
		{
			name:       "restating closed cannot clear closed_at",
			oldIssue:   closedIssue,
			updates:    map[string]interface{}{"status": "closed", "closed_at": nil},
			wantRefuse: true,
		},
		{
			name:     "no closed_at key is never refused",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": "closed"},
		},
		{
			name:     "a mis-typed status defers to the crossing check",
			oldIssue: openIssue,
			updates:  map[string]interface{}{"status": 7, "closed_at": stamp},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClosedAtCoherence(tt.oldIssue, tt.updates)
			if !tt.wantRefuse {
				if err != nil {
					t.Fatalf("ValidateClosedAtCoherence = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateClosedAtCoherence = nil, want a refusal")
			}
			if !errors.Is(err, storage.ErrValidation) {
				t.Errorf("refusal %v is not storage.ErrValidation", err)
			}
			// The refusal must name the column and the issue, so a raw-map
			// caller can tell WHICH write was rejected and why.
			for _, want := range []string{"closed_at", tt.oldIssue.ID} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}
