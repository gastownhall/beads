package dolt

import (
	"context"
	"database/sql"
	"log"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *DoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		tables, err := fn(tx)
		return tables, commitMsg, err
	})
}

// runIssueOperationTxWithMessage is runIssueOperationTx for an operation whose
// commit message is only known once the body has run. A ready claim names the
// id it won, and nothing outside the transaction can predict which one that
// is, so the message is composed where the selection happens.
//
// Ordering contract (lost-update fix, LatentLabsSpace/NEXUS#92): the Dolt
// commit MUST run after the SQL transaction commits, never inside it.
// DOLT_ADD stages the ENTIRE table from the session's transaction root; when
// it ran inside the still-open transaction, the Dolt commit was built from
// the pre-merge BEGIN-time snapshot with the then-current branch HEAD as its
// parent — so every row a concurrent writer had committed inside the
// transaction's window was silently written back to its BEGIN-time value
// (observed in production as reverted claims). Committing the SQL
// transaction first lets Dolt's commit-time three-way merge reconcile this
// session's changes with concurrent writers; the post-commit DOLT_ADD then
// stages the merged state, which cannot resurrect stale rows.
//
// Failure mode note: if the post-commit Dolt commit fails (after retries),
// the data change HAS landed — it remains in the branch working set and
// rides the next Dolt commit on the branch. That failure is therefore
// logged and NOT propagated: the mutation succeeded, and surfacing an error
// here would make every caller treat an applied mutation as failed —
// automated retries double-apply (duplicate creates/comments), completion
// hooks are skipped for a persisted state change, and the claim verify
// wrappers report claims the caller actually holds as failed (the wy-x543k
// inversion). The only cost of a swallowed failure is a missing dolt-log
// audit line until the next commit.
//
// Caller contract (non-verified batch mutators): CreateBatch, ApplyBatch, and
// CloseBatch route through here and, unlike the claim paths, have no
// verify-by-re-read recovery — so for them a nil error means "data durable in
// the branch working set; the trailing Dolt history commit may be deferred and
// is observable only via bd.db.post_tx_commit_dropped." That is the conscious,
// documented contract: a nil return is not a retry signal, and propagating a
// publish-failure sentinel to those callers would reintroduce the double-apply
// inversion this reorder exists to prevent.
//
// Audit-trail caveat (server mode): sessions on one branch share the working
// set, so a concurrent writer's DOLT_COMMIT can absorb this operation's rows
// under its own message; this operation's commit then degrades to
// nothing-to-commit and its message never reaches dolt log. The data is
// intact — doltAddAndCommit logs the absorption so the missing audit line is
// explicable.
func (s *DoltStore) runIssueOperationTxWithMessage(ctx context.Context, fn func(*sql.Tx) (storageissueops.ChangedTables, string, error)) error {
	var tables storageissueops.ChangedTables
	var commitMsg string
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		// Plain assignment (not append-into-captured-state): a retried
		// transaction re-runs the body and overwrites both values, so a
		// rolled-back attempt cannot leak tables into the commit.
		var err error
		tables, commitMsg, err = fn(tx)
		return err
	})
	if err != nil {
		// Includes ErrCommitIndeterminate (ambiguous commit loss): NO post-tx
		// dolt commit is attempted there. If the ambiguous commit actually
		// landed, its audit entry is lost (the change rides the next dolt
		// commit) — accepted, because attempting one would stage the SHARED
		// branch working set, and when the commit actually rolled back that
		// mints a dolt commit of a concurrent writer's pending rows under
		// THIS operation's message (phantom audit evidence), fires even for
		// definite rollbacks (a caller cancellation during tx.Commit is also
		// tagged indeterminate), and delays the claim-verify re-read that
		// resolves the true outcome.
		return err
	}
	s.publishCommittedOperation(ctx, tables, commitMsg)
	return nil
}

// publishCommittedOperation is the one post-tx publication tail every
// SQL-committed issue mutation funnels through (GH#5752): it stages tables and
// mints the trailing Dolt history commit strictly AFTER the SQL transaction
// committed — never inside it. See runIssueOperationTxWithMessage for the
// lost-update rationale and the failure-mode note this tail implements: a
// publication failure is counted and logged, never propagated, because the
// data change already committed and rides the next dolt commit on the branch.
//
// Only call this once the SQL transaction has committed successfully — never
// from inside one, never after an error path where the commit outcome is
// ambiguous (ErrCommitIndeterminate): staging the shared branch working set
// off an ambiguous commit mints phantom audit evidence.
func (s *DoltStore) publishCommittedOperation(ctx context.Context, tables storageissueops.ChangedTables, commitMsg string) {
	staged := sortedDirtyTables(tables)
	if len(staged) == 0 {
		return
	}
	if commitMsg == "" {
		// A body can dirty tables without composing a message (e.g. a ready
		// claim whose side effects landed but which claimed nothing); never
		// mint a Dolt commit with an empty message.
		commitMsg = "bd: issue operation"
	}
	if err := s.doltAddAndCommitPostTx(ctx, staged, commitMsg); err != nil {
		doltMetrics.postTxCommitDropped.Add(ctx, 1)
		log.Printf("dolt: post-tx dolt commit failed for %q (data already committed; change rides the next dolt commit): %v", commitMsg, err)
	}
}

// publishCommittedTables is publishCommittedOperation for a site whose table
// set is a derived list rather than a ChangedTables map — the lifecycle
// runner's dirtyTableNames and the deletes' fixed cascade lists. Same
// contract: only after the SQL transaction committed, never inside one, never
// after an ambiguous commit.
func (s *DoltStore) publishCommittedTables(ctx context.Context, tables []string, commitMsg string) {
	if len(tables) == 0 {
		return
	}
	changed := make(storageissueops.ChangedTables, len(tables))
	for _, table := range tables {
		changed[table] = true
	}
	s.publishCommittedOperation(ctx, changed, commitMsg)
}
