package validation

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// TestLintMaxCharsConfig is the lint.max-chars.<field> contract: a positive
// limit reports the field in TemplateError.TooLong with its actual length;
// unset or non-positive limits are inert; length counts characters, not
// bytes; and length findings join missing-section findings in one error.
func TestLintMaxCharsConfig(t *testing.T) {
	const acceptance = "- done"

	t.Run("no limit configured", func(t *testing.T) {
		isolateLintConfig(t)
		issue := &types.Issue{IssueType: types.TypeTask, Design: strings.Repeat("x", 100000), AcceptanceCriteria: acceptance}
		if err := LintIssue(issue); err != nil {
			t.Fatalf("unconfigured limits must be inert, got %v", err)
		}
	})

	t.Run("at and over the limit", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.max-chars.design", 10)
		atLimit := &types.Issue{IssueType: types.TypeTask, Design: strings.Repeat("x", 10), AcceptanceCriteria: acceptance}
		if err := LintIssue(atLimit); err != nil {
			t.Fatalf("field at the limit should pass, got %v", err)
		}

		over := &types.Issue{IssueType: types.TypeTask, Design: strings.Repeat("x", 11), AcceptanceCriteria: acceptance}
		te := lintTemplateError(t, LintIssue(over))
		want := []FieldLength{{Field: "design", Chars: 11, Max: 10}}
		if len(te.Missing) != 0 || len(te.TooLong) != 1 || te.TooLong[0] != want[0] {
			t.Fatalf("got Missing=%v TooLong=%v, want TooLong=%v only", te.Missing, te.TooLong, want)
		}
		if msg := te.Error(); !strings.Contains(msg, "design: 11 chars (max 10)") {
			t.Fatalf("error message %q does not name the field and lengths", msg)
		}
	})

	t.Run("counts characters, not bytes", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.max-chars.title", 5)
		issue := &types.Issue{IssueType: types.TypeTask, Title: "äöüßé", AcceptanceCriteria: acceptance}
		if err := LintIssue(issue); err != nil {
			t.Fatalf("5 multi-byte characters must fit a 5-char limit, got %v", err)
		}
	})

	t.Run("non-positive limit is inert", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.max-chars.notes", 0)
		config.Set("lint.max-chars.design", -1)
		issue := &types.Issue{IssueType: types.TypeTask, Notes: "n", Design: "d", AcceptanceCriteria: acceptance}
		if err := LintIssue(issue); err != nil {
			t.Fatalf("zero and negative limits must be inert, got %v", err)
		}
	})

	t.Run("joins missing sections in one error", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.max-chars.description", 3)
		issue := &types.Issue{IssueType: types.TypeTask, Description: "too long"}
		te := lintTemplateError(t, LintIssue(issue))
		if len(te.Missing) != 1 || te.Missing[0].Heading != "## Acceptance Criteria" {
			t.Fatalf("Missing = %v, want the built-in Acceptance Criteria", te.Missing)
		}
		if len(te.TooLong) != 1 || te.TooLong[0].Field != "description" {
			t.Fatalf("TooLong = %v, want description", te.TooLong)
		}
	})
}

// lintTemplateError asserts err is a *TemplateError and returns it.
func lintTemplateError(t *testing.T, err error) *TemplateError {
	t.Helper()
	te, ok := err.(*TemplateError)
	if !ok {
		t.Fatalf("expected *TemplateError, got %T: %v", err, err)
	}
	return te
}
