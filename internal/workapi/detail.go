package workapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// DetailOptions selects the two expensive halves of the detail view. Both
// default to off: the detail response carries counts only (be-ijck6q), and a
// caller that wants the rows asks for them.
//
// BriefDeps is opt-in for the opposite reason: dependencies are populated by
// default and stay that way, so no existing caller loses a field it reads.
type DetailOptions struct {
	IncludeDependents bool
	IncludeComments   bool
	BriefDeps         bool
}

// DetailSource is the read seam issue-detail assembly runs on. It is the
// single definition of what one issue's detail view contains, so a CLI
// command and an HTTP handler cannot drift into answering it differently.
//
// Every method carries an isWisp flag because the domain use cases are
// per-table: an ephemeral wisp lives in `wisps` with its own labels,
// dependency, and comment tables, and the caller must say which side it
// wants. The store seam resolves that routing internally and therefore
// ignores the flag - see NewStoreDetailSource.
type DetailSource interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetWisp(ctx context.Context, id string) (*types.Issue, error)

	Labels(ctx context.Context, id string, isWisp bool) ([]string, error)
	Dependencies(ctx context.Context, id string, isWisp bool) ([]*types.IssueWithDependencyMetadata, error)

	CountDependencies(ctx context.Context, id string, isWisp bool) (int64, error)
	CountDependents(ctx context.Context, id string, isWisp bool) (int64, error)
	CountComments(ctx context.Context, id string, isWisp bool) (int64, error)

	IterDependents(ctx context.Context, id string, isWisp bool) (storage.Iter[types.IssueWithDependencyMetadata], error)
	IterComments(ctx context.Context, id string, isWisp bool) (storage.Iter[types.Comment], error)
}

// isNotFound reports whether err is any of the shapes a storage seam uses to
// say "no such row".
//
// The seams disagree. The store wraps storage.ErrNotFound; the domain layer
// wraps database/sql's ErrNoRows, because db.issueSQLRepositoryImpl.Get
// returns sql.ErrNoRows unchanged and issueUseCaseImpl.get wraps it with the
// id. Callers must not have to know which one they are talking to - which is
// why this stays unexported: the one way out of this package is through
// GetIssueOrWisp, which has already collapsed both shapes onto
// storage.ErrNotFound. Exporting it would hand frontends a second way to read
// seam errors, and the whole point is that they never see one.
func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

func notFound(id string) error {
	return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
}

// GetIssueOrWisp resolves id to a durable issue, falling back to the wisp
// table, and reports which side answered.
//
// Not-found is always storage.ErrNotFound, never a raw sql.ErrNoRows and
// never a (nil, nil) pair. That normalization lives here rather than in a
// caller on purpose: both failure modes it prevents are caller bugs that
// only look correct until the seam underneath changes. A frontend that
// writes `if err != nil || issue == nil` reports a dropped connection as
// "issue not found"; a frontend that trusts errors.Is(err, ErrNotFound)
// against the domain seam ships a 500 for a missing issue. Neither caller
// can reintroduce those from here.
//
// A hard error from the issue lookup short-circuits: it is returned as-is
// rather than being retried against the wisp table, so a backend failure
// stays a backend failure instead of decaying into not-found.
func GetIssueOrWisp(ctx context.Context, src DetailSource, id string) (*types.Issue, bool, error) {
	issue, err := src.GetIssue(ctx, id)
	if err == nil && issue != nil {
		return issue, false, nil
	}
	if err != nil && !isNotFound(err) {
		return nil, false, err
	}

	wisp, wispErr := src.GetWisp(ctx, id)
	if wispErr == nil && wisp != nil {
		return wisp, true, nil
	}
	if wispErr != nil && !isNotFound(wispErr) {
		return nil, false, wispErr
	}
	return nil, false, notFound(id)
}

// BuildIssueDetails assembles the detail view of an already-resolved issue:
// labels, outgoing dependencies with their metadata, the three cardinality
// counts, and - under DetailOptions - the dependent and comment rows.
//
// Labels, dependencies, and counts are best effort, matching what both CLI
// paths have always shipped: a detail view missing an edge list still beats
// refusing to show the issue. The opt-in row streams are not best effort,
// because a caller that asked for the rows gets a truthful answer or an
// error rather than a silently short list.
func BuildIssueDetails(ctx context.Context, src DetailSource, issue *types.Issue, isWisp bool, opts DetailOptions) (*types.IssueDetails, error) {
	if issue == nil {
		return nil, errors.New("build issue details: issue must not be nil")
	}
	id := issue.ID
	details := types.NewIssueDetails(*issue)

	details.Labels, _ = src.Labels(ctx, id, isWisp)
	details.Dependencies, _ = src.Dependencies(ctx, id, isWisp)

	// Aggregate counts - O(1) queries, no row materialization.
	dependentCount, _ := src.CountDependents(ctx, id, isWisp)
	details.DependentCount = &dependentCount
	dependencyCount, _ := src.CountDependencies(ctx, id, isWisp)
	details.DependencyCount = &dependencyCount
	commentCount, _ := src.CountComments(ctx, id, isWisp)
	details.CommentCount = &commentCount

	if opts.IncludeDependents {
		dependents, err := collectDependents(ctx, src, id, isWisp)
		if err != nil {
			return nil, err
		}
		details.Dependents = dependents
		applyEpicProgress(details, dependents)
	}

	if opts.IncludeComments {
		comments, err := collectComments(ctx, src, id, isWisp)
		if err != nil {
			return nil, err
		}
		details.Comments = comments
	} else if commentCount > 0 {
		// ga-clgh: comment_count alone announces content exists without
		// saying the representation is partial. Flag it explicitly so a
		// caller reading only `.comments` doesn't read absence as "none".
		omitted := true
		details.CommentsOmitted = &omitted
	}

	for _, dep := range details.Dependencies {
		if dep.DependencyType == types.DepParentChild {
			parentID := dep.ID
			details.Parent = &parentID
			break
		}
	}

	// After the Parent scan, which reads DependencyType and ID off the full rows.
	// Builds a new slice: the source owns the one it returned and may cache it.
	if opts.BriefDeps && details.Dependencies != nil {
		brief := make([]*types.IssueWithDependencyMetadata, 0, len(details.Dependencies))
		for _, dep := range details.Dependencies {
			if dep == nil {
				brief = append(brief, nil)
				continue
			}
			brief = append(brief, shallowDep(dep))
		}
		details.Dependencies = brief
	}
	return details, nil
}

// shallowDep keeps the identity-and-shape fields callers consume and drops the
// free-form text, which is the shape collectDependents settled on in be-4d36f2.
func shallowDep(item *types.IssueWithDependencyMetadata) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue: types.Issue{
			ID:        item.Issue.ID,
			Status:    item.Issue.Status,
			IssueType: item.Issue.IssueType,
			Priority:  item.Issue.Priority,
			Title:     item.Issue.Title,
		},
		DependencyType: item.DependencyType,
	}
}

// collectDependents streams the dependents and keeps only the
// identity-and-shape fields of each.
//
// be-4d36f2: hub beads with thousands of dependents made `bd show --json
// <hub>` allocate 5-13 GB marshaling full Issue records. The shallow shape
// preserves what callers consume (id, status, type, priority, title) and
// drops the free-form text; streaming keeps the full rows from piling up
// while we do it.
func collectDependents(ctx context.Context, src DetailSource, id string, isWisp bool) ([]*types.IssueWithDependencyMetadata, error) {
	iter, err := src.IterDependents(ctx, id, isWisp)
	if err != nil {
		return nil, fmt.Errorf("iter dependents %s: %w", id, err)
	}
	defer iter.Close() //nolint:errcheck // read-only iterator

	var out []*types.IssueWithDependencyMetadata
	for iter.Next(ctx) {
		item := iter.Value()
		if item == nil {
			continue
		}
		out = append(out, shallowDep(item))
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("iter dependents %s: %w", id, err)
	}
	return out, nil
}

func collectComments(ctx context.Context, src DetailSource, id string, isWisp bool) ([]*types.Comment, error) {
	iter, err := src.IterComments(ctx, id, isWisp)
	if err != nil {
		return nil, fmt.Errorf("iter comments %s: %w", id, err)
	}
	defer iter.Close() //nolint:errcheck // read-only iterator

	var out []*types.Comment
	for iter.Next(ctx) {
		item := iter.Value()
		if item == nil {
			continue
		}
		// storage.Iter may reuse the pointer across Next calls, so a value
		// we keep has to be our own copy.
		comment := *item
		out = append(out, &comment)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("iter comments %s: %w", id, err)
	}
	return out, nil
}

// applyEpicProgress derives an epic's child tally from its streamed
// dependents. Only parent-child edges count as children; an epic with no
// children reports nothing rather than 0/0.
func applyEpicProgress(details *types.IssueDetails, dependents []*types.IssueWithDependencyMetadata) {
	if details.IssueType != types.TypeEpic || len(dependents) == 0 {
		return
	}
	total, closed := 0, 0
	for _, dep := range dependents {
		if dep.DependencyType != types.DepParentChild {
			continue
		}
		total++
		if dep.Status == types.StatusClosed {
			closed++
		}
	}
	if total == 0 {
		return
	}
	closeable := total == closed
	details.EpicTotalChildren = &total
	details.EpicClosedChildren = &closed
	details.EpicCloseable = &closeable
}

// StoreDetailReader is the subset of storage.DoltStorage that detail assembly
// reads. Naming the subset keeps the seam small enough to fake in a test.
type StoreDetailReader interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	CountDependencies(ctx context.Context, issueID string) (int64, error)
	CountDependents(ctx context.Context, issueID string) (int64, error)
	CountIssueComments(ctx context.Context, issueID string) (int64, error)
	IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error)
	IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error)
}

var _ StoreDetailReader = (storage.DoltStorage)(nil)

type storeDetailSource struct{ store StoreDetailReader }

// NewStoreDetailSource reads detail straight from a store handle.
//
// The store routes every id to the issue or wisp table itself, so this
// adapter ignores the isWisp flag and its GetWisp is a dead end: the store's
// GetIssue has already tried both tables by the time it reports not-found.
func NewStoreDetailSource(store StoreDetailReader) DetailSource {
	return storeDetailSource{store: store}
}

func (s storeDetailSource) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return s.store.GetIssue(ctx, id)
}

func (s storeDetailSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}

func (s storeDetailSource) Labels(ctx context.Context, id string, _ bool) ([]string, error) {
	return s.store.GetLabels(ctx, id)
}

func (s storeDetailSource) Dependencies(ctx context.Context, id string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	return s.store.GetDependenciesWithMetadata(ctx, id)
}

func (s storeDetailSource) CountDependencies(ctx context.Context, id string, _ bool) (int64, error) {
	return s.store.CountDependencies(ctx, id)
}

func (s storeDetailSource) CountDependents(ctx context.Context, id string, _ bool) (int64, error) {
	return s.store.CountDependents(ctx, id)
}

func (s storeDetailSource) CountComments(ctx context.Context, id string, _ bool) (int64, error) {
	return s.store.CountIssueComments(ctx, id)
}

func (s storeDetailSource) IterDependents(ctx context.Context, id string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return s.store.IterDependentsWithMetadata(ctx, id)
}

func (s storeDetailSource) IterComments(ctx context.Context, id string, _ bool) (storage.Iter[types.Comment], error) {
	return s.store.IterIssueComments(ctx, id)
}

// The four use-case slices the UOW adapter needs. They exist so the adapter's
// wisp routing - the half of it that is not shared with the store - can be
// tested without standing up the full domain interfaces.
type (
	detailIssueReader interface {
		GetIssue(ctx context.Context, id string) (*types.Issue, error)
		GetWisp(ctx context.Context, id string) (*types.Issue, error)
	}

	detailLabelReader interface {
		GetLabels(ctx context.Context, issueID string) ([]string, error)
		GetWispLabels(ctx context.Context, wispID string) ([]string, error)
	}

	detailDepReader interface {
		ListWithIssueMetadata(ctx context.Context, issueID string, filter domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error)
		ListWispWithIssueMetadata(ctx context.Context, wispID string, filter domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error)
		IterWithIssueMetadata(ctx context.Context, issueID string, filter domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error)
		IterWispWithIssueMetadata(ctx context.Context, wispID string, filter domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error)
		CountByIssueID(ctx context.Context, issueID string, filter domain.DepListFilter) (int64, error)
		CountByWispID(ctx context.Context, wispID string, filter domain.DepListFilter) (int64, error)
	}

	detailCommentReader interface {
		CountCommentsForIssue(ctx context.Context, issueID string) (int64, error)
		CountCommentsForWisp(ctx context.Context, wispID string) (int64, error)
		IterCommentsForIssue(ctx context.Context, issueID string) (storage.Iter[types.Comment], error)
		IterCommentsForWisp(ctx context.Context, wispID string) (storage.Iter[types.Comment], error)
	}
)

type useCaseDetailSource struct {
	issues   detailIssueReader
	labels   detailLabelReader
	deps     detailDepReader
	comments detailCommentReader
}

// NewUOWDetailSource reads detail through an open unit of work.
func NewUOWDetailSource(uw UnitOfWork) DetailSource {
	return newUseCaseDetailSource(uw.IssueUseCase(), uw.LabelUseCase(), uw.DependencyUseCase(), uw.CommentUseCase())
}

func newUseCaseDetailSource(issues detailIssueReader, labels detailLabelReader, deps detailDepReader, comments detailCommentReader) DetailSource {
	return useCaseDetailSource{issues: issues, labels: labels, deps: deps, comments: comments}
}

func (u useCaseDetailSource) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return u.issues.GetIssue(ctx, id)
}

func (u useCaseDetailSource) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	return u.issues.GetWisp(ctx, id)
}

func (u useCaseDetailSource) Labels(ctx context.Context, id string, isWisp bool) ([]string, error) {
	if isWisp {
		return u.labels.GetWispLabels(ctx, id)
	}
	return u.labels.GetLabels(ctx, id)
}

func (u useCaseDetailSource) Dependencies(ctx context.Context, id string, isWisp bool) ([]*types.IssueWithDependencyMetadata, error) {
	filter := domain.DepListFilter{Direction: domain.DepDirectionOut}
	if isWisp {
		return u.deps.ListWispWithIssueMetadata(ctx, id, filter)
	}
	return u.deps.ListWithIssueMetadata(ctx, id, filter)
}

func (u useCaseDetailSource) CountDependencies(ctx context.Context, id string, isWisp bool) (int64, error) {
	return u.countDeps(ctx, id, isWisp, domain.DepDirectionOut)
}

func (u useCaseDetailSource) CountDependents(ctx context.Context, id string, isWisp bool) (int64, error) {
	return u.countDeps(ctx, id, isWisp, domain.DepDirectionIn)
}

func (u useCaseDetailSource) countDeps(ctx context.Context, id string, isWisp bool, dir domain.DepDirection) (int64, error) {
	filter := domain.DepListFilter{Direction: dir}
	if isWisp {
		return u.deps.CountByWispID(ctx, id, filter)
	}
	return u.deps.CountByIssueID(ctx, id, filter)
}

func (u useCaseDetailSource) CountComments(ctx context.Context, id string, isWisp bool) (int64, error) {
	if isWisp {
		return u.comments.CountCommentsForWisp(ctx, id)
	}
	return u.comments.CountCommentsForIssue(ctx, id)
}

func (u useCaseDetailSource) IterDependents(ctx context.Context, id string, isWisp bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	filter := domain.DepListFilter{Direction: domain.DepDirectionIn}
	if isWisp {
		return u.deps.IterWispWithIssueMetadata(ctx, id, filter)
	}
	return u.deps.IterWithIssueMetadata(ctx, id, filter)
}

func (u useCaseDetailSource) IterComments(ctx context.Context, id string, isWisp bool) (storage.Iter[types.Comment], error) {
	if isWisp {
		return u.comments.IterCommentsForWisp(ctx, id)
	}
	return u.comments.IterCommentsForIssue(ctx, id)
}
