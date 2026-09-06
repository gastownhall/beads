package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Dual-write issue-version history records every accepted issue mutation as a
// row in issue_versions, written in the SAME transaction as the mutation
// itself (see internal/storage/schema/migrations/0067_add_versioned_beads_schema.up.sql).
// Activation mirrors the durable events journal (journal.go): a per-instance
// flag (storage.VersionedHistoryConfigurer) is bound to a concrete
// transaction via ScopeVersionedHistoryTransaction immediately after
// BeginTx, so enabling history on one store instance cannot turn it on for
// any other sharing the process.
//
// RecordVersionInTx is the single seam both direct-SQL legs (dolt,
// embeddeddolt) and the domain/db package (used by uow) call through, from
// inside the same already-short-circuited functions that call
// RecordEventInTx: a mutation DiscardNoopIssueUpdates has already discarded
// never reaches either seam. dependency_editor.go's edge add/remove is the
// one caller that is NOT already short-circuited by that helper — it gates
// on its own eventWritten signal instead (a duplicate add / absent remove
// never reaches this seam either).

var versionedHistoryTransactions sync.Map // map[DBTX]bool; entries live for one transaction

// ScopeVersionedHistoryTransaction associates versioned-history activation
// with one concrete transaction and returns a cleanup function. Store
// implementations call it immediately after BeginTx, alongside (not instead
// of) ScopeEventsJournalTransaction. This is instance/project scoped even
// when many stores share a process; there is no process-wide activation
// switch.
func ScopeVersionedHistoryTransaction(tx DBTX, enabled bool) func() {
	if tx == nil {
		return func() {}
	}
	versionedHistoryTransactions.Store(tx, enabled)
	return func() { versionedHistoryTransactions.Delete(tx) }
}

func versionedHistoryEnabled(tx DBTX) bool {
	enabled, _ := versionedHistoryTransactions.Load(tx)
	on, _ := enabled.(bool)
	return on
}

// RecordVersionInTx mints one issue_versions row for issueID and advances
// issues.current_revision to match, as of tx (read-your-writes within the
// same transaction). A no-op when versioned history is disabled for tx, or
// when issueID resolves to a wisp: wisps carry current_revision for shape
// parity only and are never versioned this phase (design FR-8).
//
// actor is the acting identity that performed the mutation, recorded as the
// version row's attribution — "" when the mutation path genuinely has none,
// matching RecordEventInTx's own convention.
func RecordVersionInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("versioned history: snapshot %s: %w", issueID, err)
	}
	if IsWisp(issue) {
		return nil
	}

	// durable_state is a verbatim marshal of the mutated issue (design §15.3,
	// corrected by §17.1): the write-path loader GetIssueInTx never hydrates
	// Dependencies (types.Issue.Dependencies is omitempty and unrelated to
	// this snapshot's own read), so it is populated here, once, for every
	// caller of this seam — in GetDependencyRecordsForIssuesInTx's own
	// ordering (issue_id, depends_on_id, type, id).
	deps, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{issueID})
	if err != nil {
		return fmt.Errorf("versioned history: load dependencies for %s: %w", issueID, err)
	}
	issue.Dependencies = deps[issueID]

	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO store_epoch (id, epoch) VALUES (1, 1)"); err != nil {
		return fmt.Errorf("versioned history: seed store epoch: %w", err)
	}
	var epoch int
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch); err != nil {
		return fmt.Errorf("versioned history: read store epoch: %w", err)
	}

	var newRevision int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(revision), 0) + 1 FROM issue_versions WHERE issue_id = ?", issueID,
	).Scan(&newRevision); err != nil {
		return fmt.Errorf("versioned history: compute next revision for %s: %w", issueID, err)
	}

	durableState, err := json.Marshal(issue)
	if err != nil {
		return fmt.Errorf("versioned history: marshal durable state for %s: %w", issueID, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issue_versions
			(issue_id, revision, epoch, durable_state, change_actor, change_agent, change_message, change_at)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?)`,
		issueID, newRevision, epoch, string(durableState), actor, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("versioned history: insert version row for %s: %w", issueID, err)
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET current_revision = ? WHERE id = ?", newRevision, issueID,
	); err != nil {
		return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
	}
	return nil
}
