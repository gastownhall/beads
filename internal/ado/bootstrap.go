package ado

import (
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

const (
	// bootstrapTimeWindow is the maximum time difference for heuristic matching.
	bootstrapTimeWindow = 24 * time.Hour
)

// MatchResult describes the outcome of a bootstrap match attempt.
type MatchResult struct {
	Matched    bool   // Whether a match was found
	BeadsID    string // The matched beads issue ID (if matched)
	MatchType  string // How it matched: "external_ref", "source_system", "heuristic", ""
	Candidates int    // Number of heuristic candidates found (for warnings)
}

// BootstrapMatcher handles deduplication during first sync.
type BootstrapMatcher struct {
	Mapper         tracker.FieldMapper
	HeuristicMatch bool // Whether heuristic matching is enabled (--bootstrap-match)
}

// NewBootstrapMatcher creates a new matcher. Set heuristicMatch=true to enable
// title+type+time matching (opt-in via --bootstrap-match flag).
func NewBootstrapMatcher(mapper tracker.FieldMapper, heuristicMatch bool) *BootstrapMatcher {
	return &BootstrapMatcher{
		Mapper:         mapper,
		HeuristicMatch: heuristicMatch,
	}
}

// FindMatch searches local beads issues for one that matches the given ADO work item.
// It checks matching policies in priority order: external_ref → source_system → heuristic.
// localIssues should be the full set of beads issues to search against.
// adoItem is the incoming ADO TrackerIssue to find a match for.
func (m *BootstrapMatcher) FindMatch(adoItem *tracker.TrackerIssue, localIssues []*types.Issue) MatchResult {
	// 1. External ref match
	for _, issue := range localIssues {
		if issue.ExternalRef != nil && *issue.ExternalRef == adoItem.URL {
			return MatchResult{
				Matched:   true,
				BeadsID:   issue.ID,
				MatchType: "external_ref",
			}
		}
	}

	// 2. Source system match
	for _, issue := range localIssues {
		id := extractIDFromSourceSystem(issue.SourceSystem)
		if id != "" && id == adoItem.ID {
			return MatchResult{
				Matched:   true,
				BeadsID:   issue.ID,
				MatchType: "source_system",
			}
		}
	}

	// 3. Heuristic match (opt-in)
	if m.HeuristicMatch {
		adoBeadsType := m.Mapper.TypeToBeads(adoItem.Type)
		var candidates []*types.Issue
		for _, issue := range localIssues {
			if issue.Title != adoItem.Title {
				continue
			}
			if issue.IssueType != adoBeadsType {
				continue
			}
			diff := issue.CreatedAt.Sub(adoItem.CreatedAt)
			if diff < 0 {
				diff = -diff
			}
			if diff > bootstrapTimeWindow {
				continue
			}
			candidates = append(candidates, issue)
		}
		if len(candidates) == 1 {
			return MatchResult{
				Matched:    true,
				BeadsID:    candidates[0].ID,
				MatchType:  "heuristic",
				Candidates: 1,
			}
		}
		if len(candidates) > 1 {
			return MatchResult{
				Matched:    false,
				Candidates: len(candidates),
			}
		}
	}

	// 4. No match
	return MatchResult{}
}

// BootstrapIndex holds pre-built lookup maps for O(1) matching.
type BootstrapIndex struct {
	// ExternalRefMap maps external_ref URL → issue for O(1) external ref lookups.
	ExternalRefMap map[string]*types.Issue
	// SourceSystemMap maps extracted source system ID → issue for O(1) source_system lookups.
	SourceSystemMap map[string]*types.Issue
	// TitleMap maps lowercase title → issues for O(1) heuristic candidate lookup.
	TitleMap map[string][]*types.Issue
}

// BuildBootstrapIndex pre-indexes a slice of issues for fast matching.
func BuildBootstrapIndex(issues []*types.Issue) *BootstrapIndex {
	idx := &BootstrapIndex{
		ExternalRefMap:  make(map[string]*types.Issue, len(issues)),
		SourceSystemMap: make(map[string]*types.Issue, len(issues)),
		TitleMap:        make(map[string][]*types.Issue, len(issues)),
	}
	for _, issue := range issues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			idx.ExternalRefMap[*issue.ExternalRef] = issue
		}
		if id := extractIDFromSourceSystem(issue.SourceSystem); id != "" {
			idx.SourceSystemMap[id] = issue
		}
		key := strings.ToLower(issue.Title)
		idx.TitleMap[key] = append(idx.TitleMap[key], issue)
	}
	return idx
}

// FindMatchIndexed searches pre-indexed local issues for a match against the given ADO work item.
// Uses O(1) hash lookups instead of O(N) iteration.
func (m *BootstrapMatcher) FindMatchIndexed(adoItem *tracker.TrackerIssue, idx *BootstrapIndex) MatchResult {
	if idx == nil {
		return MatchResult{}
	}

	// 1. External ref match — O(1).
	if issue, ok := idx.ExternalRefMap[adoItem.URL]; ok {
		return MatchResult{
			Matched:   true,
			BeadsID:   issue.ID,
			MatchType: "external_ref",
		}
	}

	// 2. Source system match — O(1).
	if issue, ok := idx.SourceSystemMap[adoItem.ID]; ok {
		return MatchResult{
			Matched:   true,
			BeadsID:   issue.ID,
			MatchType: "source_system",
		}
	}

	// 3. Heuristic match (opt-in) — O(K) where K = issues with same title.
	if m.HeuristicMatch {
		adoBeadsType := m.Mapper.TypeToBeads(adoItem.Type)
		key := strings.ToLower(adoItem.Title)
		candidates := make([]*types.Issue, 0)
		for _, issue := range idx.TitleMap[key] {
			if issue.IssueType != adoBeadsType {
				continue
			}
			diff := issue.CreatedAt.Sub(adoItem.CreatedAt)
			if diff < 0 {
				diff = -diff
			}
			if diff > bootstrapTimeWindow {
				continue
			}
			candidates = append(candidates, issue)
		}
		if len(candidates) == 1 {
			return MatchResult{
				Matched:    true,
				BeadsID:    candidates[0].ID,
				MatchType:  "heuristic",
				Candidates: 1,
			}
		}
		if len(candidates) > 1 {
			return MatchResult{
				Matched:    false,
				Candidates: len(candidates),
			}
		}
	}

	// 4. No match
	return MatchResult{}
}

// extractIDFromSourceSystem extracts the work item ID from a source_system string
// like "ado:https://dev.azure.com/org/proj/_workitems/edit/42" or "ado:42".
func extractIDFromSourceSystem(sourceSystem string) string {
	if !strings.HasPrefix(sourceSystem, "ado:") {
		return ""
	}
	value := sourceSystem[len("ado:"):]
	if value == "" {
		return ""
	}
	// If it's a URL, extract the last path segment.
	if strings.Contains(value, "/") {
		idx := strings.LastIndex(value, "/")
		value = value[idx+1:]
	}
	return value
}
