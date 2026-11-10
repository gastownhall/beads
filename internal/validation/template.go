package validation

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
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

// LintSectionsConfigPrefix is the config namespace for per-issue-type
// additional lint sections (G4): lint.sections.<type> holds a
// comma-separated list of section headings, e.g.
//
//	bd config set lint.sections.epic "Standards scorecard, Cost"
//
// The list is ADDITIVE to the built-in sections in
// types.IssueType.RequiredSections: built-in requirements are never relaxed.
const LintSectionsConfigPrefix = "lint.sections."

// ConfiguredLintSections returns the extra required sections configured for
// the issue type under lint.sections.<type>. Each comma-separated entry is
// normalized to a canonical "## <heading>" form (a leading run of '#' and
// surrounding whitespace is stripped, so "# Cost", "## Cost", and "cost"
// all yield "## Cost"), empty entries are dropped, and entries are deduped
// case-insensitively with the first occurrence's casing kept. Returns nil
// when the key is unset or empty.
func ConfiguredLintSections(issueType types.IssueType) []types.RequiredSection {
	raw := config.GetString(LintSectionsConfigPrefix + string(issueType))
	var out []types.RequiredSection
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		text := strings.TrimSpace(part)
		text = strings.TrimSpace(strings.TrimLeft(text, "#"))
		if text == "" {
			continue
		}
		if seen[headingKey("## "+text)] {
			continue
		}
		seen[headingKey("## "+text)] = true
		out = append(out, types.RequiredSection{
			Heading: "## " + text,
			Hint:    "Required by " + LintSectionsConfigPrefix + string(issueType) + " config",
		})
	}
	return out
}

// headingKey normalizes a section heading to a case-insensitive comparison
// key: markdown prefix hashes and surrounding whitespace are dropped.
func headingKey(heading string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(heading), "#")))
}

// mergeLintSections returns the sections ValidateTemplate requires for the
// issue type: the built-ins from types.IssueType.RequiredSections plus the
// additive configured sections from lint.sections.<type> (G4). Deduped
// case-insensitively by heading text, built-ins first so their canonical
// hints win.
func mergeLintSections(issueType types.IssueType) []types.RequiredSection {
	builtIn := issueType.RequiredSections()
	configured := ConfiguredLintSections(issueType)
	if len(configured) == 0 {
		return builtIn
	}
	merged := make([]types.RequiredSection, 0, len(builtIn)+len(configured))
	seen := make(map[string]bool, len(builtIn)+len(configured))
	for _, s := range builtIn {
		if seen[headingKey(s.Heading)] {
			continue
		}
		seen[headingKey(s.Heading)] = true
		merged = append(merged, s)
	}
	for _, s := range configured {
		if seen[headingKey(s.Heading)] {
			continue
		}
		seen[headingKey(s.Heading)] = true
		merged = append(merged, s)
	}
	return merged
}

// ValidateTemplate checks if the description contains all required sections
// for the given issue type: the built-ins from
// types.IssueType.RequiredSections plus the additive configured sections from
// lint.sections.<type> (G4). Returns nil if validation passes or if the
// issue type has no required sections.
//
// Section matching is case-insensitive and looks for the heading text
// anywhere in the description (doesn't require exact markdown format).
func ValidateTemplate(issueType types.IssueType, description string) error {
	required := mergeLintSections(issueType)
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

		if strings.Contains(descLower, headingLower) {
			continue
		}

		// Epics canonically require "Success Criteria", but "Acceptance Criteria"
		// is equally acceptable so epics aren't a special case agents have to
		// remember (GH#3834). Success Criteria remains canonical for new epics
		// (see RequiredSections); this only widens what bd lint accepts.
		if issueType == types.TypeEpic && headingLower == "success criteria" &&
			strings.Contains(descLower, "acceptance criteria") {
			continue
		}

		missing = append(missing, MissingSection{
			Heading: section.Heading,
			Hint:    section.Hint,
		})
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
// It checks both Description and AcceptanceCriteria fields.
// A non-empty AcceptanceCriteria field satisfies the "Acceptance Criteria"
// (or "Success Criteria" for epics) requirement without needing a heading. (GH#2468)
// Returns nil if the issue passes validation or has no requirements.
func LintIssue(issue *types.Issue) error {
	if issue == nil {
		return nil
	}
	text := issue.Description
	if issue.AcceptanceCriteria != "" {
		text = text + "\n" + issue.AcceptanceCriteria
	}
	err := ValidateTemplate(issue.IssueType, text)
	if err == nil || issue.AcceptanceCriteria == "" {
		return err
	}

	// A non-empty AcceptanceCriteria field satisfies "Acceptance Criteria"
	// or "Success Criteria" requirements even without the heading text.
	templateErr, ok := err.(*TemplateError)
	if !ok {
		return err
	}
	var remaining []MissingSection
	for _, m := range templateErr.Missing {
		heading := strings.ToLower(strings.TrimPrefix(m.Heading, "## "))
		if heading == "acceptance criteria" || heading == "success criteria" {
			continue // satisfied by the dedicated field
		}
		remaining = append(remaining, m)
	}
	if len(remaining) == 0 {
		return nil
	}
	templateErr.Missing = remaining
	return templateErr
}

// ValidateCloseReason checks if a close reason meets minimum quality standards.
// Returns nil if the reason is acceptable. Used by validation.on-close config.
func ValidateCloseReason(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || strings.EqualFold(reason, "closed") {
		return fmt.Errorf("close reason is empty or default; provide a summary of what was done")
	}
	if len(reason) < 20 {
		return fmt.Errorf("close reason is terse (%d chars); aim for 20+ characters describing what was done", len(reason))
	}
	return nil
}
