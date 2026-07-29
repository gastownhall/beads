package validation

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestExists(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name:    "nil issue returns error",
			issue:   nil,
			wantErr: true,
		},
		{
			name:    "non-nil issue passes",
			issue:   &types.Issue{ID: "bd-test"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Exists()("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("Exists() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotTemplate(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name:    "nil issue passes (delegated check)",
			issue:   nil,
			wantErr: false,
		},
		{
			name:    "non-template passes",
			issue:   &types.Issue{ID: "bd-test", IsTemplate: false},
			wantErr: false,
		},
		{
			name:    "template returns error",
			issue:   &types.Issue{ID: "bd-test", IsTemplate: true},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotTemplate()("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("NotTemplate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotPinned(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		force   bool
		wantErr bool
	}{
		{
			name:    "nil issue passes",
			issue:   nil,
			force:   false,
			wantErr: false,
		},
		{
			name:    "open issue passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusOpen},
			force:   false,
			wantErr: false,
		},
		{
			name:    "pinned issue without force fails",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusPinned},
			force:   false,
			wantErr: true,
		},
		{
			name:    "pinned issue with force passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusPinned},
			force:   true,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotPinned(tt.force)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("NotPinned() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAssigneeMatches(t *testing.T) {
	tests := []struct {
		name        string
		issue       *types.Issue
		actor       string
		force       bool
		wantErr     bool
		wantErrFrag string
	}{
		{
			name:    "nil issue passes (delegated check)",
			issue:   nil,
			actor:   "alice",
			wantErr: false,
		},
		{
			name:    "unassigned issue passes for any actor",
			issue:   &types.Issue{ID: "bd-test", Assignee: ""},
			actor:   "alice",
			wantErr: false,
		},
		{
			name:    "matching assignee passes",
			issue:   &types.Issue{ID: "bd-test", Assignee: "alice"},
			actor:   "alice",
			wantErr: false,
		},
		{
			name:        "mismatched assignee without force fails",
			issue:       &types.Issue{ID: "bd-test", Assignee: "bob"},
			actor:       "alice",
			force:       false,
			wantErr:     true,
			wantErrFrag: "assignee is",
		},
		{
			name:    "mismatched assignee with force passes",
			issue:   &types.Issue{ID: "bd-test", Assignee: "bob"},
			actor:   "alice",
			force:   true,
			wantErr: false,
		},
		{
			name:        "empty actor with assigned bead fails",
			issue:       &types.Issue{ID: "bd-test", Assignee: "bob"},
			actor:       "",
			wantErr:     true,
			wantErrFrag: "assignee is",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssigneeMatches(tt.actor, tt.force)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("AssigneeMatches() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErrFrag != "" && err != nil {
				if !strings.Contains(err.Error(), tt.wantErrFrag) {
					t.Errorf("AssigneeMatches() error %q should contain %q", err.Error(), tt.wantErrFrag)
				}
			}
		})
	}
}

func TestAssigneeNotStolen(t *testing.T) {
	inProgress := func(assignee string) *types.Issue {
		return &types.Issue{ID: "bd-test", Assignee: assignee, Status: types.StatusInProgress}
	}
	poolsCalled := false
	pools := func() []string {
		poolsCalled = true
		return []string{"fable-crew", "night-crew"}
	}

	tests := []struct {
		name        string
		issue       *types.Issue
		actor       string
		newAssignee string
		pools       func() []string
		force       bool
		wantErr     bool
	}{
		{
			name:    "nil issue passes (delegated check)",
			issue:   nil,
			actor:   "alice",
			wantErr: false,
		},
		{
			name:        "unassigned in_progress issue is freely assignable",
			issue:       inProgress(""),
			actor:       "alice",
			newAssignee: "bob",
			wantErr:     false,
		},
		{
			name:        "actor editing its own claim passes",
			issue:       inProgress("alice"),
			actor:       "alice",
			newAssignee: "bob",
			wantErr:     false,
		},
		{
			name:        "idempotent re-assert of the current holder passes",
			issue:       inProgress("bob"),
			actor:       "alice",
			newAssignee: "bob",
			wantErr:     false,
		},
		{
			name:        "reassigning an OPEN assigned bead stays frictionless",
			issue:       &types.Issue{ID: "bd-test", Assignee: "bob", Status: types.StatusOpen},
			actor:       "alice",
			newAssignee: "carol",
			wantErr:     false,
		},
		{
			name:        "live foreign claim refuses without force",
			issue:       inProgress("bob"),
			actor:       "alice",
			newAssignee: "carol",
			wantErr:     true,
		},
		{
			name:        "unassign of a live foreign claim refuses (unclaim's rule)",
			issue:       inProgress("bob"),
			actor:       "alice",
			newAssignee: "",
			wantErr:     true,
		},
		{
			name:        "live foreign claim with force passes",
			issue:       inProgress("bob"),
			actor:       "alice",
			newAssignee: "carol",
			force:       true,
			wantErr:     false,
		},
		{
			name:        "claim.pools alias holder is takeable (matches --claim)",
			issue:       inProgress("fable-crew"),
			actor:       "alice",
			newAssignee: "alice",
			pools:       pools,
			wantErr:     false,
		},
		{
			name:        "nil pool thunk fails closed on a foreign claim",
			issue:       inProgress("fable-crew"),
			actor:       "alice",
			newAssignee: "alice",
			pools:       nil,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssigneeNotStolen(tt.actor, tt.newAssignee, tt.pools, tt.force)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("AssigneeNotStolen() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				// The copy must name the holder, point at coordination, and
				// name --force explicitly (so script authors can adopt it
				// deterministically) — never an eviction recipe like the old
				// "use bd unclaim" claim-refusal copy (wy-zs5s2 item 2).
				for _, frag := range []string{"held by", "coordinate with the holder", "--force", "bd mail"} {
					if !strings.Contains(err.Error(), frag) {
						t.Errorf("AssigneeNotStolen() error %q should contain %q", err.Error(), frag)
					}
				}
			}
		})
	}

	// The pool thunk must be consulted only after the cheap clauses pass —
	// call sites hand it a live config read, and routine dispatch (open-bead
	// reassigns, self-edits) must not pay that round trip.
	poolsCalled = false
	if err := AssigneeNotStolen("alice", "bob", pools, false)("bd-test", &types.Issue{ID: "bd-test", Assignee: "bob", Status: types.StatusOpen}); err != nil {
		t.Errorf("open-bead reassign should pass, got %v", err)
	}
	if poolsCalled {
		t.Errorf("pool thunk consulted on an open-bead reassign; the config read should be lazy")
	}
}

func TestNotClosed(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name:    "nil issue passes",
			issue:   nil,
			wantErr: false,
		},
		{
			name:    "open issue passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusOpen},
			wantErr: false,
		},
		{
			name:    "closed issue fails",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusClosed},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotClosed()("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("NotClosed() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChain(t *testing.T) {
	tests := []struct {
		name       string
		issue      *types.Issue
		validators []IssueValidator
		wantErr    bool
	}{
		{
			name:       "empty chain passes",
			issue:      &types.Issue{ID: "bd-test"},
			validators: []IssueValidator{},
			wantErr:    false,
		},
		{
			name:  "all validators pass",
			issue: &types.Issue{ID: "bd-test", Status: types.StatusOpen},
			validators: []IssueValidator{
				Exists(),
				NotTemplate(),
				NotPinned(false),
			},
			wantErr: false,
		},
		{
			name:  "first validator fails stops chain",
			issue: nil,
			validators: []IssueValidator{
				Exists(),
				NotTemplate(),
			},
			wantErr: true,
		},
		{
			name:  "middle validator fails",
			issue: &types.Issue{ID: "bd-test", IsTemplate: true},
			validators: []IssueValidator{
				Exists(),
				NotTemplate(),
				NotPinned(false),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := Chain(tt.validators...)
			err := chain("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("Chain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHasStatus(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		allowed []types.Status
		wantErr bool
	}{
		{
			name:    "nil issue passes",
			issue:   nil,
			allowed: []types.Status{types.StatusOpen},
			wantErr: false,
		},
		{
			name:    "matching status passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusOpen},
			allowed: []types.Status{types.StatusOpen},
			wantErr: false,
		},
		{
			name:    "one of multiple allowed passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusClosed},
			allowed: []types.Status{types.StatusOpen, types.StatusClosed},
			wantErr: false,
		},
		{
			name:    "non-matching status fails",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusPinned},
			allowed: []types.Status{types.StatusOpen, types.StatusClosed},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HasStatus(tt.allowed...)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("HasStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHasType(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		allowed []types.IssueType
		wantErr bool
	}{
		{
			name:    "nil issue passes",
			issue:   nil,
			allowed: []types.IssueType{types.TypeTask},
			wantErr: false,
		},
		{
			name:    "matching type passes",
			issue:   &types.Issue{ID: "bd-test", IssueType: types.TypeTask},
			allowed: []types.IssueType{types.TypeTask},
			wantErr: false,
		},
		{
			name:    "one of multiple allowed passes",
			issue:   &types.Issue{ID: "bd-test", IssueType: types.TypeBug},
			allowed: []types.IssueType{types.TypeTask, types.TypeBug},
			wantErr: false,
		},
		{
			name:    "non-matching type fails",
			issue:   &types.Issue{ID: "bd-test", IssueType: types.TypeEpic},
			allowed: []types.IssueType{types.TypeTask, types.TypeBug},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HasType(tt.allowed...)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("HasType() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEpicHasOpenChildren(t *testing.T) {
	tests := []struct {
		name           string
		issue          *types.Issue
		force          bool
		openChildCount int
		wantErr        bool
	}{
		{
			name:           "nil issue passes",
			issue:          nil,
			force:          false,
			openChildCount: 5,
			wantErr:        false,
		},
		{
			name:           "non-epic passes regardless of children",
			issue:          &types.Issue{ID: "bd-test", IssueType: types.TypeTask},
			force:          false,
			openChildCount: 10,
			wantErr:        false,
		},
		{
			name:           "epic with no open children passes",
			issue:          &types.Issue{ID: "bd-test", IssueType: types.TypeEpic},
			force:          false,
			openChildCount: 0,
			wantErr:        false,
		},
		{
			name:           "epic with open children fails",
			issue:          &types.Issue{ID: "bd-test", IssueType: types.TypeEpic},
			force:          false,
			openChildCount: 3,
			wantErr:        true,
		},
		{
			name:           "epic with open children passes with force",
			issue:          &types.Issue{ID: "bd-test", IssueType: types.TypeEpic},
			force:          true,
			openChildCount: 3,
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EpicHasOpenChildren(tt.force, tt.openChildCount)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("EpicHasOpenChildren() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestForUpdate(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name:    "nil issue fails",
			issue:   nil,
			wantErr: true,
		},
		{
			name:    "template fails",
			issue:   &types.Issue{ID: "bd-test", IsTemplate: true},
			wantErr: true,
		},
		{
			name:    "regular issue passes",
			issue:   &types.Issue{ID: "bd-test", IsTemplate: false},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := forUpdate()("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("forUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestForClose(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		force   bool
		wantErr bool
	}{
		{
			name:    "nil issue fails",
			issue:   nil,
			force:   false,
			wantErr: true,
		},
		{
			name:    "template fails",
			issue:   &types.Issue{ID: "bd-test", IsTemplate: true},
			force:   false,
			wantErr: true,
		},
		{
			name:    "pinned without force fails",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusPinned},
			force:   false,
			wantErr: true,
		},
		{
			name:    "pinned with force passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusPinned},
			force:   true,
			wantErr: false,
		},
		{
			name:    "regular open issue passes",
			issue:   &types.Issue{ID: "bd-test", Status: types.StatusOpen},
			force:   false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := forClose(tt.force)("bd-test", tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("forClose() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
