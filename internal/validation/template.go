package validation

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MissingSection describes a section that should be present but isn't.
type MissingSection struct {
	Heading string // The expected heading, e.g., "## Steps to Reproduce"
	Hint    string // Guidance for what to include
}

// TemplateError is returned when template validation fails.
// It contains all missing sections for a single error report.
type TemplateError struct {
	IssueType types.IssueType
	Missing   []MissingSection
}

func (e *TemplateError) Error() string {
	if len(e.Missing) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "missing required sections for %s:", e.IssueType)
	for _, m := range e.Missing {
		fmt.Fprintf(&b, "\n  - %s (%s)", m.Heading, m.Hint)
	}
	return b.String()
}

// ValidateTemplate checks if the description contains all required sections
// for the given issue type. Returns nil if validation passes or if the
// issue type has no required sections.
//
// Section matching is case-insensitive and looks for the heading text
// anywhere in the description (doesn't require exact markdown format).
func ValidateTemplate(issueType types.IssueType, description string) error {
	required := issueType.RequiredSections()
	if len(required) == 0 {
		return nil
	}

	descLower := strings.ToLower(description)
	var missing []MissingSection

	for _, section := range required {
		// Extract the heading text without markdown prefix for flexible matching
		// e.g., "## Steps to Reproduce" -> "steps to reproduce"
		headingText := strings.TrimPrefix(section.Heading, "## ")
		headingText = strings.TrimPrefix(headingText, "# ")
		headingLower := strings.ToLower(headingText)

		if !strings.Contains(descLower, headingLower) {
			missing = append(missing, MissingSection{
				Heading: section.Heading,
				Hint:    section.Hint,
			})
		}
	}

	if len(missing) > 0 {
		return &TemplateError{
			IssueType: issueType,
			Missing:   missing,
		}
	}
	return nil
}

// LintIssue checks an existing issue for missing template sections.
// Unlike ValidateTemplate, this operates on a full Issue struct.
// Returns nil if the issue passes validation or has no requirements.
func LintIssue(issue *types.Issue) error {
	if issue == nil {
		return nil
	}
	return ValidateTemplate(issue.IssueType, issue.Description)
}
