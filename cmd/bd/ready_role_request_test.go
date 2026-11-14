package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// TestReadyRoleRequestDropsThePage pins what both role-taking questions of
// `bd ready` share: the claim and the count are asked over the LISTING's
// request with the page removed.
//
// Both roles refuse a Limit and an Offset (ErrValidation). For the count,
// published as "showing 2 of N", a limit that survived would make N the size of
// the page instead of the size of the set.
func TestReadyRoleRequestDropsThePage(t *testing.T) {
	got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--limit", "5", "--label", "api"), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	// The listing itself is bounded, which is what makes the drop observable.
	if got.in.Limit == nil || *got.in.Limit != 5 {
		t.Fatalf("the listing request carries limit %v, want 5: the drop below would prove nothing", got.in.Limit)
	}

	req := readyRoleRequest(got.in)
	if req.Limit != nil {
		t.Errorf("role request carries limit %d; both roles refuse one", *req.Limit)
	}
	if req.Offset != 0 {
		t.Errorf("role request carries offset %d; both roles refuse one", req.Offset)
	}
	if !slices.Equal(req.Labels, []string{"api"}) {
		t.Errorf("role request Labels = %q, want the listing's own %q", req.Labels, []string{"api"})
	}

	// The claim asks the same question, plus the claimant: one builder, so the
	// count and the claim cannot drift apart from each other either.
	claim := claimNextRequest(got.in)
	if !reflect.DeepEqual(claim.Filter, req) {
		t.Errorf("the claim's filter and the shared role request differ:\nclaim: %+v\nrole:  %+v", claim.Filter, req)
	}

	// And the predicate the count actually runs is the listing's, unbounded —
	// the identity issueops.ReadyCounter promises, checked at the front door
	// rather than only at the three backends.
	counted, err := workapi.BuildReadyCountFilter(req)
	if err != nil {
		t.Fatalf("BuildReadyCountFilter: %v", err)
	}
	unlimited := 0
	listing := got.in.ReadyRequest
	listing.Limit = &unlimited
	want, err := workapi.BuildReadyFilter(listing)
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if !reflect.DeepEqual(counted, want) {
		t.Errorf("the count's filter and the unbounded listing filter differ:\ncount:   %+v\nlisting: %+v", counted, want)
	}
}

// TestReadyRoleRequestCarriesTheDirectoryLabelDefault pins the one thing the
// role request adds that the raw flags do not: GH#541's configured label,
// applied under the same gate the listing applies it under. A count or a claim
// that missed the default would answer for a wider set than the listing beside
// it shows.
func TestReadyRoleRequestCarriesTheDirectoryLabelDefault(t *testing.T) {
	const configured = "scope:web"
	configureDirectoryLabel(t, configured)

	got := runGatherReadyInput(t, newReadyFlagsCommand(t), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	if req := readyRoleRequest(got.in); !slices.Equal(req.LabelsAny, []string{configured}) {
		t.Errorf("role request LabelsAny = %q, want the configured %q", req.LabelsAny, []string{configured})
	}

	// An explicit label suppresses it, the same way it suppresses the
	// listing's.
	explicit := runGatherReadyInput(t, newReadyFlagsCommand(t, "--label", "chosen"), nil)
	if explicit.err != nil {
		t.Fatalf("gatherReadyInput: %v", explicit.err)
	}
	if req := readyRoleRequest(explicit.in); len(req.LabelsAny) != 0 {
		t.Errorf("role request LabelsAny = %q with an explicit --label, want none", req.LabelsAny)
	}
}

// TestReadyBlockedRequestIsTheListingScopeWithoutAPage pins the question the
// empty state asks bd blocked: the listing's parent and label sets, and no
// page or row cap. The probe used to take the whole listing filter, so
// `bd ready --max-rows 2` over three blocked issues had the blocked list
// refused by the cap and printed that nothing was blocked.
func TestReadyBlockedRequestIsTheListingScopeWithoutAPage(t *testing.T) {
	capAtTwo := func(*cobra.Command) (int, string, error) { return 2, "--max-rows", nil }
	got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--limit", "1", "--label", "api", "--label-any", "x",
		"--exclude-label", "y", "--parent", "bd-p", "--assignee", "alice", "--priority", "1"), capAtTwo)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	if got.in.filter.Limit != 1 || got.in.filter.MaxRows != 2 {
		t.Fatalf("the listing filter carries limit %d, cap %d; want 1 and 2, or the drop below proves nothing",
			got.in.filter.Limit, got.in.filter.MaxRows)
	}

	req := readyBlockedRequest(got.in)
	want := issueops.BlockedRequest{ParentID: "bd-p", Labels: []string{"api"}, LabelsAny: []string{"x"}, ExcludeLabels: []string{"y"}}
	if !reflect.DeepEqual(req, want) {
		t.Errorf("readyBlockedRequest = %+v, want the listing's scope %+v", req, want)
	}
	probe := storageissueops.BlockedFilter(req)
	if probe.Limit != 0 || probe.Offset != 0 || probe.MaxRows != 0 || probe.MaxRowsSource != "" {
		t.Errorf("the blocked probe carries a page or a cap: %+v", probe)
	}
}

// TestReadyEmptyStateVerdict pins when an empty `bd ready` may say blocking
// dependencies are why, and when it may say nothing is blocked: only an issue
// the listing would show but for its blockers supports the claim, and only an
// answer that covers the listing's scope supports the negative.
func TestReadyEmptyStateVerdict(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	blocked := func(mutate func(*types.Issue)) *types.BlockedIssue {
		b := &types.BlockedIssue{
			Issue:          types.Issue{ID: "bd-b", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			BlockedByCount: 1,
			BlockedBy:      []string{"bd-x"},
		}
		if mutate != nil {
			mutate(&b.Issue)
		}
		return b
	}
	one := 1
	cases := []struct {
		name    string
		req     issueops.ReadyRequest
		blocked []*types.BlockedIssue
		want    readyEmptyVerdict
	}{
		{"nothing_blocked", issueops.ReadyRequest{}, nil, readyEmptyNoneBlocked},
		{"open_blocked_issue", issueops.ReadyRequest{}, []*types.BlockedIssue{blocked(nil)}, readyEmptyBlocked},
		{"blocked_issue_in_progress", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Status = types.StatusInProgress })}, readyEmptyNoneBlocked},
		{"blocked_issue_pinned", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Pinned = true })}, readyEmptyNoneBlocked},
		{"blocked_issue_deferred", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.DeferUntil = &future })}, readyEmptyNoneBlocked},
		{"blocked_issue_deferred_include_deferred", issueops.ReadyRequest{IncludeDeferred: true},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.DeferUntil = &future })}, readyEmptyBlocked},
		{"blocked_issue_defer_passed", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.DeferUntil = &past })}, readyEmptyBlocked},
		{"blocked_ephemeral_wisp", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Ephemeral = true })}, readyEmptyNoneBlocked},
		{"blocked_ephemeral_wisp_include_ephemeral", issueops.ReadyRequest{IncludeEphemeral: true},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Ephemeral = true })}, readyEmptyBlocked},
		{"blocked_issue_of_excluded_type", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.IssueType = types.TypeGate })}, readyEmptyNoneBlocked},
		{"blocked_issue_of_requested_type", issueops.ReadyRequest{IssueType: "gate"},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.IssueType = types.TypeGate })}, readyEmptyBlocked},
		{"blocked_issue_of_another_type", issueops.ReadyRequest{IssueType: "bug"},
			[]*types.BlockedIssue{blocked(nil)}, readyEmptyNoneBlocked},
		{"another_assignees_blocked_issue", issueops.ReadyRequest{Assignee: "alice"},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Assignee = "bob" })}, readyEmptyNoneBlocked},
		{"own_blocked_issue", issueops.ReadyRequest{Assignee: "alice"},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Assignee = "alice" })}, readyEmptyBlocked},
		{"unassigned_listing_assigned_blocked_issue", issueops.ReadyRequest{Unassigned: true},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Assignee = "bob" })}, readyEmptyNoneBlocked},
		{"another_priority", issueops.ReadyRequest{Priority: &one},
			[]*types.BlockedIssue{blocked(nil)}, readyEmptyNoneBlocked},
		{"label_pattern_cannot_be_judged", issueops.ReadyRequest{LabelPattern: "tech-*"},
			[]*types.BlockedIssue{blocked(nil)}, readyEmptyUnknown},
		{"label_pattern_nothing_blocked", issueops.ReadyRequest{LabelPattern: "tech-*"}, nil, readyEmptyNoneBlocked},
		{"custom_status_cannot_be_judged", issueops.ReadyRequest{},
			[]*types.BlockedIssue{blocked(func(i *types.Issue) { i.Status = "review" })}, readyEmptyUnknown},
		{"parent_nothing_blocked", issueops.ReadyRequest{ParentID: "bd-p"}, nil, readyEmptyUnknown},
		{"parent_blocked_child", issueops.ReadyRequest{ParentID: "bd-p"}, []*types.BlockedIssue{blocked(nil)}, readyEmptyBlocked},
		{"a_listable_issue_after_others", issueops.ReadyRequest{}, []*types.BlockedIssue{
			blocked(func(i *types.Issue) { i.Status = types.StatusInProgress }),
			blocked(func(i *types.Issue) { i.Status = "review" }),
			blocked(nil),
		}, readyEmptyBlocked},
		{"nil_entry", issueops.ReadyRequest{}, []*types.BlockedIssue{nil}, readyEmptyNoneBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := workapi.BuildReadyFilter(tc.req)
			if err != nil {
				t.Fatalf("BuildReadyFilter: %v", err)
			}
			if got := readyEmptyStateVerdict(f, tc.blocked, now); got != tc.want {
				t.Errorf("verdict = %d, want %d", got, tc.want)
			}
		})
	}
}

// fakeReadyEmptyStateReads answers the empty state's two reads and records the
// filter the blocked read was asked with.
type fakeReadyEmptyStateReads struct {
	stats      *types.Statistics
	statsErr   error
	blocked    []*types.BlockedIssue
	blockedErr error
	asked      []types.WorkFilter
}

func (f *fakeReadyEmptyStateReads) GetStatistics(context.Context) (*types.Statistics, error) {
	return f.stats, f.statsErr
}

func (f *fakeReadyEmptyStateReads) GetBlockedIssues(_ context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	f.asked = append(f.asked, filter)
	return f.blocked, f.blockedErr
}

// TestPrintReadyEmptyStateTexts pins the empty state's texts, and that a read
// that fails proves nothing: a failed blocked read used to print that no issue
// was blocked, and a failed statistics read that there were no open issues.
func TestPrintReadyEmptyStateTexts(t *testing.T) {
	const (
		claim     = "No ready work found (all issues have blocking dependencies)"
		noneClaim = "Nothing bd ready would list is blocked by an open dependency"
	)
	filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	filter.MaxRows = 2
	in := readyInput{filter: filter}
	open := &types.Statistics{OpenIssues: 1}
	listable := &types.BlockedIssue{Issue: types.Issue{ID: "bd-b", Status: types.StatusOpen, IssueType: types.TypeTask}}
	failed := errors.New("blocked.list refused")

	cases := []struct {
		name   string
		reads  *fakeReadyEmptyStateReads
		want   []string
		absent []string
	}{
		{"no_open_issues", &fakeReadyEmptyStateReads{stats: &types.Statistics{}},
			[]string{"No open issues"}, []string{claim, noneClaim}},
		{"a_listable_issue_is_blocked", &fakeReadyEmptyStateReads{stats: open, blocked: []*types.BlockedIssue{listable}},
			[]string{claim}, []string{noneClaim, "bd recompute-blocked"}},
		{"nothing_blocked", &fakeReadyEmptyStateReads{stats: open},
			[]string{"No ready work found", noneClaim, "bd recompute-blocked"}, []string{claim}},
		{"blocked_read_failed", &fakeReadyEmptyStateReads{stats: open, blockedErr: failed},
			[]string{"No ready work found", "'bd blocked'", "bd recompute-blocked"}, []string{claim, noneClaim}},
		{"statistics_read_failed", &fakeReadyEmptyStateReads{statsErr: failed, blocked: []*types.BlockedIssue{listable}},
			[]string{claim}, []string{"No open issues"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() error {
				printReadyEmptyState(context.Background(), in, tc.reads)
				return nil
			})
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(out, a) {
					t.Errorf("output has %q:\n%s", a, out)
				}
			}
			for _, asked := range tc.reads.asked {
				if asked.Limit != 0 || asked.MaxRows != 0 || asked.Offset != 0 {
					t.Errorf("blocked read asked with a page or cap: %+v", asked)
				}
			}
		})
	}
}

// TestPrintReadyEmptyStateOffsetPage pins the empty state of an --offset page
// past the last ready issue: the issues before the offset may be ready work,
// so it judges nothing. Its reads would claim both that there are no open
// issues and that a listable issue is blocked; it makes neither claim and does
// not ask bd blocked at all.
func TestPrintReadyEmptyStateOffsetPage(t *testing.T) {
	filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{Offset: 3})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	reads := &fakeReadyEmptyStateReads{
		stats:   &types.Statistics{},
		blocked: []*types.BlockedIssue{{Issue: types.Issue{ID: "bd-b", Status: types.StatusOpen, IssueType: types.TypeTask}}},
	}
	out := captureStdout(t, func() error {
		printReadyEmptyState(context.Background(), readyInput{filter: filter}, reads)
		return nil
	})
	for _, w := range []string{"No ready work at offset 3", "without --offset"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	for _, a := range []string{"No open issues", "blocking dependencies", "Nothing bd ready would list is blocked"} {
		if strings.Contains(out, a) {
			t.Errorf("an --offset page judged the ready set (%q):\n%s", a, out)
		}
	}
	if len(reads.asked) != 0 {
		t.Errorf("an --offset page asked bd blocked: %+v", reads.asked)
	}
}
