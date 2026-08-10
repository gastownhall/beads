package workapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// ---------------------------------------------------------------------------
// Fixture: one dataset, read by both adapters' fakes.
// ---------------------------------------------------------------------------

type detailFixture struct {
	issues     map[string]*types.Issue
	wisps      map[string]*types.Issue
	labels     map[string][]string
	deps       map[string][]*types.IssueWithDependencyMetadata
	dependents map[string][]*types.IssueWithDependencyMetadata
	comments   map[string][]*types.Comment
}

func newDetailFixture() *detailFixture {
	heavy := strings.Repeat("x", 1024)

	return &detailFixture{
		issues: map[string]*types.Issue{
			"bd-1":    {ID: "bd-1", Title: "Durable issue", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 1},
			"bd-epic": {ID: "bd-epic", Title: "Epic", IssueType: types.TypeEpic, Status: types.StatusOpen, Priority: 1},
			"bd-chat": {ID: "bd-chat", Title: "Has comments", IssueType: types.TypeTask, Status: types.StatusOpen},
		},
		wisps: map[string]*types.Issue{
			"bd-w1": {ID: "bd-w1", Title: "Ephemeral wisp", IssueType: types.TypeTask, Status: types.StatusOpen},
		},
		labels: map[string][]string{
			"bd-1":  {"alpha", "beta"},
			"bd-w1": {"wisp-only"},
		},
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-1": {
				{Issue: types.Issue{ID: "bd-parent", Title: "Parent"}, DependencyType: types.DepParentChild},
				{Issue: types.Issue{ID: "bd-blocker", Title: "Blocker"}, DependencyType: types.DepBlocks},
			},
			"bd-w1": {
				{Issue: types.Issue{ID: "bd-1", Title: "Durable issue"}, DependencyType: types.DepBlocks},
			},
		},
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			"bd-1": {
				{
					Issue: types.Issue{
						ID: "bd-spoke", Title: "Spoke", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
						Description: heavy, Design: heavy, Notes: heavy, AcceptanceCriteria: heavy,
					},
					DependencyType: types.DepBlocks,
				},
			},
			"bd-epic": {
				{Issue: types.Issue{ID: "bd-kid1", Title: "Kid 1", Status: types.StatusClosed}, DependencyType: types.DepParentChild},
				{Issue: types.Issue{ID: "bd-kid2", Title: "Kid 2", Status: types.StatusOpen}, DependencyType: types.DepParentChild},
				{Issue: types.Issue{ID: "bd-other", Title: "Not a child", Status: types.StatusOpen}, DependencyType: types.DepBlocks},
			},
			"bd-w1": {
				{Issue: types.Issue{ID: "bd-wkid", Title: "Wisp kid", Status: types.StatusOpen}, DependencyType: types.DepParentChild},
			},
		},
		comments: map[string][]*types.Comment{
			"bd-chat": {
				{ID: "c1", IssueID: "bd-chat", Author: "alice", Text: "first"},
				{ID: "c2", IssueID: "bd-chat", Author: "bob", Text: "second"},
			},
			"bd-w1": {
				{ID: "c3", IssueID: "bd-w1", Author: "carol", Text: "wisp comment"},
			},
		},
	}
}

func (f *detailFixture) isWisp(id string) bool {
	_, ok := f.wisps[id]
	return ok
}

// ---------------------------------------------------------------------------
// Store-shaped fake: mirrors how the Dolt store behaves. GetIssue falls back
// to the wisp table itself and reports misses as a wrapped storage.ErrNotFound;
// every other getter resolves wisp routing internally, so no caller passes a
// table selector in.
// ---------------------------------------------------------------------------

type fakeStoreReader struct {
	fx      *detailFixture
	hardErr error
}

func (f fakeStoreReader) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if f.hardErr != nil {
		return nil, f.hardErr
	}
	if issue, ok := f.fx.issues[id]; ok {
		return issue, nil
	}
	if wisp, ok := f.fx.wisps[id]; ok {
		return wisp, nil
	}
	return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
}

func (f fakeStoreReader) GetLabels(_ context.Context, id string) ([]string, error) {
	return f.fx.labels[id], nil
}

func (f fakeStoreReader) GetDependenciesWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return f.fx.deps[id], nil
}

func (f fakeStoreReader) CountDependencies(_ context.Context, id string) (int64, error) {
	return int64(len(f.fx.deps[id])), nil
}

func (f fakeStoreReader) CountDependents(_ context.Context, id string) (int64, error) {
	return int64(len(f.fx.dependents[id])), nil
}

func (f fakeStoreReader) CountIssueComments(_ context.Context, id string) (int64, error) {
	return int64(len(f.fx.comments[id])), nil
}

func (f fakeStoreReader) IterDependentsWithMetadata(_ context.Context, id string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter(f.fx.dependents[id]), nil
}

func (f fakeStoreReader) IterIssueComments(_ context.Context, id string) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter(f.fx.comments[id]), nil
}

// ---------------------------------------------------------------------------
// Use-case-shaped fakes: mirror the domain seam. Lookups are per-table and a
// miss surfaces as db.issueSQLRepositoryImpl.Get's raw sql.ErrNoRows, wrapped
// by issueUseCaseImpl.get - never storage.ErrNotFound.
// ---------------------------------------------------------------------------

type fakeIssueUC struct {
	fx *detailFixture

	hardErr error // both lookups fail: the whole backend is down
	// issueErr fails only the issue lookup, leaving the wisp table healthy.
	// That asymmetry is the one a fall-through resolver gets wrong.
	issueErr error
	nilNil   bool // report a miss as (nil, nil) instead of an error
}

func (f fakeIssueUC) miss(id string) (*types.Issue, error) {
	if f.nilNil {
		return nil, nil
	}
	return nil, fmt.Errorf("get %s: %w", id, sql.ErrNoRows)
}

func (f fakeIssueUC) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if f.hardErr != nil {
		return nil, f.hardErr
	}
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	if issue, ok := f.fx.issues[id]; ok {
		return issue, nil
	}
	return f.miss(id)
}

func (f fakeIssueUC) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	if f.hardErr != nil {
		return nil, f.hardErr
	}
	if wisp, ok := f.fx.wisps[id]; ok {
		return wisp, nil
	}
	return f.miss(id)
}

type fakeLabelUC struct{ fx *detailFixture }

func (f fakeLabelUC) GetLabels(_ context.Context, id string) ([]string, error) {
	if f.fx.isWisp(id) {
		return nil, fmt.Errorf("GetLabels called for wisp %s", id)
	}
	return f.fx.labels[id], nil
}

func (f fakeLabelUC) GetWispLabels(_ context.Context, id string) ([]string, error) {
	if !f.fx.isWisp(id) {
		return nil, fmt.Errorf("GetWispLabels called for durable issue %s", id)
	}
	return f.fx.labels[id], nil
}

type fakeDepUC struct{ fx *detailFixture }

func (f fakeDepUC) rows(id string, filter domain.DepListFilter) []*types.IssueWithDependencyMetadata {
	if filter.Direction == domain.DepDirectionIn {
		return f.fx.dependents[id]
	}
	return f.fx.deps[id]
}

func (f fakeDepUC) ListWithIssueMetadata(_ context.Context, id string, filter domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	if f.fx.isWisp(id) {
		return nil, fmt.Errorf("ListWithIssueMetadata called for wisp %s", id)
	}
	return f.rows(id, filter), nil
}

func (f fakeDepUC) ListWispWithIssueMetadata(_ context.Context, id string, filter domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	if !f.fx.isWisp(id) {
		return nil, fmt.Errorf("ListWispWithIssueMetadata called for durable issue %s", id)
	}
	return f.rows(id, filter), nil
}

func (f fakeDepUC) IterWithIssueMetadata(ctx context.Context, id string, filter domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	rows, err := f.ListWithIssueMetadata(ctx, id, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(rows), nil
}

func (f fakeDepUC) IterWispWithIssueMetadata(ctx context.Context, id string, filter domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	rows, err := f.ListWispWithIssueMetadata(ctx, id, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(rows), nil
}

func (f fakeDepUC) CountByIssueID(ctx context.Context, id string, filter domain.DepListFilter) (int64, error) {
	rows, err := f.ListWithIssueMetadata(ctx, id, filter)
	return int64(len(rows)), err
}

func (f fakeDepUC) CountByWispID(ctx context.Context, id string, filter domain.DepListFilter) (int64, error) {
	rows, err := f.ListWispWithIssueMetadata(ctx, id, filter)
	return int64(len(rows)), err
}

type fakeCommentUC struct{ fx *detailFixture }

func (f fakeCommentUC) CountCommentsForIssue(_ context.Context, id string) (int64, error) {
	if f.fx.isWisp(id) {
		return 0, fmt.Errorf("CountCommentsForIssue called for wisp %s", id)
	}
	return int64(len(f.fx.comments[id])), nil
}

func (f fakeCommentUC) CountCommentsForWisp(_ context.Context, id string) (int64, error) {
	if !f.fx.isWisp(id) {
		return 0, fmt.Errorf("CountCommentsForWisp called for durable issue %s", id)
	}
	return int64(len(f.fx.comments[id])), nil
}

func (f fakeCommentUC) IterCommentsForIssue(ctx context.Context, id string) (storage.Iter[types.Comment], error) {
	if _, err := f.CountCommentsForIssue(ctx, id); err != nil {
		return nil, err
	}
	return storage.NewSliceIter(f.fx.comments[id]), nil
}

func (f fakeCommentUC) IterCommentsForWisp(ctx context.Context, id string) (storage.Iter[types.Comment], error) {
	if _, err := f.CountCommentsForWisp(ctx, id); err != nil {
		return nil, err
	}
	return storage.NewSliceIter(f.fx.comments[id]), nil
}

func fixtureSources(fx *detailFixture) (store, useCase DetailSource) {
	return NewStoreDetailSource(fakeStoreReader{fx: fx}),
		newUseCaseDetailSource(fakeIssueUC{fx: fx}, fakeLabelUC{fx: fx}, fakeDepUC{fx: fx}, fakeCommentUC{fx: fx})
}

// ---------------------------------------------------------------------------
// Not-found normalization.
// ---------------------------------------------------------------------------

// TestGetIssueOrWispNormalizesNotFound pins the reason detail lookup is
// shared at all. The store seam reports a miss as a wrapped
// storage.ErrNotFound; the domain seam reports it as a wrapped sql.ErrNoRows
// (db.issueSQLRepositoryImpl.Get) or, for a repository that returns an empty
// result without an error, as a nil issue with a nil error. Callers get one
// sentinel regardless.
func TestGetIssueOrWispNormalizesNotFound(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()

	cases := []struct {
		name string
		src  DetailSource
	}{
		{"store seam wraps storage.ErrNotFound", NewStoreDetailSource(fakeStoreReader{fx: fx})},
		{"domain seam wraps sql.ErrNoRows", newUseCaseDetailSource(fakeIssueUC{fx: fx}, fakeLabelUC{fx: fx}, fakeDepUC{fx: fx}, fakeCommentUC{fx: fx})},
		{"domain seam returns nil issue with nil error", newUseCaseDetailSource(fakeIssueUC{fx: fx, nilNil: true}, fakeLabelUC{fx: fx}, fakeDepUC{fx: fx}, fakeCommentUC{fx: fx})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, isWisp, err := GetIssueOrWisp(ctx, tc.src, "bd-missing")
			if issue != nil {
				t.Errorf("issue = %v, want nil", issue)
			}
			if isWisp {
				t.Error("isWisp = true, want false")
			}
			if !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("err = %v, want storage.ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), "bd-missing") {
				t.Errorf("err = %q, want the id in the message", err)
			}
		})
	}
}

// TestGetIssueOrWispKeepsHardErrors is the other half of the fix: a backend
// failure must not decay into not-found, and it must short-circuit rather than
// be retried against the wisp table.
//
// The discriminating case is the last one - the issue lookup fails while the
// id happens to exist as a wisp. A resolver that falls through to the wisp
// table on any issue-lookup failure answers that with (wisp, true, nil): a
// dead backend reported as a successful read of a different table. Failing
// both lookups does not test the ordering at all, because fall-through reaches
// the same hard error by the longer route.
func TestGetIssueOrWispKeepsHardErrors(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	boom := errors.New("connection reset by peer")

	cases := []struct {
		name string
		id   string
		src  DetailSource
	}{
		// The store resolves both tables inside its own GetIssue, so this
		// adapter's GetWisp is a dead end and there is no fall-through to
		// suppress. The case is here to pin that the store's hard error still
		// reaches the caller unwrapped, not to test ordering.
		{"store", "bd-1", NewStoreDetailSource(fakeStoreReader{fx: fx, hardErr: boom})},
		{"use case, whole backend down", "bd-1",
			newUseCaseDetailSource(fakeIssueUC{fx: fx, hardErr: boom}, fakeLabelUC{fx: fx}, fakeDepUC{fx: fx}, fakeCommentUC{fx: fx})},
		{"use case, issue lookup fails while the id exists as a wisp", "bd-w1",
			newUseCaseDetailSource(fakeIssueUC{fx: fx, issueErr: boom}, fakeLabelUC{fx: fx}, fakeDepUC{fx: fx}, fakeCommentUC{fx: fx})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, isWisp, err := GetIssueOrWisp(ctx, tc.src, tc.id)
			if err == nil {
				t.Fatalf("issue = %+v, isWisp = %v, err = nil: a failed issue lookup must not answer with a row", issue, isWisp)
			}
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the backend error", err)
			}
			if errors.Is(err, storage.ErrNotFound) {
				t.Error("backend failure reported as not-found")
			}
			if issue != nil {
				t.Errorf("issue = %+v, want nil alongside the error", issue)
			}
		})
	}
}

func TestGetIssueOrWispResolvesWisps(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, useCase := fixtureSources(fx)

	// The store resolves the wisp table inside GetIssue, so it answers with
	// isWisp=false; the domain seam has to ask the wisp table by name. The
	// axis is an artifact of the seam, not of the issue.
	issue, isWisp, err := GetIssueOrWisp(ctx, store, "bd-w1")
	if err != nil || issue == nil || issue.ID != "bd-w1" {
		t.Fatalf("store: issue=%v isWisp=%v err=%v", issue, isWisp, err)
	}
	if isWisp {
		t.Error("store: isWisp = true, want false (the store routes internally)")
	}

	issue, isWisp, err = GetIssueOrWisp(ctx, useCase, "bd-w1")
	if err != nil || issue == nil || issue.ID != "bd-w1" {
		t.Fatalf("use case: issue=%v isWisp=%v err=%v", issue, isWisp, err)
	}
	if !isWisp {
		t.Error("use case: isWisp = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Conformance.
// ---------------------------------------------------------------------------

// TestDetailSourceConformance runs the same lookups through both adapters and
// requires identical details. The adapters differ underneath - one seam
// routes wisps internally, the other by method name - and that difference must
// not reach the response.
func TestDetailSourceConformance(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, useCase := fixtureSources(fx)

	ids := []string{"bd-1", "bd-epic", "bd-chat", "bd-w1"}
	optionSets := []struct {
		name string
		opts DetailOptions
	}{
		{"counts only", DetailOptions{}},
		{"with dependents", DetailOptions{IncludeDependents: true}},
		{"with comments", DetailOptions{IncludeComments: true}},
		{"with both", DetailOptions{IncludeDependents: true, IncludeComments: true}},
	}

	for _, id := range ids {
		for _, optSet := range optionSets {
			t.Run(id+"/"+optSet.name, func(t *testing.T) {
				storeIssue, storeIsWisp, err := GetIssueOrWisp(ctx, store, id)
				if err != nil {
					t.Fatalf("store lookup: %v", err)
				}
				ucIssue, ucIsWisp, err := GetIssueOrWisp(ctx, useCase, id)
				if err != nil {
					t.Fatalf("use case lookup: %v", err)
				}
				if !reflect.DeepEqual(storeIssue, ucIssue) {
					t.Fatalf("issue differs:\nstore    = %+v\nuse case = %+v", storeIssue, ucIssue)
				}

				storeDetails, err := BuildIssueDetails(ctx, store, storeIssue, storeIsWisp, optSet.opts)
				if err != nil {
					t.Fatalf("store details: %v", err)
				}
				ucDetails, err := BuildIssueDetails(ctx, useCase, ucIssue, ucIsWisp, optSet.opts)
				if err != nil {
					t.Fatalf("use case details: %v", err)
				}
				if !reflect.DeepEqual(storeDetails, ucDetails) {
					t.Fatalf("details differ:\nstore    = %+v\nuse case = %+v", storeDetails, ucDetails)
				}
			})
		}
	}

	t.Run("missing id/both report the sentinel", func(t *testing.T) {
		_, _, storeErr := GetIssueOrWisp(ctx, store, "bd-missing")
		_, _, ucErr := GetIssueOrWisp(ctx, useCase, "bd-missing")
		if !errors.Is(storeErr, storage.ErrNotFound) || !errors.Is(ucErr, storage.ErrNotFound) {
			t.Fatalf("store err = %v, use case err = %v; both want storage.ErrNotFound", storeErr, ucErr)
		}
		if storeErr.Error() != ucErr.Error() {
			t.Errorf("messages differ: store %q, use case %q", storeErr, ucErr)
		}
	})
}

// ---------------------------------------------------------------------------
// Assembly.
// ---------------------------------------------------------------------------

func TestBuildIssueDetailsCountsAndParent(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, _ := fixtureSources(fx)

	details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if got := deref(t, details.DependencyCount); got != 2 {
		t.Errorf("dependency_count = %d, want 2", got)
	}
	if got := deref(t, details.DependentCount); got != 1 {
		t.Errorf("dependent_count = %d, want 1", got)
	}
	if got := deref(t, details.CommentCount); got != 0 {
		t.Errorf("comment_count = %d, want 0", got)
	}
	if !reflect.DeepEqual(details.Labels, []string{"alpha", "beta"}) {
		t.Errorf("labels = %v", details.Labels)
	}
	if details.Parent == nil || *details.Parent != "bd-parent" {
		t.Errorf("parent = %v, want bd-parent", details.Parent)
	}
	if details.Dependents != nil {
		t.Errorf("dependents populated without IncludeDependents: %v", details.Dependents)
	}
	if details.Comments != nil {
		t.Errorf("comments populated without IncludeComments: %v", details.Comments)
	}
	if details.CommentsOmitted != nil {
		t.Errorf("comments_omitted set with a zero count: %v", *details.CommentsOmitted)
	}
}

// TestBuildIssueDetailsCommentsOmitted covers ga-clgh: a nonzero count with no
// rows has to say so, or a caller reading `.comments` reads absence as "none".
func TestBuildIssueDetailsCommentsOmitted(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, _ := fixtureSources(fx)

	omitted, err := BuildIssueDetails(ctx, store, fx.issues["bd-chat"], false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if omitted.CommentsOmitted == nil || !*omitted.CommentsOmitted {
		t.Errorf("comments_omitted = %v, want true", omitted.CommentsOmitted)
	}
	if omitted.Comments != nil {
		t.Errorf("comments should stay nil in count-only mode: %v", omitted.Comments)
	}

	included, err := BuildIssueDetails(ctx, store, fx.issues["bd-chat"], false, DetailOptions{IncludeComments: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if included.CommentsOmitted != nil {
		t.Errorf("comments_omitted set alongside a populated slice: %v", *included.CommentsOmitted)
	}
	if len(included.Comments) != 2 || included.Comments[0].Text != "first" {
		t.Fatalf("comments = %+v", included.Comments)
	}
}

// TestBuildIssueDetailsShallowDependents is the regression guard for
// be-4d36f2: the dependent rows must not carry the heavy free-form fields.
// On hub beads with thousands of dependents that cost 5-13 GB.
func TestBuildIssueDetailsShallowDependents(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, _ := fixtureSources(fx)

	details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{IncludeDependents: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if len(details.Dependents) != 1 {
		t.Fatalf("dependents = %d, want 1", len(details.Dependents))
	}
	got := details.Dependents[0]

	if got.ID != "bd-spoke" || got.Title != "Spoke" || got.Status != types.StatusOpen ||
		got.IssueType != types.TypeTask || got.Priority != 2 || got.DependencyType != types.DepBlocks {
		t.Errorf("identity fields not preserved: %+v", got)
	}
	if got.Description != "" || got.Design != "" || got.Notes != "" || got.AcceptanceCriteria != "" {
		t.Errorf("heavy fields not stripped: %+v", got)
	}
}

// TestBuildIssueDetailsBriefDeps covers the dependency side of be-4d36f2. The
// shared fixture gives dependency rows no free-form text, so no existing case
// could observe them carrying it; this one plants the heavy fields first.
func TestBuildIssueDetailsBriefDeps(t *testing.T) {
	ctx := context.Background()
	heavy := strings.Repeat("x", 1024)

	newFixture := func() *detailFixture {
		fx := newDetailFixture()
		fx.deps["bd-1"] = []*types.IssueWithDependencyMetadata{{
			Issue: types.Issue{
				ID: "bd-blocker", Title: "Blocker", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Description: heavy, Design: heavy, Notes: heavy, AcceptanceCriteria: heavy,
			},
			DependencyType: types.DepBlocks,
		}}
		return fx
	}

	findDep := func(t *testing.T, details *types.IssueDetails) *types.IssueWithDependencyMetadata {
		t.Helper()
		for _, dep := range details.Dependencies {
			if dep.ID == "bd-blocker" {
				return dep
			}
		}
		t.Fatalf("dependency bd-blocker missing from %d rows", len(details.Dependencies))
		return nil
	}

	t.Run("default keeps the full body", func(t *testing.T) {
		fx := newFixture()
		store, _ := fixtureSources(fx)
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if got := findDep(t, details); got.Description != heavy || got.Notes != heavy {
			t.Errorf("default must not drop dependency text: description=%d notes=%d",
				len(got.Description), len(got.Notes))
		}
	})

	t.Run("BriefDeps strips text and keeps identity", func(t *testing.T) {
		fx := newFixture()
		store, _ := fixtureSources(fx)
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{BriefDeps: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		got := findDep(t, details)
		if got.Title != "Blocker" || got.Status != types.StatusOpen ||
			got.IssueType != types.TypeTask || got.Priority != 2 || got.DependencyType != types.DepBlocks {
			t.Errorf("identity fields not preserved: %+v", got)
		}
		if got.Description != "" || got.Design != "" || got.Notes != "" || got.AcceptanceCriteria != "" {
			t.Errorf("heavy fields not stripped: %+v", got)
		}
	})

	t.Run("BriefDeps does not mutate the source rows", func(t *testing.T) {
		fx := newFixture()
		store, _ := fixtureSources(fx)
		if _, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{BriefDeps: true}); err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if got := fx.deps["bd-1"][0]; got.Notes != heavy || got.Description != heavy {
			t.Errorf("source row was shallowed in place: notes=%d description=%d",
				len(got.Notes), len(got.Description))
		}
	})

	t.Run("BriefDeps still resolves Parent", func(t *testing.T) {
		fx := newFixture()
		fx.deps["bd-1"] = append(fx.deps["bd-1"], &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: "bd-parent", Title: "Parent", Notes: heavy},
			DependencyType: types.DepParentChild,
		})
		store, _ := fixtureSources(fx)
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{BriefDeps: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.Parent == nil || *details.Parent != "bd-parent" {
			t.Errorf("parent = %v, want bd-parent", details.Parent)
		}
	})
}

func TestBuildIssueDetailsEpicProgress(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	store, _ := fixtureSources(fx)

	t.Run("counts only parent-child edges", func(t *testing.T) {
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-epic"], false, DetailOptions{IncludeDependents: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.EpicTotalChildren == nil || *details.EpicTotalChildren != 2 {
			t.Errorf("epic_total_children = %v, want 2", details.EpicTotalChildren)
		}
		if details.EpicClosedChildren == nil || *details.EpicClosedChildren != 1 {
			t.Errorf("epic_closed_children = %v, want 1", details.EpicClosedChildren)
		}
		if details.EpicCloseable == nil || *details.EpicCloseable {
			t.Errorf("epic_closeable = %v, want false", details.EpicCloseable)
		}
	})

	t.Run("absent without IncludeDependents", func(t *testing.T) {
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-epic"], false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.EpicTotalChildren != nil {
			t.Errorf("epic progress derived without the dependent rows: %v", *details.EpicTotalChildren)
		}
	})

	t.Run("absent for non-epics", func(t *testing.T) {
		details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{IncludeDependents: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.EpicTotalChildren != nil {
			t.Errorf("epic progress on a task: %v", *details.EpicTotalChildren)
		}
	})
}

// TestBuildIssueDetailsRowStreamErrors: the opt-in row streams are not best
// effort. A caller that asked for the rows must not be handed a short list
// that looks complete.
func TestBuildIssueDetailsRowStreamErrors(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	boom := errors.New("iterator exploded")
	src := failingIterSource{DetailSource: NewStoreDetailSource(fakeStoreReader{fx: fx}), err: boom}

	t.Run("dependents", func(t *testing.T) {
		_, err := BuildIssueDetails(ctx, src, fx.issues["bd-1"], false, DetailOptions{IncludeDependents: true})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the iterator error", err)
		}
		if !strings.Contains(err.Error(), "iter dependents bd-1") {
			t.Errorf("err = %q, want the id and the operation", err)
		}
	})

	t.Run("comments", func(t *testing.T) {
		_, err := BuildIssueDetails(ctx, src, fx.issues["bd-1"], false, DetailOptions{IncludeComments: true})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the iterator error", err)
		}
		if !strings.Contains(err.Error(), "iter comments bd-1") {
			t.Errorf("err = %q, want the id and the operation", err)
		}
	})
}

// TestBuildIssueDetailsProjectsTheRevisionToken pins the SEAM, which is the
// half types.NewIssueDetails cannot pin for itself.
//
// Both front doors read their detail view from here, so this is where the
// published token is either projected off the row or silently 0. It is
// asserted on BOTH seams because the store and the use case assemble the same
// view through different readers, and a leg that reached the constructor and a
// leg that went back to a struct literal look identical from the outside: 0 is
// a legal token, so a caller cannot tell an unset one from a legacy row.
func TestBuildIssueDetailsProjectsTheRevisionToken(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	fx.issues["bd-1"].RowVersion = 987654321
	store, useCase := fixtureSources(fx)

	for _, tc := range []struct {
		name string
		src  DetailSource
	}{
		{"store seam", store},
		{"use case seam", useCase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, fx.issues["bd-1"], false, DetailOptions{})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			if details.Revision != 987654321 {
				t.Errorf("Revision = %d, want the row's token 987654321", details.Revision)
			}
			if details.Revision != details.RowVersion {
				t.Errorf("Revision = %d but RowVersion = %d; the published token must be the row's",
					details.Revision, details.RowVersion)
			}
		})
	}
}

func TestBuildIssueDetailsRejectsNilIssue(t *testing.T) {
	fx := newDetailFixture()
	store, _ := fixtureSources(fx)
	if _, err := BuildIssueDetails(context.Background(), store, nil, false, DetailOptions{}); err == nil {
		t.Fatal("expected an error for a nil issue")
	}
}

type failingIterSource struct {
	DetailSource
	err error
}

func (f failingIterSource) IterDependents(context.Context, string, bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return nil, f.err
}

func (f failingIterSource) IterComments(context.Context, string, bool) (storage.Iter[types.Comment], error) {
	return nil, f.err
}

func deref(t *testing.T, v *int64) int64 {
	t.Helper()
	if v == nil {
		t.Fatal("count pointer is nil")
	}
	return *v
}
