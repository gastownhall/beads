package sqlkit

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This file wires the shared "portable" non-version-control methods onto *Store. Each is
// the same thin issueops delegation the embedded-Dolt reference uses, so behavior is the
// reference's by construction; the dialect layer translates the underlying SQL. Reads run
// in a read tx, writes in a write tx.

// --- Molecule rollups ---

func (s *Store) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	var out *types.MoleculeProgressStats
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetMoleculeProgressInTx(ctx, tx, moleculeID)
		return e
	})
	return out, err
}

func (s *Store) GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	var out *types.MoleculeLastActivity
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetMoleculeLastActivityInTx(ctx, tx, moleculeID)
		return e
	})
	return out, err
}

// --- Repo-mtime cache ---

func (s *Store) GetRepoMtime(ctx context.Context, repoPath string) (int64, error) {
	var out int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetRepoMtimeInTx(ctx, tx, repoPath)
		return e
	})
	return out, err
}

func (s *Store) SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetRepoMtimeInTx(ctx, tx, repoPath, jsonlPath, mtimeNs)
	})
}

func (s *Store) ClearRepoMtime(ctx context.Context, repoPath string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.ClearRepoMtimeInTx(ctx, tx, repoPath)
	})
}

// --- Event / dependency streams ---

func (s *Store) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	var out []*types.Event
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetAllEventsSinceInTx(ctx, tx, since)
		return e
	})
	return out, err
}

func (s *Store) IterAllEventsSince(ctx context.Context, since time.Time) (storage.Iter[types.Event], error) {
	events, err := s.GetAllEventsSince(ctx, since)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(events), nil
}

func (s *Store) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	var out map[string][]*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetAllDependencyRecordsInTx(ctx, tx)
		return e
	})
	return out, err
}

func (s *Store) IterAllDependencyRecords(ctx context.Context) (storage.Iter[types.Dependency], error) {
	all, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	var flat []*types.Dependency
	for _, deps := range all {
		flat = append(flat, deps...)
	}
	return storage.NewSliceIter(flat), nil
}

// --- Dependent counts ---

// CountDependentsByStatus counts issues that depend on issueID whose source issue is in
// the given status, summed across the durable and wisp dependency tables. Mirrors the
// embedded-Dolt reference (counts.go): COALESCE resolves the edge target, and both
// classes are counted so a durable issue with a wisp dependent is not under-counted.
func (s *Store) CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error) {
	var total int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM dependencies d
			JOIN issues i ON i.id = d.issue_id
			WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND i.status = ?
		`, issueID, string(status)).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM wisp_dependencies d
			JOIN wisps w ON w.id = d.issue_id
			WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND w.status = ?
		`, issueID, string(status)).Scan(&wisp); err != nil {
			return err
		}
		total = perm + wisp
		return nil
	})
	return total, err
}

func (s *Store) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	var out map[string]bool
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.FindWispDependentsRecursiveInTx(ctx, tx, ids)
		return e
	})
	return out, err
}

// --- Comment / audit writes ---

func (s *Store) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.AddCommentEventInTx(ctx, tx, issueID, actor, comment)
	})
}

func (s *Store) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var out *types.Comment
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.ImportIssueCommentInTx(ctx, tx, issueID, author, text, createdAt)
		return e
	})
	return out, err
}

// --- Id rekey / wisp promote / source-repo purge ---

func (s *Store) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.UpdateIssueIDInTx(ctx, tx, oldID, newID, issue, actor)
	})
}

func (s *Store) PromoteFromEphemeral(ctx context.Context, id, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.PromoteFromEphemeralInTx(ctx, tx, id, actor)
	})
}

func (s *Store) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	var count int
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var e error
		count, e = issueops.DeleteIssuesBySourceRepoInTx(ctx, tx, sourceRepo)
		return e
	})
	return count, err
}

// --- Batch create ---

// CreateIssuesWithFullOptions mirrors the embedded-Dolt reference (dolt/issues.go):
// an all-wisps batch marks every issue ephemeral (unless it is explicitly
// no-history) so the rows route to the wisp tables, and EVERY batch — all-wisps or
// mixed/durable — is created in a SINGLE transaction through
// issueops.CreateIssuesInTx. That batch path is what threads opts, persists inline
// dependencies, applies conflict/stale policy, reconciles child counters, and
// recomputes is_blocked. The reference keeps two arms only to skip Dolt versioning
// for the all-wisps case; sqlkit has no Dolt versioning, so the arms differ solely
// in the ephemeral marking.
//
// Do NOT loop the single-issue issueops.CreateIssueInTx here: it never runs the
// dependency-persist pass, so an all-wisps batch carrying inline dependencies would
// silently drop every edge, and one transaction per issue would forfeit batch
// atomicity.
func (s *Store) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}
	if issueops.AllWisps(issues) {
		for _, issue := range issues {
			if !issue.NoHistory {
				issue.Ephemeral = true
			}
		}
	}
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.CreateIssuesInTx(ctx, tx, issues, actor, opts)
	})
}
