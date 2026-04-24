package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSummaryRenderParity covers the be-nu4.3.2 render-parity hard gate:
// for every row in a 1K-row fixture that includes a pinned permanent AND a
// pinned wisp, formatIssueCompact(issue) and formatSummaryCompact(summary)
// must produce byte-identical output. Same for formatAgentIssue /
// formatSummaryAgent.
//
// Drift means IssueSummary is missing a field the formatter dereferences OR
// formatSummaryCompact/formatSummaryAgent got out of sync with its full-Issue
// counterpart. The first-diff byte offset in the failure message points at
// the exact divergence.
//
// The fixture is built in-memory (no store) — the storage layer's ID parity
// is covered in internal/storage/dolt/search_summary_test.go.
func TestSummaryRenderParity(t *testing.T) {
	pairs := buildParityFixture(1000)

	t.Run("compact", func(t *testing.T) {
		var issueBuf, summaryBuf strings.Builder
		for _, p := range pairs {
			formatIssueCompact(&issueBuf, p.issue, p.summary.Labels,
				p.blockedBy, p.blocks, p.parent)
			formatSummaryCompact(&summaryBuf, p.summary, p.summary.Labels,
				p.blockedBy, p.blocks, p.parent)
		}
		got := issueBuf.String()
		want := summaryBuf.String()
		if got != want {
			at := firstByteDiff(got, want)
			t.Fatalf("compact render mismatch at byte %d\n--- formatIssueCompact ---\n%s\n--- formatSummaryCompact ---\n%s",
				at,
				snippetAround(got, at),
				snippetAround(want, at))
		}
	})

	t.Run("agent", func(t *testing.T) {
		var issueBuf, summaryBuf strings.Builder
		for _, p := range pairs {
			formatAgentIssue(&issueBuf, p.issue,
				p.blockedBy, p.blocks, p.parent)
			formatSummaryAgent(&summaryBuf, p.summary,
				p.blockedBy, p.blocks, p.parent)
		}
		got := issueBuf.String()
		want := summaryBuf.String()
		if got != want {
			at := firstByteDiff(got, want)
			t.Fatalf("agent render mismatch at byte %d\n--- formatAgentIssue ---\n%s\n--- formatSummaryAgent ---\n%s",
				at,
				snippetAround(got, at),
				snippetAround(want, at))
		}
	})

	// Fixture shape guard: regression in buildParityFixture must not silently
	// drop the pinned items that exercise pinIndicator / pinIndicatorSummary.
	var pinnedPerm, pinnedWisp bool
	for _, p := range pairs {
		if !p.issue.Pinned {
			continue
		}
		if p.issue.Ephemeral {
			pinnedWisp = true
		} else {
			pinnedPerm = true
		}
	}
	if !pinnedPerm {
		t.Error("fixture missing pinned permanent issue")
	}
	if !pinnedWisp {
		t.Error("fixture missing pinned wisp")
	}
}

// parityPair bundles an Issue with an equivalent IssueSummary plus the
// per-row blocking context formatters require. Every field IssueSummary
// exposes is mirrored in Issue with the same value, so if a format function
// reads anything IssueSummary doesn't expose, the two renders will diverge
// on Issue.<that field> being zero vs non-zero on Summary's side.
type parityPair struct {
	issue     *types.Issue
	summary   *types.IssueSummary
	blockedBy []string
	blocks    []string
	parent    string
}

func buildParityFixture(n int) []parityPair {
	numWisps := n / 4
	numPerms := n - numWisps
	statuses := []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusClosed}
	issueTypes := []types.IssueType{types.TypeTask, types.TypeBug, types.TypeFeature, types.TypeEpic}
	labelSets := [][]string{nil, {"perf"}, {"storage"}, {"perf", "storage"}}
	out := make([]parityPair, 0, n)

	for i := 0; i < numPerms; i++ {
		id := fmt.Sprintf("par-perm-%04d", i)
		title := fmt.Sprintf("parity summary perm %04d", i)
		pinned := i == 7
		if pinned {
			title = "parity summary perm 0007 (pinned)"
		}
		status := statuses[i%len(statuses)]
		issueType := issueTypes[i%len(issueTypes)]
		assignee := fmt.Sprintf("user-%d", i%5)
		labels := labelSets[i%len(labelSets)]

		issue := &types.Issue{
			ID:        id,
			Title:     title,
			Status:    status,
			Priority:  i % 5,
			IssueType: issueType,
			Assignee:  assignee,
			Pinned:    pinned,
			Labels:    labels,
		}
		summary := &types.IssueSummary{
			ID:        id,
			Title:     title,
			Status:    status,
			Priority:  i % 5,
			IssueType: issueType,
			Assignee:  assignee,
			Pinned:    pinned,
			Labels:    labels,
		}
		// Vary blocking context so formatDependencyInfo branches exercise
		// both "" and non-"" paths.
		p := parityPair{issue: issue, summary: summary}
		switch i % 4 {
		case 1:
			p.blockedBy = []string{"par-perm-0001"}
		case 2:
			p.blocks = []string{"par-perm-0002"}
		case 3:
			p.parent = "par-perm-0000"
		}
		out = append(out, p)
	}

	for i := 0; i < numWisps; i++ {
		id := fmt.Sprintf("par-wisp-%04d", i)
		title := fmt.Sprintf("parity summary wisp %04d", i)
		pinned := i == 3
		if pinned {
			title = "parity summary wisp 0003 (pinned)"
		}
		status := types.StatusOpen
		issueType := types.TypeTask
		labels := labelSets[i%len(labelSets)]

		issue := &types.Issue{
			ID:        id,
			Title:     title,
			Status:    status,
			Priority:  i % 5,
			IssueType: issueType,
			Ephemeral: true,
			Pinned:    pinned,
			Labels:    labels,
		}
		summary := &types.IssueSummary{
			ID:        id,
			Title:     title,
			Status:    status,
			Priority:  i % 5,
			IssueType: issueType,
			Pinned:    pinned,
			Labels:    labels,
		}
		out = append(out, parityPair{issue: issue, summary: summary})
	}

	return out
}

func firstByteDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func snippetAround(s string, at int) string {
	const window = 120
	start := at - window
	if start < 0 {
		start = 0
	}
	end := at + window
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
