package workapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// skewSource is a DetailSource whose COUNTS and ROWS are set independently.
//
// The shared fakeStoreReader derives both from one slice, so it can only ever
// produce a store where they agree — which is exactly the state this test has
// to move away from. A real backend derives them from two different queries:
// the counts are a plain COUNT(*) over the dependency tables, while the rows
// are the issues on the far end and silently omit any id with no row in this
// database (issueops.GetDependenciesWithMetadataInTx's `continue`). A
// cross-repo or `external:` target hits that gap by construction, since
// depends_on_external is the one target column carrying no foreign key into
// issues.
type skewSource struct {
	issue *types.Issue

	deps    []*types.IssueWithDependencyMetadata
	depsErr error

	dependents []*types.IssueWithDependencyMetadata

	depCount     int64
	depCountErr  error
	rdepCount    int64
	rdepCountErr error

	commentCount    int64
	commentCountErr error
	comments        []*types.Comment
}

func (s skewSource) GetIssue(_ context.Context, _ string) (*types.Issue, error) { return s.issue, nil }
func (s skewSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}
func (s skewSource) Labels(_ context.Context, _ string, _ bool) ([]string, error) { return nil, nil }
func (s skewSource) Dependencies(_ context.Context, _ string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps, s.depsErr
}
func (s skewSource) CountDependencies(_ context.Context, _ string, _ bool) (int64, error) {
	return s.depCount, s.depCountErr
}
func (s skewSource) CountDependents(_ context.Context, _ string, _ bool) (int64, error) {
	return s.rdepCount, s.rdepCountErr
}
func (s skewSource) CountComments(_ context.Context, _ string, _ bool) (int64, error) {
	return s.commentCount, s.commentCountErr
}
func (s skewSource) IterDependents(_ context.Context, _ string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter(s.dependents), nil
}
func (s skewSource) IterComments(_ context.Context, _ string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter(s.comments), nil
}

func depRow(id string, t types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{Issue: types.Issue{ID: id}, DependencyType: t}
}

// TestBuildIssueDetails_UnresolvableDependencies pins be-lpi: a dependency
// edge whose target has no row in this database counts toward
// dependency_count and cannot appear in the dependencies slice, and that
// difference must be published rather than left for the caller to infer.
//
// The convoy case that produced the report is the one-edge, zero-row row:
// hq-cv-ftcra carries a single `tracks` edge to
// external:liveop:liveop-kmf, so bd show --json answered
// dependency_count 1 beside dependencies:null and every enumeration path
// agreed the edge did not exist.
func TestBuildIssueDetails_UnresolvableDependencies(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}

	tests := []struct {
		name      string
		src       skewSource
		want      *int64
		wantCount *int64
	}{
		{
			name:      "all edges unresolvable",
			src:       skewSource{issue: issue, deps: nil, depCount: 1},
			want:      ptr64(1),
			wantCount: ptr64(1),
		},
		{
			name: "one of two edges unresolvable",
			src: skewSource{
				issue:    issue,
				deps:     []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount: 2,
			},
			want:      ptr64(1),
			wantCount: ptr64(2),
		},
		{
			// The negative control. A fully local store must be
			// byte-identical to before the fix, or every caller that
			// checks the field starts seeing it on healthy rows.
			name: "fully local, nothing unresolvable",
			src: skewSource{
				issue:    issue,
				deps:     []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount: 1,
			},
			want:      nil,
			wantCount: ptr64(1),
		},
		{
			name:      "no dependencies at all",
			src:       skewSource{issue: issue, depCount: 0},
			want:      nil,
			wantCount: ptr64(0),
		},
		{
			// A FAILED read and a SHORT read both leave the slice
			// empty. Only the first must stay silent: reporting it
			// would announce a data property on the strength of a
			// query that never ran. The count itself succeeded and
			// stays published, 3 included.
			name:      "dependency read failed, not merely short",
			src:       skewSource{issue: issue, depsErr: errors.New("backend down"), depCount: 3},
			want:      nil,
			wantCount: ptr64(3),
		},
		{
			// A failed COUNT publishes nothing at all: neither the
			// value the store returned beside its error nor a delta
			// derived from it. The source answers 5 against one
			// readable row, and 4 unrenderable edges is exactly the
			// conclusion the data cannot support — the 5 is unknown,
			// not evidence (GH#7563).
			name: "count read failed",
			src: skewSource{
				issue:       issue,
				deps:        []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount:    5,
				depCountErr: errors.New("backend down"),
			},
			want:      nil,
			wantCount: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, DetailOptions{})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			switch {
			case tc.wantCount == nil && details.DependencyCount != nil:
				t.Errorf("DependencyCount = %d, want unset: its query failed", *details.DependencyCount)
			case tc.wantCount != nil && (details.DependencyCount == nil || *details.DependencyCount != *tc.wantCount):
				t.Errorf("DependencyCount = %v, want %d", details.DependencyCount, *tc.wantCount)
			}
			switch {
			case tc.want == nil && details.UnresolvableDependencies != nil:
				t.Errorf("UnresolvableDependencies = %d, want unset", *details.UnresolvableDependencies)
			case tc.want != nil && details.UnresolvableDependencies == nil:
				t.Errorf("UnresolvableDependencies unset, want %d", *tc.want)
			case tc.want != nil && *details.UnresolvableDependencies != *tc.want:
				t.Errorf("UnresolvableDependencies = %d, want %d", *details.UnresolvableDependencies, *tc.want)
			}
		})
	}
}

// TestBuildIssueDetails_UnresolvableDependents pins the inbound half, and in
// particular that it stays silent in count-only mode. Dependents is nil by
// design without IncludeDependents (be-ijck6q), so a delta taken against it
// there would report every dependent an issue has as unresolvable.
func TestBuildIssueDetails_UnresolvableDependents(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	src := skewSource{issue: issue, dependents: nil, rdepCount: 2}

	countOnly, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails count-only: %v", err)
	}
	if countOnly.UnresolvableDependents != nil {
		t.Errorf("count-only UnresolvableDependents = %d, want unset — the rows were never fetched",
			*countOnly.UnresolvableDependents)
	}

	included, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{IncludeDependents: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails include-dependents: %v", err)
	}
	if included.UnresolvableDependents == nil || *included.UnresolvableDependents != 2 {
		t.Errorf("UnresolvableDependents = %v, want 2", included.UnresolvableDependents)
	}

	// Negative control on the same path: every dependent resolvable.
	local := skewSource{
		issue:      issue,
		dependents: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
		rdepCount:  1,
	}
	got, err := BuildIssueDetails(ctx, local, issue, false, DetailOptions{IncludeDependents: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails local: %v", err)
	}
	if got.UnresolvableDependents != nil {
		t.Errorf("UnresolvableDependents = %d on a fully local store, want unset", *got.UnresolvableDependents)
	}
}

// mutatingSource models a write landing BETWEEN the two reads
// BuildIssueDetails makes: whichever of Dependencies/CountDependencies runs
// first observes one edge, and whichever runs second observes two.
//
// This is what makes the ordering testable at all. A source returning fixed
// values cannot distinguish the two orders — the first version of this test
// used one, and reverting the fix under it still passed, which is a test that
// pins arithmetic while claiming to pin an order.
type mutatingSource struct {
	issue  *types.Issue
	edges  int
	before int
}

func (m *mutatingSource) observe() int {
	if m.before == 0 {
		m.before = 1
		return m.edges // the first reader sees the pre-write state
	}
	return m.edges + 1 // by the second read, an edge has been added
}

func (m *mutatingSource) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return m.issue, nil
}
func (m *mutatingSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}
func (m *mutatingSource) Labels(_ context.Context, _ string, _ bool) ([]string, error) {
	return nil, nil
}
func (m *mutatingSource) Dependencies(_ context.Context, _ string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	n := m.observe()
	out := make([]*types.IssueWithDependencyMetadata, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, depRow(fmt.Sprintf("rp-%d", i+2), types.DepBlocks))
	}
	return out, nil
}
func (m *mutatingSource) CountDependencies(_ context.Context, _ string, _ bool) (int64, error) {
	return int64(m.observe()), nil
}
func (m *mutatingSource) CountDependents(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (m *mutatingSource) CountComments(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (m *mutatingSource) IterDependents(_ context.Context, _ string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter[types.IssueWithDependencyMetadata](nil), nil
}
func (m *mutatingSource) IterComments(_ context.Context, _ string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}

// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable pins the
// ORDER of the two reads, which is the only defence the store-backed source
// has against a write landing between them. (The UOW source runs the whole
// build inside one read transaction and needs none.)
//
// The count is read FIRST, so an edge added in the window leaves it
// stale-LOW: the difference goes negative and nothing is reported. Read
// afterwards it would be stale-HIGH, and a freshly added, perfectly ordinary
// local edge would be published as unresolvable — a false claim about the
// data, which is the expensive direction. Found by a codex review of the
// first cut of this change.
//
// The source above mutates between calls, so swapping the two reads in
// BuildIssueDetails turns this red. That is the control; without it the case
// passes under either order.
func TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	src := &mutatingSource{issue: issue, edges: 1}

	details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if details.UnresolvableDependencies != nil {
		t.Errorf("UnresolvableDependencies = %d, want unset — a count read before a concurrent add is stale-low, not evidence of an unrenderable edge; are the two reads still in count-then-listing order?",
			*details.UnresolvableDependencies)
	}
}

func ptr64(v int64) *int64 { return &v }

func ptrBool(v bool) *bool { return &v }

// commentIDs / dependentIDs collapse the collected rows to their ids so a
// case can assert which rows survived without pinning their full shape.
func commentIDs(comments []*types.Comment) []string {
	if len(comments) == 0 {
		return nil
	}
	ids := make([]string, len(comments))
	for i, c := range comments {
		ids[i] = c.ID
	}
	return ids
}

func dependentIDs(deps []*types.IssueWithDependencyMetadata) []string {
	if len(deps) == 0 {
		return nil
	}
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.Issue.ID
	}
	return ids
}

func marshalDetails(t *testing.T, details *types.IssueDetails) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	return raw
}

// checkInt64Key asserts both the pointer and the marshaled key. A want of nil
// means the value must be wholly absent: a present `null` or a present `0` is
// the defect this pins (GH#7563) — an unreadable count dressed up as data,
// indistinguishable from "counted none".
func checkInt64Key(t *testing.T, raw map[string]json.RawMessage, key string, got, want *int64) {
	t.Helper()
	_, present := raw[key]
	switch {
	case want == nil:
		if got != nil {
			t.Errorf("%s = %d, want unset: its query failed", key, *got)
		}
		if present {
			t.Errorf("%s present in JSON as %s, want the key absent", key, raw[key])
		}
	default:
		if got == nil || *got != *want {
			t.Errorf("%s = %v, want %d", key, got, *want)
		}
		if !present {
			t.Errorf("%s absent from JSON, want %d", key, *want)
		}
	}
}

func checkBoolKey(t *testing.T, raw map[string]json.RawMessage, key string, got, want *bool) {
	t.Helper()
	_, present := raw[key]
	switch {
	case want == nil:
		if got != nil {
			t.Errorf("%s = %v, want unset", key, *got)
		}
		if present {
			t.Errorf("%s present in JSON as %s, want the key absent", key, raw[key])
		}
	default:
		if got == nil || *got != *want {
			t.Errorf("%s = %v, want %v", key, got, *want)
		}
		if !present {
			t.Errorf("%s absent from JSON, want %v", key, *want)
		}
	}
}

// TestBuildIssueDetails_FailedCountIsAbsentNotZero pins bd-cyry49 (GH#7563):
// a count whose query failed is left unset rather than published as the value
// the store returned beside its error — usually 0 — while a count that
// succeeded is published as-is, including a genuine zero. Absence is the only
// honest signal the optional fields have: an absent key means unknown, a
// present 0 means the query ran and counted none. That is already the rule
// the derived unresolvable_* fields follow and the list path applies to
// comments_omitted; the detail path published the meaningless zero instead.
func TestBuildIssueDetails_FailedCountIsAbsentNotZero(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1", Title: "count repro", Status: types.StatusOpen}
	boom := errors.New("simulated count failure")
	oneDep := []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)}
	oneDependent := []*types.IssueWithDependencyMetadata{depRow("rp-3", types.DepBlocks)}
	twoComments := []*types.Comment{
		{ID: "c-1", IssueID: "rp-1", Author: "alice", Text: "first"},
		{ID: "c-2", IssueID: "rp-1", Author: "bob", Text: "second"},
	}

	tests := []struct {
		name string
		src  skewSource
		opts DetailOptions

		wantDependent         *int64
		wantDependency        *int64
		wantComment           *int64
		wantCommentsOmitted   *bool
		wantUnresolvableDeps  *int64
		wantUnresolvableRdeps *int64
		wantCommentIDs        []string
		wantDependentIDs      []string
	}{
		// AC-1: a failed edge count is omitted; successful siblings stay.
		{
			name: "ac1_dependent_count_failed_returns_zero",
			src: skewSource{
				issue: issue, deps: oneDep, depCount: 1,
				rdepCount: 0, rdepCountErr: boom,
			},
			wantDependency: ptr64(1),
			wantComment:    ptr64(0),
		},
		{
			name: "ac1_dependent_count_failed_returns_nonzero",
			src: skewSource{
				issue: issue, deps: oneDep, depCount: 1,
				rdepCount: 7, rdepCountErr: boom,
			},
			wantDependency: ptr64(1),
			wantComment:    ptr64(0),
		},
		{
			name: "ac1_dependency_count_failed_returns_zero",
			src: skewSource{
				issue: issue, deps: oneDep,
				depCount: 0, depCountErr: boom, rdepCount: 2,
			},
			wantDependent: ptr64(2),
			wantComment:   ptr64(0),
		},
		{
			// The failed count answers 5 against one readable row. The
			// 4-edge difference must not be reported as unresolvable:
			// the 5 is unknown, not evidence of unrenderable edges.
			name: "ac1_dependency_count_failed_greater_than_rows",
			src: skewSource{
				issue: issue, deps: oneDep,
				depCount: 5, depCountErr: boom, rdepCount: 2,
			},
			wantDependent: ptr64(2),
			wantComment:   ptr64(0),
		},
		{
			name: "ac1_both_edge_counts_failed",
			src: skewSource{
				issue: issue, deps: oneDep,
				depCount: 9, depCountErr: boom,
				rdepCount: 8, rdepCountErr: boom,
			},
			wantComment: ptr64(0),
		},
		{
			// The readable dependent row survives; only its count is
			// withheld, and the 9-over-1 difference stays unreported.
			name: "ac1_dependent_count_failed_under_include_dependents",
			src: skewSource{
				issue: issue, deps: oneDep, depCount: 1,
				dependents: oneDependent, rdepCount: 9, rdepCountErr: boom,
			},
			opts:             DetailOptions{IncludeDependents: true},
			wantDependency:   ptr64(1),
			wantComment:      ptr64(0),
			wantDependentIDs: []string{"rp-3"},
		},
		// AC-3: with the rows requested, a failed comment count leaves
		// the readable comment in place beside no count at all.
		{
			name: "ac3_comment_count_failed_with_readable_comment",
			src: skewSource{
				issue: issue, deps: oneDep, depCount: 1,
				commentCount: 1, commentCountErr: boom, comments: twoComments[:1],
			},
			opts:           DetailOptions{IncludeComments: true},
			wantDependent:  ptr64(0),
			wantDependency: ptr64(1),
			wantCommentIDs: []string{"c-1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, tc.opts)
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			raw := marshalDetails(t, details)
			checkInt64Key(t, raw, "dependent_count", details.DependentCount, tc.wantDependent)
			checkInt64Key(t, raw, "dependency_count", details.DependencyCount, tc.wantDependency)
			checkInt64Key(t, raw, "comment_count", details.CommentCount, tc.wantComment)
			checkBoolKey(t, raw, "comments_omitted", details.CommentsOmitted, tc.wantCommentsOmitted)
			checkInt64Key(t, raw, "unresolvable_dependencies", details.UnresolvableDependencies, tc.wantUnresolvableDeps)
			checkInt64Key(t, raw, "unresolvable_dependents", details.UnresolvableDependents, tc.wantUnresolvableRdeps)
			if got := commentIDs(details.Comments); !reflect.DeepEqual(got, tc.wantCommentIDs) {
				t.Errorf("comments ids = %v, want %v", got, tc.wantCommentIDs)
			}
			if got := dependentIDs(details.Dependents); !reflect.DeepEqual(got, tc.wantDependentIDs) {
				t.Errorf("dependents ids = %v, want %v", got, tc.wantDependentIDs)
			}
		})
	}
}
