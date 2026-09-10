package validation

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// isolateLintConfig resets the process-wide config singleton so the test
// starts from a known state and can set lint.sections.* values in memory.
// BEADS_TEST_IGNORE_REPO_CONFIG keeps the checkout's own .beads/config.yaml
// out of the picture; the real user-level config on dev machines carries no
// lint.* keys, and config.Set() overrides any file value anyway.
func isolateLintConfig(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// missingHeadings extracts the ordered list of missing headings from a
// TemplateError, or nil when validation passed.
func missingHeadings(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	te, ok := err.(*TemplateError)
	if !ok {
		t.Fatalf("expected *TemplateError, got %T: %v", err, err)
	}
	out := make([]string, len(te.Missing))
	for i, m := range te.Missing {
		out[i] = m.Heading
	}
	return out
}

// assertMissing fails unless err is a TemplateError whose missing headings
// match want exactly (order and content).
func assertMissing(t *testing.T, err error, want ...string) {
	t.Helper()
	got := missingHeadings(t, err)
	if len(got) != len(want) {
		t.Fatalf("missing sections = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing sections = %v, want %v", got, want)
		}
	}
}

// TestLintSectionsConfigAdditive is the G4 contract: lint.sections.<type>
// (comma-separated, read through the normal config plumbing) ADDS sections
// to the built-ins in types.IssueType.RequiredSections; it never replaces
// them. Deduping is case-insensitive, so configuring a section a type
// already requires changes nothing.
func TestLintSectionsConfigAdditive(t *testing.T) {
	const epicWithSuccess = "## Success Criteria\n- Shipped"

	t.Run("built-in behavior without config", func(t *testing.T) {
		isolateLintConfig(t)
		if err := ValidateTemplate(types.TypeEpic, epicWithSuccess); err != nil {
			t.Fatalf("epic with built-in section should pass, got %v", err)
		}
		assertMissing(t, ValidateTemplate(types.TypeEpic, "big initiative"), "## Success Criteria")
	})

	t.Run("configured sections are additive to built-ins", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.sections.epic", "Standards scorecard, ## Cost")
		assertMissing(t, ValidateTemplate(types.TypeEpic, epicWithSuccess),
			"## Standards scorecard", "## Cost")

		full := epicWithSuccess + "\n\n## Standards scorecard\n- meets score\n\n## Cost\n- low"
		if err := ValidateTemplate(types.TypeEpic, full); err != nil {
			t.Fatalf("epic with all built-in and configured sections should pass, got %v", err)
		}
	})

	t.Run("configured section duplicating a built-in is deduped", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.sections.epic", "Success Criteria")
		if err := ValidateTemplate(types.TypeEpic, epicWithSuccess); err != nil {
			t.Fatalf("deduped built-in must not double-report, got %v", err)
		}
		assertMissing(t, ValidateTemplate(types.TypeEpic, "nope"), "## Success Criteria")
	})

	t.Run("other issue types are unaffected", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.sections.epic", "Standards scorecard")
		if err := ValidateTemplate(types.TypeTask, "## Acceptance Criteria\n- done"); err != nil {
			t.Fatalf("task must not be affected by an epic-only config, got %v", err)
		}
	})

	t.Run("empty and whitespace-only value is inert", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.sections.epic", "  ,  ,")
		if err := ValidateTemplate(types.TypeEpic, epicWithSuccess); err != nil {
			t.Fatalf("empty configured list should be inert, got %v", err)
		}
	})

	t.Run("LintIssue sees configured sections too", func(t *testing.T) {
		isolateLintConfig(t)
		config.Set("lint.sections.epic", "Cost")

		// Description carries Success Criteria; the configured Cost section is missing.
		issue := &types.Issue{IssueType: types.TypeEpic, Description: epicWithSuccess}
		assertMissing(t, LintIssue(issue), "## Cost")

		// AcceptanceCriteria field satisfies Success Criteria (no heading needed),
		// but the configured section still has to appear in the description.
		issue = &types.Issue{IssueType: types.TypeEpic, Description: "epic body", AcceptanceCriteria: "done"}
		assertMissing(t, LintIssue(issue), "## Cost")
	})
}

// TestConfiguredLintSectionsNormalizesHeadings pins the value grammar:
// comma-separated entries, arbitrary leading '#' runs and surrounding
// whitespace normalized to "## <heading>", empties dropped, case-insensitive
// dedupe keeping the first occurrence's casing.
func TestConfiguredLintSectionsNormalizesHeadings(t *testing.T) {
	isolateLintConfig(t)
	config.Set("lint.sections.bug", "  # repro steps ,  ## RePro Steps , , Acceptance criteria")

	got := ConfiguredLintSections(types.TypeBug)
	want := []types.RequiredSection{
		{Heading: "## repro steps", Hint: "Required by lint.sections.bug config"},
		{Heading: "## Acceptance criteria", Hint: "Required by lint.sections.bug config"},
	}
	if len(got) != len(want) {
		t.Fatalf("ConfiguredLintSections = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ConfiguredLintSections[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Unset key: no configured sections.
	config.Set("lint.sections.spike", "")
	if got := ConfiguredLintSections(types.TypeSpike); len(got) != 0 {
		t.Fatalf("empty value should yield no sections, got %+v", got)
	}
}
