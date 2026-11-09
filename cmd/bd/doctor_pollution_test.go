package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// TestDetectTestPollutionSparesLegitimateWork pins GH#5025: substantive work
// about test infrastructure — and an open P1 epic whose title merely starts
// with "Test-" that was bulk-imported alongside other issues — must not be
// flagged as test pollution. The old scorer gave the epic 1.00 and offered
// --clean deletion on the strength of a title word plus a bulk-import
// timestamp. The fixture ran at the old detector seam at base (see the
// triage repro); it now runs against the shared classifier.
func TestDetectTestPollutionSparesLegitimateWork(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	epic := &types.Issue{
		ID:          "dcr-9f3",
		Title:       "Test-quality architecture",
		Description: "Restructure the component test suite so each layer owns its fixtures and assertions.",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   created,
	}
	// Real engineering work about the project's own test-infrastructure
	// scripts — exactly the kind of title a healthy repo produces.
	testInfraWork := []*types.Issue{
		{ID: "dcr-lzse", Title: "test-driver.sh isolation broken — constants.sh clobbers sibling runs", Description: "constants.sh writes a fixed path shared by parallel test-driver invocations; scope it per run.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-3t0", Title: "test-agents-md-currency.sh asserts the retired wording", Description: "The script still greps for the pre-1.0 wording of AGENTS.md; update it to the current section names.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-dk2n", Title: "test-check-19-spec-a11y-gating.sh scatters advisory residue on failure", Description: "On failure the script leaves advisory .residue files in the repo root; clean them up in the failure path.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-3db", Title: "test-shellcheck.sh teeth fixtures use fixed repo paths", Description: "The fixture scripts hardcode /tmp/teeth; derive the paths from the test's own temp dir.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
	}
	issues := append([]*types.Issue{epic}, testInfraWork...)

	// A bulk import created ten more issues in the same minute — an import
	// signature, not pollution evidence.
	for i := 0; i < 10; i++ {
		issues = append(issues, &types.Issue{
			ID:          fmt.Sprintf("dcr-imp%d", i),
			Title:       fmt.Sprintf("Imported report finding %d", i),
			Description: "Derived from the audit report; see linked findings.",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
			CreatedAt:   created.Add(time.Duration(i) * time.Second),
		})
	}

	if matches := doctor.ClassifyTestPollution(issues); len(matches) != 0 {
		for _, m := range matches {
			t.Errorf("legitimate issue %s %q flagged as test pollution (score %.2f, reasons %v)", m.Issue.ID, m.Issue.Title, m.Score, m.Reasons)
		}
	}
}
