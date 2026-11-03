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
		name  string
		src   skewSource
		want  *int64
		count *int64
	}{
		{
			name:  "all edges unresolvable",
			src:   skewSource{issue: issue, deps: nil, depCount: 1},
			want:  ptr64(1),
			count: ptr64(1),
		},
		{
			name: "one of two edges unresolvable",
			src: skewSource{
				issue:    issue,
				deps:     []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount: 2,
			},
			want:  ptr64(1),
			count: ptr64(2),
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
			want:  nil,
			count: ptr64(1),
		},
		{
			name:  "no dependencies at all",
			src:   skewSource{issue: issue, depCount: 0},
			want:  nil,
			count: ptr64(0),
		},
		{
			// A FAILED read and a SHORT read both leave the slice
			// empty. Only the first must stay silent: reporting it
			// would announce a data property on the strength of a
			// query that never ran.
			name:  "dependency read failed, not merely short",
			src:   skewSource{issue: issue, depsErr: errors.New("backend down"), depCount: 3},
			want:  nil,
			count: ptr64(3),
		},
		{
			// Symmetric: a failed COUNT is no count at all, so it is
			// neither published nor compared against the slice. The
			// source returns a value above the row count beside its
			// error, which a delta taken from it would report as
			// unresolvable: only the error gate keeps it silent.
			name: "count read failed",
			src: skewSource{
				issue:       issue,
				deps:        []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount:    3,
				depCountErr: errors.New("backend down"),
			},
			want:  nil,
			count: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, DetailOptions{})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			switch {
			case tc.count == nil && details.DependencyCount != nil:
				t.Errorf("DependencyCount = %d, want unset: its query failed", *details.DependencyCount)
			case tc.count != nil && details.DependencyCount == nil:
				t.Errorf("DependencyCount unset, want %d", *tc.count)
			case tc.count != nil && *details.DependencyCount != *tc.count:
				t.Errorf("DependencyCount = %d, want %d", *details.DependencyCount, *tc.count)
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

// TestBuildIssueDetails_FailedCountsAreUnset pins GH#7563: a Count* method
// that returns an error must leave the matching IssueDetails field unset
// (DependentCount/DependencyCount/CommentCount are *int64 with omitempty for
// exactly this), not published as the zero value Count* also returns on
// error. A published 0 is indistinguishable from an issue that genuinely has
// none, which is already how the UnresolvableDependencies/UnresolvableDependents
// deltas treat a count error - they go silent rather than report a delta
// against a count of 0 - so the raw count was the one field still
// contradicting that rule.
func TestBuildIssueDetails_FailedCountsAreUnset(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	boom := errors.New("simulated count failure")

	t.Run("dependent and dependency counts", func(t *testing.T) {
		src := skewSource{issue: issue, depCountErr: boom, rdepCountErr: boom}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.DependentCount != nil {
			t.Errorf("DependentCount = %d, want unset", *details.DependentCount)
		}
		if details.DependencyCount != nil {
			t.Errorf("DependencyCount = %d, want unset", *details.DependencyCount)
		}
	})

	// The IncludeComments case from the report: a failed count beside a
	// real comment must not publish a comment_count that contradicts the
	// comments it actually returns.
	t.Run("comment count, with comments included", func(t *testing.T) {
		src := skewSource{
			issue:           issue,
			commentCountErr: boom,
			comments:        []*types.Comment{{ID: "c-1", IssueID: "rp-1", Author: "alice", Text: "a real comment"}},
		}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{IncludeComments: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.CommentCount != nil {
			t.Errorf("CommentCount = %d, want unset", *details.CommentCount)
		}
		if len(details.Comments) != 1 {
			t.Errorf("Comments = %v, want the one comment IterComments returned", details.Comments)
		}
	})

	// The default, count-only view: a failed comment count must not
	// announce comments_omitted either, for the same reason the list path's
	// TestHydrateListCommentsMarksNothingWhenCountsAreSkipped already
	// requires (list_comments_test.go) - deriving the flag from a count that
	// means nothing is exactly the bug.
	t.Run("comment count failed, count-only view", func(t *testing.T) {
		src := skewSource{issue: issue, commentCountErr: boom}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.CommentCount != nil {
			t.Errorf("CommentCount = %d, want unset", *details.CommentCount)
		}
		if details.CommentsOmitted != nil {
			t.Errorf("CommentsOmitted = %v, want unset", *details.CommentsOmitted)
		}
	})
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

// TestBuildIssueDetails_FailedCountIsAbsentNotZero pins bd-cyry49 (GH#7563):
// the counts are best effort, so a count whose query failed leaves the view
// standing, but it must be left out rather than published as whatever value
// came back beside the error. A published 0 reads as "none" to every caller
// guarding on the count; an absent key is the only honest "unknown". The
// sources below return a NONZERO value beside most errors, so a fix that only
// stopped the zero from leaking would still fail here.
//
// Key presence is checked on the marshaled JSON, not only on the pointers:
// the contract consumers read is "key absent", and a null would not be it.
func TestBuildIssueDetails_FailedCountIsAbsentNotZero(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1", Title: "count repro", Status: types.StatusOpen}
	boom := errors.New("simulated count failure")
	oneDep := []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)}

	tests := []struct {
		name string
		src  skewSource
		opts DetailOptions

		// nil means the key must be absent.
		dependentCount  *int64
		dependencyCount *int64
		commentCount    *int64
		commentsOmitted bool
		comments        int
		// commentIDs, when set, must match the returned rows in order,
		// both on the struct and in the marshaled comments array.
		commentIDs []string

		dependencies             int
		dependents               int
		unresolvableDependencies *int64
		unresolvableDependents   *int64
	}{
		// AC-1: failed edge counts are absent; successful siblings stay.
		{
			name:            "dependent count failed with zero",
			src:             skewSource{issue: issue, rdepCountErr: boom, depCount: 1, deps: oneDep},
			dependencyCount: ptr64(1),
			commentCount:    ptr64(0),
			dependencies:    1,
		},
		{
			name:            "dependent count failed with nonzero",
			src:             skewSource{issue: issue, rdepCount: 4, rdepCountErr: boom, depCount: 1, deps: oneDep},
			dependencyCount: ptr64(1),
			commentCount:    ptr64(0),
			dependencies:    1,
		},
		{
			name:           "dependency count failed with zero",
			src:            skewSource{issue: issue, depCountErr: boom, rdepCount: 2, deps: oneDep},
			dependentCount: ptr64(2),
			commentCount:   ptr64(0),
			dependencies:   1,
		},
		{
			// The value beside the error exceeds the readable rows, so a
			// delta taken from it would publish unresolvable_dependencies.
			name:           "dependency count failed with nonzero above its rows",
			src:            skewSource{issue: issue, depCount: 5, depCountErr: boom, rdepCount: 2, deps: oneDep},
			dependentCount: ptr64(2),
			commentCount:   ptr64(0),
			dependencies:   1,
		},
		{
			name:         "both edge counts failed",
			src:          skewSource{issue: issue, depCount: 3, depCountErr: boom, rdepCount: 3, rdepCountErr: boom, deps: oneDep},
			commentCount: ptr64(0),
			dependencies: 1,
		},
		{
			// The dependent row survives; the count and the delta against
			// it do not.
			name: "dependent count failed above its rows with dependents included",
			src: skewSource{
				issue: issue, rdepCount: 3, rdepCountErr: boom,
				dependents: []*types.IssueWithDependencyMetadata{depRow("rp-3", types.DepBlocks)},
			},
			opts:            DetailOptions{IncludeDependents: true},
			dependencyCount: ptr64(0),
			commentCount:    ptr64(0),
			dependents:      1,
		},

		// AC-2: a failed comment count in count-only mode leaves both
		// comment_count and comments_omitted out. The positive value
		// beside the error catches a fix that drops the pointer but
		// still derives the marker from the raw value.
		{
			name:            "comment count failed with zero",
			src:             skewSource{issue: issue, commentCountErr: boom},
			dependentCount:  ptr64(0),
			dependencyCount: ptr64(0),
		},
		{
			name:            "comment count failed with positive",
			src:             skewSource{issue: issue, commentCount: 2, commentCountErr: boom},
			dependentCount:  ptr64(0),
			dependencyCount: ptr64(0),
		},

		// AC-3: with the rows requested, a failed comment count leaves the
		// readable comment in place beside no count, rather than beside a
		// contradicting comment_count 0.
		{
			name: "comment count failed with comments included",
			src: skewSource{
				issue: issue, commentCountErr: boom,
				comments: []*types.Comment{{ID: "c-1", IssueID: "rp-1", Author: "alice", Text: "a real comment"}},
			},
			opts:            DetailOptions{IncludeComments: true},
			dependentCount:  ptr64(0),
			dependencyCount: ptr64(0),
			comments:        1,
		},
		{
			// A positive value beside the error must not be published
			// either, and the one readable row is the one that survives.
			name: "comment count failed with nonzero and comments included",
			src: skewSource{
				issue: issue, deps: oneDep, depCount: 1,
				commentCount: 1, commentCountErr: boom,
				comments: []*types.Comment{{ID: "c-1", IssueID: "rp-1", Author: "alice", Text: "first"}},
			},
			opts:            DetailOptions{IncludeComments: true},
			dependentCount:  ptr64(0),
			dependencyCount: ptr64(1),
			comments:        1,
			commentIDs:      []string{"c-1"},
			dependencies:    1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, tc.opts)
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v, want the view despite the failed count", err)
			}
			if details.ID != issue.ID || details.Title != issue.Title {
				t.Errorf("issue body = %q/%q, want %q/%q", details.ID, details.Title, issue.ID, issue.Title)
			}
			b, err := json.Marshal(details)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}

			assertCountKey(t, raw, "dependent_count", details.DependentCount, tc.dependentCount)
			assertCountKey(t, raw, "dependency_count", details.DependencyCount, tc.dependencyCount)
			assertCountKey(t, raw, "comment_count", details.CommentCount, tc.commentCount)
			assertCountKey(t, raw, "unresolvable_dependencies", details.UnresolvableDependencies, tc.unresolvableDependencies)
			assertCountKey(t, raw, "unresolvable_dependents", details.UnresolvableDependents, tc.unresolvableDependents)

			if _, ok := raw["comments_omitted"]; ok != tc.commentsOmitted {
				t.Errorf("comments_omitted present = %v, want %v (%s)", ok, tc.commentsOmitted, b)
			}
			if tc.commentsOmitted && (details.CommentsOmitted == nil || !*details.CommentsOmitted) {
				t.Errorf("CommentsOmitted = %v, want true", details.CommentsOmitted)
			}
			if got := len(details.Comments); got != tc.comments {
				t.Errorf("len(Comments) = %d, want %d", got, tc.comments)
			}
			if tc.commentIDs != nil {
				var got []string
				for _, c := range details.Comments {
					got = append(got, c.ID)
				}
				if !reflect.DeepEqual(got, tc.commentIDs) {
					t.Errorf("comment IDs = %v, want %v", got, tc.commentIDs)
				}
				var marshaled []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(raw["comments"], &marshaled); err != nil {
					t.Fatalf("comments = %s: %v", raw["comments"], err)
				}
				got = got[:0]
				for _, c := range marshaled {
					got = append(got, c.ID)
				}
				if !reflect.DeepEqual(got, tc.commentIDs) {
					t.Errorf("marshaled comment IDs = %v, want %v", got, tc.commentIDs)
				}
			}
			if got := len(details.Dependencies); got != tc.dependencies {
				t.Errorf("len(Dependencies) = %d, want %d", got, tc.dependencies)
			}
			if got := len(details.Dependents); got != tc.dependents {
				t.Errorf("len(Dependents) = %d, want %d", got, tc.dependents)
			}
		})
	}
}

// assertCountKey checks one optional count both ways: the pointer and the
// marshaled key. want nil means the key must be absent from the JSON.
func assertCountKey(t *testing.T, raw map[string]json.RawMessage, key string, got, want *int64) {
	t.Helper()
	v, present := raw[key]
	switch {
	case want == nil && present:
		t.Errorf("%s = %s, want the key absent", key, v)
	case want == nil && got != nil:
		t.Errorf("%s pointer = %d, want unset", key, *got)
	case want != nil && (got == nil || *got != *want):
		t.Errorf("%s pointer = %v, want %d", key, got, *want)
	case want != nil && !present:
		t.Errorf("%s absent, want %d", key, *want)
	case want != nil:
		var n int64
		if err := json.Unmarshal(v, &n); err != nil {
			t.Errorf("%s = %s: %v", key, v, err)
		} else if n != *want {
			t.Errorf("%s = %d, want %d", key, n, *want)
		}
	}
}

func ptr64(v int64) *int64 { return &v }
