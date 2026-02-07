package validation

import (
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
