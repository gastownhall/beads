package uowstore

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/journalops"
)

// Optional interfaces embedders type-assert on a storage.Storage. The SDK's
// EventQuerier and DependentQuerier are NOT implemented: neither has a
// one-call use case, so a caller's assertion fails cleanly instead of
// reaching an ErrUnsupported at call time.

var (
	_ storage.EventsJournalConfigurer = (*Store)(nil)
	_ journalops.Journal              = (*Store)(nil)
	_ interface{ IsClosed() bool }    = (*Store)(nil)
)

// SetEventsJournalEnabled forwards to the provider. The SDK open already
// activates the journal exactly as the CLI does; a later call (Gas City makes
// one) wins, as on DoltStore. After Close it does nothing.
func (s *Store) SetEventsJournalEnabled(enabled bool) {
	p, err := s.provider()
	if err != nil {
		return
	}
	if c, ok := p.(storage.EventsJournalConfigurer); ok {
		c.SetEventsJournalEnabled(enabled)
	}
}

// EventsJournalActivationError forwards the provider's journal shape check,
// when it has one.
func (s *Store) EventsJournalActivationError() error {
	if c, ok := s.p.(storage.EventsJournalShapeChecker); ok {
		return c.EventsJournalActivationError()
	}
	return nil
}

// ReadEventsJournalPage reads one journal page in one read-only unit of work
// (uow.NewEventsJournalCursor).
func (s *Store) ReadEventsJournalPage(ctx context.Context, since int64, limit int) (journalops.Page, error) {
	cursor, err := uow.NewEventsJournalCursor(guarded{s})
	if err != nil {
		return journalops.Page{}, err
	}
	return cursor.ReadEventsJournalPage(ctx, since, limit)
}

// IsBlocked reports the denormalized transitive blocked flag and the open
// direct blockers (beads.BlockedQuerier).
func (s *Store) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	type blocked struct {
		is       bool
		blockers []string
	}
	out, err := read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (blocked, error) {
		is, blockers, err := uw.DependencyUseCase().IsBlocked(ctx, issueID)
		return blocked{is, blockers}, err
	})
	return out.is, out.blockers, err
}

// IsBlockedBatch is IsBlocked's flag for many ids in one read
// (beads.BlockedQuerier).
func (s *Store) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	return read(ctx, s, func(ctx context.Context, uw uow.UnitOfWork) (map[string]bool, error) {
		return uw.DependencyUseCase().IsBlockedBatch(ctx, ids)
	})
}

// ClaimIssue atomically claims id for actor (beads.IssueClaimer). A conflict
// is the use case's typed refusal (ErrAlreadyClaimed / ErrNotClaimable).
func (s *Store) ClaimIssue(ctx context.Context, id string, actor string) error {
	return s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		isWisp, err := newTxAdapter(uw).resolve(ctx, id)
		if err != nil {
			return "", err
		}
		if isWisp {
			_, err = uw.IssueUseCase().ClaimWisp(ctx, id, actor)
		} else {
			_, err = uw.IssueUseCase().ClaimIssue(ctx, id, actor)
		}
		return fmt.Sprintf("bd: claim %s", id), err
	})
}

// ClaimReadyIssue claims the first ready issue matching filter, or returns
// (nil, nil) when none is claimable (beads.IssueClaimer).
func (s *Store) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	var out *types.Issue
	err := s.write(ctx, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		out = nil
		claimed, err := uw.IssueUseCase().ClaimReadyIssue(ctx, filter, actor)
		if err != nil {
			return "", err
		}
		if !claimed.Claimed || claimed.Issue == nil {
			return "", nil
		}
		out = claimed.Issue
		return issueops.ClaimNextCommitMessage(claimed.Issue.ID), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
