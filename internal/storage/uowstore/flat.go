package uowstore

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Flat storage.Storage methods.
//
// RULE: a flat method is supported exactly when it is ONE call to a use case
// the proxied CLI already drives through the same unit of work, so it adds no
// SQL and inherits that use case's tested behavior. The exceptions add calls
// to such use cases, still no SQL, only where one call returns less than
// DoltStore's method does: GetIssue also reads the issue's labels, and
// GetIssuesByIDs reads in chunks that GetMany accepts. Reads run in one unit of
// work (uow.RunTxRead). Writes run in one unit of work committed with a
// DoltStore-style message through uow.RunTx, which replays the whole unit of
// work on a serialization failure exactly as every proxied CLI write does;
// a replay starts from a rolled-back transaction, so nothing is applied twice.
// Every other flat method is a generated ErrUnsupported stub.

// guarded is the provider as the store's own reads and writes see it: every
// unit of work it begins is refused after Close, instead of reaching a
// provider whose pool is gone.
type guarded struct{ s *Store }

func (g guarded) NewUOW(ctx context.Context) (uow.UnitOfWork, error) { return g.s.newUOW(ctx) }
func (g guarded) Close(context.Context) error                        { return nil }

// FenceProvider lets a guarded delete in a role built over the guard find the
// provider's GET_LOCK fence; the delete's units of work still go through the
// guard.
func (g guarded) FenceProvider() uow.UnitOfWorkProvider { return g.s.p }

var _ uow.FenceProvider = guarded{}

func read[T any](ctx context.Context, s *Store, work func(ctx context.Context, uw uow.UnitOfWork) (T, error)) (T, error) {
	return uow.RunTxRead(ctx, guarded{s}, work)
}

// write runs work in one unit of work through uow.RunTx. Its commit is
// classified (classifying): a serialization failure is still replayed, and a
// commit whose outcome is unknown surfaces as storage.ErrCommitIndeterminate
// and is never replayed.
func (s *Store) write(ctx context.Context, work func(ctx context.Context, uw uow.UnitOfWork) (string, error)) error {
	return uow.RunTx(ctx, classifying{guarded{s}}, work)
}

// classifying is a provider whose units of work classify their commit error
// as RunInTransaction does (classifyCommitError). Only the flat writes use it:
// roles are built over guarded alone, because they look for optional
// interfaces (uow.ScopedCommitter) on the units of work they open.
type classifying struct{ guarded }

func (c classifying) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	uw, err := c.guarded.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return classifyingUOW{uw}, nil
}

type classifyingUOW struct{ uow.UnitOfWork }

func (u classifyingUOW) Commit(ctx context.Context, message string) error {
	err := u.UnitOfWork.Commit(ctx, message)
	if err == nil || issueops.IsNothingToCommitError(err) || isSerialization(err) {
		return err
	}
	return classifyCommitError(err)
}

// --- issues ---

// GetIssue resolves id against issues, then wisps, and fills Labels from the
// plane it lives in, in the same unit of work, as DoltStore's GetIssue does
// (issueops.GetIssueInTx). The transaction's GetIssue leaves Labels empty,
// as DoltStore's transaction does.
func (s *Store) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, error) {
		t := newTxAdapter(uw)
		issue, err := t.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue.Labels, err = t.GetLabels(ctx, id); err != nil {
			return nil, err
		}
		return issue, nil
	})
}

// GetIssuesByIDs reads ids from both planes with their labels, as DoltStore
// does. It goes through the domain GetMany, whose body
// (issueops.ExecuteGetMany) runs the same batch read DoltStore's
// GetIssuesByIDs runs (issueops.GetIssuesByIDsInTx). GetMany refuses more
// than publicops.MaxGetManyIDs ids and refuses an empty id, and DoltStore
// does neither, so ids are read in chunks of that size inside one read unit
// of work, and an empty id, which names no row, is skipped. An id that
// names no row is left out, as on DoltStore. Rows come back in the order of
// each id's first mention.
func (s *Store) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	wanted := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Issue, error) {
		out := make([]*types.Issue, 0, len(wanted))
		for start := 0; start < len(wanted); start += publicops.MaxGetManyIDs {
			chunk := wanted[start:min(start+publicops.MaxGetManyIDs, len(wanted))]
			got, err := uw.IssueUseCase().GetMany(ctx, publicops.GetManyRequest{IDs: chunk})
			if err != nil {
				return nil, err
			}
			out = append(out, got.Issues...)
		}
		return out, nil
	})
}

func (s *Store) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Issue, error) {
		page, err := uw.IssueUseCase().SearchIssues(ctx, query, filter)
		return page.Items, err
	})
}

func (s *Store) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithCounts, error) {
		page, err := uw.IssueUseCase().SearchIssuesWithCounts(ctx, query, filter)
		return page.Items, err
	})
}

func (s *Store) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]string, error) {
		return uw.IssueUseCase().SearchIssueIDs(ctx, query, filter)
	})
}

func (s *Store) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (int64, error) {
		return uw.IssueUseCase().CountIssues(ctx, query, filter)
	})
}

func (s *Store) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (map[string]int, error) {
		return uw.IssueUseCase().CountIssuesByGroup(ctx, filter, groupBy)
	})
}

func (s *Store) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (*types.Statistics, error) {
		return uw.IssueUseCase().GetStatistics(ctx)
	})
}

func (s *Store) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Issue, error) {
		page, err := uw.IssueUseCase().GetReadyWork(ctx, filter)
		return page.Items, err
	})
}

func (s *Store) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithCounts, error) {
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, filter)
		return page.Items, err
	})
}

func (s *Store) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.BlockedIssue, error) {
		return uw.IssueUseCase().GetBlockedIssues(ctx, filter)
	})
}

func (s *Store) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.EpicStatus, error) {
		return uw.IssueUseCase().GetEpicsEligibleForClosure(ctx)
	})
}

// CreateIssue creates issue under DoltStore.CreateIssue's rules (see
// txAdapter.create) and leaves the minted id in issue.ID. A create that did
// not commit hands the struct back with the IDs it filled emptied again, as
// DoltStore does (GH#8457); an indeterminate commit keeps them.
func (s *Store) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("CreateIssue: issue must not be nil")
	}
	reset := issueops.SnapshotUnsetCreateIDs([]*types.Issue{issue})
	err := s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		// A replayed attempt must mint again, not treat the IDs the
		// rolled-back attempt minted as ones the caller named.
		reset()
		if err := newTxAdapter(uw).create(ctx, []*types.Issue{issue}, actor, createOpts{routeWispTypes: true}); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: create %s", issue.ID), nil
	})
	return issueops.ResetCreateIDsIfRolledBack(err, reset)
}

// CreateIssues creates issues and their dependencies under
// DoltStore.CreateIssues's rules, prefix validation included.
func (s *Store) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for i, issue := range issues {
		if issue == nil {
			return fmt.Errorf("CreateIssues: issue %d must not be nil", i)
		}
	}
	reset := issueops.SnapshotUnsetCreateIDs(issues)
	err := s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		reset()
		if err := newTxAdapter(uw).create(ctx, issues, actor, createOpts{validatePrefix: true, withDeps: true}); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: create %d issue(s)", len(issues)), nil
	})
	return issueops.ResetCreateIDsIfRolledBack(err, reset)
}

func (s *Store) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: update %s", id), newTxAdapter(uw).UpdateIssue(ctx, id, updates, actor)
	})
}

// CloseIssue is the unchecked close, as on DoltStore.
func (s *Store) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: close %s", id), newTxAdapter(uw).CloseIssue(ctx, id, reason, actor, session)
	})
}

func (s *Store) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		t := newTxAdapter(uw)
		isWisp, err := t.resolve(ctx, id)
		if err != nil {
			return "", err
		}
		params := domain.ReopenIssueParams{Reason: reason}
		if isWisp {
			_, err = uw.IssueUseCase().ReopenWisp(ctx, id, params, actor)
		} else {
			_, err = uw.IssueUseCase().ReopenIssue(ctx, id, params, actor)
		}
		return fmt.Sprintf("bd: reopen %s", id), err
	})
}

func (s *Store) DeleteIssue(ctx context.Context, id string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: delete %s", id), newTxAdapter(uw).DeleteIssue(ctx, id)
	})
}

func (s *Store) UnclaimIssue(ctx context.Context, id string, actor string, force bool) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: unclaim %s", id), uw.IssueUseCase().Unclaim(ctx, id, actor, force)
	})
}

func (s *Store) UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: unclaim %s", id), uw.IssueUseCase().UnclaimIfAssignee(ctx, id, actor, expectedAssignee)
	})
}

// --- dependencies ---

// AddDependency is quiet by default, as the transaction's is (see
// txAdapter.AddDependency).
func (s *Store) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (s *Store) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error {
	if dep == nil {
		return fmt.Errorf("AddDependency: dependency must not be nil")
	}
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: dep add %s %s", dep.IssueID, dep.DependsOnID),
			newTxAdapter(uw).AddDependencyWithOptions(ctx, dep, actor, opts)
	})
}

func (s *Store) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

func (s *Store) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts storage.DependencyRemoveOptions) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: dep remove %s %s", issueID, dependsOnID),
			newTxAdapter(uw).RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, opts)
	})
}

// listDeps lists id's neighbors with metadata in the plane id lives in.
func listDeps(ctx context.Context, uw uow.UnitOfWork, id string, dir domain.DepDirection) ([]*types.IssueWithDependencyMetadata, error) {
	isWisp, err := newTxAdapter(uw).resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	filter := domain.DepListFilter{Direction: dir}
	if isWisp {
		return uw.DependencyUseCase().ListWispWithIssueMetadata(ctx, id, filter)
	}
	return uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, filter)
}

func (s *Store) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithDependencyMetadata, error) {
		return listDeps(ctx, uw, issueID, domain.DepDirectionIn)
	})
}

func (s *Store) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithDependencyMetadata, error) {
		return listDeps(ctx, uw, issueID, domain.DepDirectionOut)
	})
}

// --- labels ---

func (s *Store) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: label add %s", issueID), newTxAdapter(uw).AddLabel(ctx, issueID, label, actor)
	})
}

func (s *Store) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: label remove %s", issueID), newTxAdapter(uw).RemoveLabel(ctx, issueID, label, actor)
	})
}

func (s *Store) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]string, error) {
		return newTxAdapter(uw).GetLabels(ctx, issueID)
	})
}

// --- comments ---

func (s *Store) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	var out *types.Comment
	err := s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		isWisp, err := newTxAdapter(uw).resolve(ctx, issueID)
		if err != nil {
			return "", err
		}
		if isWisp {
			out, err = uw.CommentUseCase().AddCommentToWisp(ctx, issueID, author, text)
		} else {
			out, err = uw.CommentUseCase().AddCommentToIssue(ctx, issueID, author, text)
		}
		return fmt.Sprintf("bd: comment %s", issueID), err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Comment, error) {
		return newTxAdapter(uw).GetIssueComments(ctx, issueID)
	})
}

func (s *Store) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (int64, error) {
		isWisp, err := newTxAdapter(uw).resolve(ctx, issueID)
		if err != nil {
			return 0, err
		}
		if isWisp {
			return uw.CommentUseCase().CountCommentsForWisp(ctx, issueID)
		}
		return uw.CommentUseCase().CountCommentsForIssue(ctx, issueID)
	})
}

// --- config ---

// SetConfig goes through the same use case as the proxied CLI's config
// write, which re-syncs custom_types / custom_statuses in the same
// transaction (configSQLRepositoryImpl.SetConfig).
func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return fmt.Sprintf("bd: config set %s", key), uw.ConfigUseCase().SetConfig(ctx, key, value)
	})
}

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetConfig(ctx, key)
	})
}

func (s *Store) GetAllConfig(ctx context.Context) (map[string]string, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (map[string]string, error) {
		return uw.ConfigUseCase().GetAllConfig(ctx)
	})
}

// SetLocalMetadata writes dolt-ignored state, so it commits with no Dolt
// commit (uow.RunTxEphemeral), as the proxied CLI does.
func (s *Store) SetLocalMetadata(ctx context.Context, key, value string) error {
	_, err := uow.RunTxEphemeral(ctx, classifying{guarded{s}}, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
		return struct{}{}, uw.ConfigUseCase().SetLocalMetadata(ctx, key, value)
	})
	return err
}

func (s *Store) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetLocalMetadata(ctx, key)
	})
}
