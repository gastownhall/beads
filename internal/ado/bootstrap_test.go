package ado

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func strPtr(s string) *string { return &s }

func TestFindMatch_ExternalRef(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, false)

	adoItem := &tracker.TrackerIssue{
		ID:    "42",
		Title: "Fix login bug",
		URL:   "https://dev.azure.com/org/proj/_workitems/edit/42",
	}
	localIssues := []*types.Issue{
		{ID: "bd-1", Title: "Unrelated", ExternalRef: strPtr("https://other.com/1")},
		{ID: "bd-2", Title: "Fix login bug", ExternalRef: strPtr("https://dev.azure.com/org/proj/_workitems/edit/42")},
	}

	result := m.FindMatch(adoItem, localIssues)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.BeadsID != "bd-2" {
		t.Errorf("BeadsID = %q, want %q", result.BeadsID, "bd-2")
	}
	if result.MatchType != "external_ref" {
		t.Errorf("MatchType = %q, want %q", result.MatchType, "external_ref")
	}
}

func TestFindMatch_SourceSystem(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, false)

	adoItem := &tracker.TrackerIssue{
		ID:    "42",
		Title: "Fix login bug",
		URL:   "https://dev.azure.com/org/proj/_workitems/edit/42",
	}
	localIssues := []*types.Issue{
		{ID: "bd-1", Title: "Unrelated", SourceSystem: "github:99"},
		{ID: "bd-2", Title: "Fix login bug", SourceSystem: "ado:42"},
	}

	result := m.FindMatch(adoItem, localIssues)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.BeadsID != "bd-2" {
		t.Errorf("BeadsID = %q, want %q", result.BeadsID, "bd-2")
	}
	if result.MatchType != "source_system" {
		t.Errorf("MatchType = %q, want %q", result.MatchType, "source_system")
	}
}

func TestFindMatch_SourceSystemURL(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, false)

	adoItem := &tracker.TrackerIssue{
		ID:    "42",
		Title: "Fix login bug",
		URL:   "https://dev.azure.com/org/proj/_workitems/edit/42",
	}
	localIssues := []*types.Issue{
		{ID: "bd-5", Title: "Fix login bug", SourceSystem: "ado:https://dev.azure.com/org/proj/_workitems/edit/42"},
	}

	result := m.FindMatch(adoItem, localIssues)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.BeadsID != "bd-5" {
		t.Errorf("BeadsID = %q, want %q", result.BeadsID, "bd-5")
	}
	if result.MatchType != "source_system" {
		t.Errorf("MatchType = %q, want %q", result.MatchType, "source_system")
	}
}

func TestFindMatch_HeuristicExactMatch(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		URL:       "https://dev.azure.com/org/proj/_workitems/edit/42",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix login bug", IssueType: types.TypeBug, CreatedAt: now.Add(2 * time.Hour)},
	}

	result := m.FindMatch(adoItem, localIssues)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.BeadsID != "bd-10" {
		t.Errorf("BeadsID = %q, want %q", result.BeadsID, "bd-10")
	}
	if result.MatchType != "heuristic" {
		t.Errorf("MatchType = %q, want %q", result.MatchType, "heuristic")
	}
	if result.Candidates != 1 {
		t.Errorf("Candidates = %d, want 1", result.Candidates)
	}
}

func TestFindMatch_HeuristicDisabled(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, false) // heuristic disabled

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix login bug", IssueType: types.TypeBug, CreatedAt: now},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match when heuristic disabled")
	}
}

func TestFindMatch_HeuristicTitleMismatch(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix logout bug", IssueType: types.TypeBug, CreatedAt: now},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match for title mismatch")
	}
}

func TestFindMatch_HeuristicTypeMismatch(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix login bug", IssueType: types.TypeFeature, CreatedAt: now},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match for type mismatch")
	}
}

func TestFindMatch_HeuristicTimeOutsideWindow(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix login bug", IssueType: types.TypeBug, CreatedAt: now.Add(25 * time.Hour)},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match for time outside window")
	}
}

func TestFindMatch_HeuristicMultipleCandidates(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		Type:      "Bug",
		CreatedAt: now,
	}
	localIssues := []*types.Issue{
		{ID: "bd-10", Title: "Fix login bug", IssueType: types.TypeBug, CreatedAt: now.Add(1 * time.Hour)},
		{ID: "bd-11", Title: "Fix login bug", IssueType: types.TypeBug, CreatedAt: now.Add(2 * time.Hour)},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match for ambiguous candidates")
	}
	if result.Candidates != 2 {
		t.Errorf("Candidates = %d, want 2", result.Candidates)
	}
}

func TestFindMatch_NoMatch(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		URL:       "https://dev.azure.com/org/proj/_workitems/edit/42",
		Type:      "Bug",
		CreatedAt: time.Now(),
	}
	localIssues := []*types.Issue{
		{ID: "bd-1", Title: "Totally different issue", IssueType: types.TypeTask},
	}

	result := m.FindMatch(adoItem, localIssues)
	if result.Matched {
		t.Fatal("expected no match")
	}
}

func TestFindMatch_PriorityOrder(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	now := time.Now()
	adoItem := &tracker.TrackerIssue{
		ID:        "42",
		Title:     "Fix login bug",
		URL:       "https://dev.azure.com/org/proj/_workitems/edit/42",
		Type:      "Bug",
		CreatedAt: now,
	}
	// Issue matches both external_ref AND heuristic — external_ref should win.
	localIssues := []*types.Issue{
		{
			ID:          "bd-99",
			Title:       "Fix login bug",
			ExternalRef: strPtr("https://dev.azure.com/org/proj/_workitems/edit/42"),
			IssueType:   types.TypeBug,
			CreatedAt:   now,
		},
	}

	result := m.FindMatch(adoItem, localIssues)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.MatchType != "external_ref" {
		t.Errorf("MatchType = %q, want %q", result.MatchType, "external_ref")
	}
}

func TestFindMatch_EmptyLocalIssues(t *testing.T) {
	mapper := NewFieldMapper(nil, nil)
	m := NewBootstrapMatcher(mapper, true)

	adoItem := &tracker.TrackerIssue{
		ID:    "42",
		Title: "Fix login bug",
		URL:   "https://dev.azure.com/org/proj/_workitems/edit/42",
	}

	result := m.FindMatch(adoItem, nil)
	if result.Matched {
		t.Fatal("expected no match for empty local issues")
	}

	result = m.FindMatch(adoItem, []*types.Issue{})
	if result.Matched {
		t.Fatal("expected no match for empty slice")
	}
}

func TestExtractIDFromSourceSystem(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ado:42", "42"},
		{"ado:https://dev.azure.com/org/proj/_workitems/edit/42", "42"},
		{"github:123", ""},
		{"", ""},
		{"ado:", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := extractIDFromSourceSystem(tc.input)
			if got != tc.want {
				t.Errorf("extractIDFromSourceSystem(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
