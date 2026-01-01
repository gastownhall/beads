package validation

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestValidateTemplate(t *testing.T) {
	tests := []struct {
		name        string
		issueType   types.IssueType
		description string
		wantErr     bool
		wantMissing int // Number of missing sections expected
	}{
		// Bug type tests
		{
			name:      "bug with all sections",
			issueType: types.TypeBug,
			description: `## Steps to Reproduce
1. Do this
2. Do that

## Acceptance Criteria
- Bug is fixed`,
			wantErr: false,
		},
		{
			name:        "bug missing all sections",
			issueType:   types.TypeBug,
			description: "This is broken",
			wantErr:     true,
			wantMissing: 2,
		},
		{
			name:      "bug missing acceptance criteria",
			issueType: types.TypeBug,
			description: `## Steps to Reproduce
1. Click button
2. See error`,
			wantErr:     true,
			wantMissing: 1,
		},
		{
			name:      "bug with case-insensitive headings",
			issueType: types.TypeBug,
			description: `## steps to reproduce
Click the button

## acceptance criteria
It works`,
			wantErr: false,
		},
		{
			name:      "bug with inline mentions (no markdown)",
			issueType: types.TypeBug,
			description: `Steps to reproduce: click the button.
Acceptance criteria: it should work.`,
			wantErr: false,
		},

		// Task type tests
		{
			name:      "task with acceptance criteria",
			issueType: types.TypeTask,
			description: `## Acceptance Criteria
- [ ] Task complete`,
			wantErr: false,
		},
		{
			name:        "task missing acceptance criteria",
			issueType:   types.TypeTask,
			description: "Do the thing",
			wantErr:     true,
			wantMissing: 1,
		},

		// Feature type tests
		{
			name:      "feature with acceptance criteria",
			issueType: types.TypeFeature,
			description: `Add new widget

## Acceptance Criteria
Widget displays correctly`,
			wantErr: false,
		},
		{
			name:        "feature missing acceptance criteria",
			issueType:   types.TypeFeature,
			description: "Add a new feature",
			wantErr:     true,
			wantMissing: 1,
		},

		// Epic type tests
		{
			name:      "epic with success criteria",
			issueType: types.TypeEpic,
			description: `Big project

## Success Criteria
- Project ships
- Users happy`,
			wantErr: false,
		},
		{
			name:        "epic missing success criteria",
			issueType:   types.TypeEpic,
			description: "Do everything",
			wantErr:     true,
			wantMissing: 1,
		},

		// Types with no requirements
		{
			name:        "chore has no requirements",
			issueType:   types.TypeChore,
			description: "Update deps",
			wantErr:     false,
		},
		{
			name:        "message has no requirements",
			issueType:   types.TypeMessage,
			description: "Hello",
			wantErr:     false,
		},
		{
			name:        "molecule has no requirements",
			issueType:   types.TypeMolecule,
			description: "",
			wantErr:     false,
		},

		// Edge cases
		{
			name:        "empty description for bug",
			issueType:   types.TypeBug,
			description: "",
			wantErr:     true,
			wantMissing: 2,
		},
		{
			name:        "empty description for chore",
			issueType:   types.TypeChore,
			description: "",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTemplate(tt.issueType, tt.description)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateTemplate() expected error, got nil")
					return
				}
				templateErr, ok := err.(*TemplateError)
				if !ok {
					t.Errorf("ValidateTemplate() error type = %T, want *TemplateError", err)
					return
				}
				if len(templateErr.Missing) != tt.wantMissing {
					t.Errorf("ValidateTemplate() missing sections = %d, want %d",
						len(templateErr.Missing), tt.wantMissing)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateTemplate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestTemplateErrorMessage(t *testing.T) {
	err := &TemplateError{
		IssueType: types.TypeBug,
		Missing: []MissingSection{
			{Heading: "## Steps to Reproduce", Hint: "Describe how to reproduce"},
			{Heading: "## Acceptance Criteria", Hint: "Define fix criteria"},
		},
	}

	msg := err.Error()
	if !strings.Contains(msg, "bug") {
		t.Errorf("Error message should contain issue type, got: %s", msg)
	}
	if !strings.Contains(msg, "Steps to Reproduce") {
		t.Errorf("Error message should contain missing heading, got: %s", msg)
	}
	if !strings.Contains(msg, "Describe how to reproduce") {
		t.Errorf("Error message should contain hint, got: %s", msg)
	}
}

func TestTemplateErrorEmpty(t *testing.T) {
	err := &TemplateError{
		IssueType: types.TypeBug,
		Missing:   nil,
	}
	if err.Error() != "" {
		t.Errorf("Empty TemplateError should return empty string, got: %s", err.Error())
	}
}

func TestLintIssue(t *testing.T) {
	tests := []struct {
		name    string
		issue   *types.Issue
		wantErr bool
	}{
		{
			name:    "nil issue",
			issue:   nil,
			wantErr: false,
		},
		{
			name: "valid bug",
			issue: &types.Issue{
				IssueType:   types.TypeBug,
				Description: "## Steps to Reproduce\nClick\n\n## Acceptance Criteria\nFixed",
			},
			wantErr: false,
		},
		{
			name: "invalid bug",
			issue: &types.Issue{
				IssueType:   types.TypeBug,
				Description: "It's broken",
			},
			wantErr: true,
		},
		{
			name: "chore always valid",
			issue: &types.Issue{
				IssueType:   types.TypeChore,
				Description: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := LintIssue(tt.issue)
			if (err != nil) != tt.wantErr {
				t.Errorf("LintIssue() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
