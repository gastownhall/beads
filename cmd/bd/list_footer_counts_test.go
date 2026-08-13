package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// A count printed next to a noun is read as a fact about the database, not as a
// property of whatever subset happened to survive the filters. When the two
// diverge the message is worse than silence: "0 in progress" answers "is
// anything in progress?" with a confident no, and the reader has no way to tell
// that the rows which would have said otherwise were removed before counting.
//
// These tests are the general guard on that class. Every integer a summary line
// asserts must be justified by the fixture it was rendered from — so a future
// edit that introduces a new number has to declare what the number means, or
// fail here.

// footerFacts are the quantities a summary line is allowed to state, keyed by
// what they mean. assertEveryNumberIsJustified fails on any number in the line
// that is not one of these values, which is what forces a new number to be
// declared rather than smuggled in.
type footerFacts map[string]int

// assertEveryNumberIsJustified checks that each integer appearing in line is
// explained by facts. It deliberately does NOT check that every fact appears:
// omitting a count is fine (that is the fix in this very file), asserting an
// unexplained one is not.
func assertEveryNumberIsJustified(t *testing.T, line string, facts footerFacts) {
	t.Helper()
	allowed := map[int][]string{}
	for name, v := range facts {
		allowed[v] = append(allowed[v], name)
	}
	for _, tok := range regexp.MustCompile(`\d+`).FindAllString(line, -1) {
		n, err := strconv.Atoi(tok)
		if err != nil {
			t.Fatalf("unparseable integer %q in summary %q", tok, line)
		}
		if _, ok := allowed[n]; !ok {
			t.Errorf("summary asserts the number %d, which no fact about the data explains.\n  summary: %q\n  known facts: %v\n"+
				"If this number is legitimate, add it to footerFacts so it is checked; if it is a count of something the query filtered out, it should not be asserted at all.",
				n, line, facts)
		}
	}
}

// The scenarios below are the cross-product that matters: whether the page was
// cut by --limit, and whether --ready pinned the query to open issues.
func TestListFooterLineCountsAreJustified(t *testing.T) {
	tests := []struct {
		name                     string
		total, open, inProgress  int
		truncated, readyFiltered bool
		facts                    footerFacts
	}{
		{
			name: "mixed statuses, whole result set",
			// A plain listing may state the breakdown: the query could have
			// returned any status, so each count is a real finding.
			total: 9, open: 6, inProgress: 3,
			facts: footerFacts{"total": 9, "open": 6, "in_progress": 3},
		},
		{
			name:  "mixed statuses, page cut by --limit",
			total: 2, open: 1, inProgress: 1, truncated: true,
			facts: footerFacts{"total": 2, "open": 1, "in_progress": 1, "limit-hint": 0},
		},
		{
			// The regression this file exists for. --ready pins the filter to
			// open, so inProgress is 0 by construction for ANY database. The
			// summary must not assert it.
			name:  "ready-filtered, whole result set",
			total: 6, open: 6, inProgress: 0, readyFiltered: true,
			facts: footerFacts{"total": 6},
		},
		{
			name:  "ready-filtered and truncated",
			total: 5, open: 5, inProgress: 0, readyFiltered: true, truncated: true,
			facts: footerFacts{"total": 5, "limit-hint": 0},
		},
		{
			name:  "empty result set",
			total: 0, open: 0, inProgress: 0,
			facts: footerFacts{"total": 0, "open": 0, "in_progress": 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := listFooterLine(tt.total, tt.open, tt.inProgress, tt.truncated, tt.readyFiltered)
			assertEveryNumberIsJustified(t, line, tt.facts)

			// The headline count always describes the rows actually rendered.
			if !strings.Contains(line, fmt.Sprintf("%d", tt.total)) {
				t.Errorf("summary %q omits the rendered row count %d", line, tt.total)
			}
		})
	}
}

// The specific claim: under --ready the summary must not report an in-progress
// count, because the filter guarantees it is zero regardless of the data. A
// reader cannot distinguish "none exist" from "none survived the filter".
func TestListFooterLineReadyOmitsVacuousInProgressCount(t *testing.T) {
	// 40 in-progress issues match the same query; --ready removed them all.
	line := listFooterLine(6, 6, 0, false, true)

	if strings.Contains(line, "in progress)") {
		t.Errorf("--ready summary asserts an in-progress count that the filter forced to zero: %q", line)
	}
	if !strings.Contains(line, "excludes in_progress") {
		t.Errorf("--ready summary must disclose what it filtered, got: %q", line)
	}
	if strings.Contains(line, "0") {
		t.Errorf("--ready summary states a zero the data does not support: %q", line)
	}
}

// Without --ready the breakdown is a genuine finding and must survive: this is
// the half of the behaviour the fix must not regress.
func TestListFooterLineUnfilteredKeepsBreakdown(t *testing.T) {
	line := listFooterLine(9, 6, 3, false, false)
	for _, want := range []string{"Total: 9 issues", "6 open", "3 in progress"} {
		if !strings.Contains(line, want) {
			t.Errorf("unfiltered summary lost %q, got: %q", want, line)
		}
	}
}

// The footer is correct as a pure function only if every renderer actually
// tells it that --ready is in force. The display wrappers are where that can be
// lost: they take readyFiltered as a parameter, and a caller that omits it (or
// a wrapper that defaults it) silently restores the vacuous count with
// listFooterLine itself still passing every test above.
//
// These two tests pin the wrappers the --watch paths display through — the
// surface where a stale "0 in progress" is most likely to be read as a live
// fact, because it is re-rendered every two seconds under a heading that says
// the data is current.
//
// bd list --ready --watch (direct) → displayWatchedIssueList.
func TestDisplayWatchedIssueListReadyFooterDisclosesFilter(t *testing.T) {
	// Open by construction: --ready pinned the query, so these are all the
	// rows any database could return here.
	issues := []*types.Issue{
		{ID: "bd-1", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "bd-2", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	// A nil store is the no-dependency-data arm the function already guards
	// for; the footer is what is under test, and this keeps the file free of
	// the cgo-tagged stub so it still compiles under CGO_ENABLED=0.
	out := captureStdout(t, func() error {
		displayWatchedIssueList(context.Background(), nil, issues, false, true)
		return nil
	})

	if strings.Contains(out, "in progress)") {
		t.Errorf("--ready --watch summary asserts an in-progress count its own filter forced to zero: %q", out)
	}
	if !strings.Contains(out, "excludes in_progress") {
		t.Errorf("--ready --watch summary must disclose what it filtered, got: %q", out)
	}
}

// bd list --ready --watch (proxied) displays through displayPrettyListWithDeps
// directly, so the parameter has to survive that wrapper too.
func TestDisplayPrettyListWithDepsReadyFooterDisclosesFilter(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-1", Title: "A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
	}

	out := captureStdout(t, func() error {
		displayPrettyListWithDeps(issues, false, nil, false, true)
		return nil
	})
	if strings.Contains(out, "in progress)") {
		t.Errorf("proxied --ready --watch summary asserts a filtered-out count: %q", out)
	}
	if !strings.Contains(out, "excludes in_progress") {
		t.Errorf("proxied --ready --watch summary must disclose its filter, got: %q", out)
	}

	// The other half: without --ready the same wrapper must keep the breakdown,
	// so the fix cannot be "never print counts".
	plain := captureStdout(t, func() error {
		displayPrettyListWithDeps(issues, false, nil, false, false)
		return nil
	})
	if !strings.Contains(plain, "in progress)") {
		t.Errorf("unfiltered listing lost its status breakdown: %q", plain)
	}
}

// Truncation and readiness are independent scopes and both must be disclosed
// when both apply — neither may silently mask the other.
func TestListFooterLineTruncationDisclosedAlongsideReady(t *testing.T) {
	line := listFooterLine(5, 5, 0, true, true)
	if !strings.Contains(line, "truncated by --limit") {
		t.Errorf("truncated page must say so even under --ready, got: %q", line)
	}
	if !strings.Contains(line, "excludes in_progress") {
		t.Errorf("--ready must still disclose its filter when truncated, got: %q", line)
	}
	if strings.Contains(line, "Total:") {
		t.Errorf("a truncated page must never be labelled Total: %q", line)
	}
}
