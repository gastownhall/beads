package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// transact wraps store.RunInTransaction and marks that a transactional
// DOLT_COMMIT occurred, preventing the redundant maybeAutoCommit in
// PersistentPostRun. Use this instead of calling store.RunInTransaction
// directly from command handlers.
func transact(ctx context.Context, s storage.DoltStorage, commitMsg string, fn func(tx storage.Transaction) error) error {
	err := s.RunInTransaction(ctx, commitMsg, fn)
	if err == nil {
		commandDidExplicitDoltCommit = true
	}
	return err
}

// transactHonoringAutoCommit wraps transactional CLI writes whose Dolt commit is
// part of command auto-commit policy. In embedded batch/off modes the SQL
// transaction still commits, but no Dolt version commit is created.
func transactHonoringAutoCommit(ctx context.Context, s storage.DoltStorage, commitMsg string, fn func(tx storage.Transaction) error) error {
	msg := commitMsg
	committedExplicitly := strings.TrimSpace(msg) != ""
	if isEmbeddedMode() {
		mode, err := getDoltAutoCommitMode()
		if err != nil {
			return err
		}
		if mode != doltAutoCommitOn {
			msg = ""
			committedExplicitly = false
		}
	}

	err := s.RunInTransaction(ctx, msg, fn)
	if err == nil && committedExplicitly {
		commandDidExplicitDoltCommit = true
	}
	return err
}

type doltAutoCommitParams struct {
	// Command is the top-level bd command name (e.g., "create", "update").
	Command string
	// IssueIDs are the primary issue IDs affected by the command (optional).
	IssueIDs []string
	// MessageOverride, if non-empty, is used verbatim.
	MessageOverride string
}

// maybeAutoCommit creates a Dolt commit after a successful write command when enabled.
//
// Semantics:
//   - Only applies when dolt auto-commit is "on" AND the active store is versioned (Dolt).
//   - Skips SQL server modes; the server owns transaction commit lifecycle there.
//   - In "batch" mode, commits are deferred — changes accumulate in the working set
//     until an explicit commit point (bd dolt commit).
//   - Uses Dolt's "commit all" behavior under the hood (DOLT_COMMIT -Am).
//   - Treats "nothing to commit" as a no-op.
func maybeAutoCommit(ctx context.Context, p doltAutoCommitParams) error {
	if !isEmbeddedMode() {
		return nil
	}
	return maybeAutoCommitStore(ctx, getStore(), p)
}

func commitPendingIfEmbedded(ctx context.Context, st storage.DoltStorage, actor string, p doltAutoCommitParams) error {
	if !isEmbeddedMode() || st == nil {
		return nil
	}
	if strings.TrimSpace(p.MessageOverride) == "" {
		p.MessageOverride = formatDoltAutoCommitMessage(p.Command, actor, p.IssueIDs)
	}
	return maybeAutoCommitStore(ctx, st, p)
}

func maybeAutoCommitStore(ctx context.Context, st storage.DoltStorage, p doltAutoCommitParams) error {
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		return err
	}
	// In batch mode, skip per-command commits. Changes stay in the working set
	// and are committed at logical boundaries (bd dolt commit).
	if mode != doltAutoCommitOn {
		return nil
	}

	if st == nil {
		return nil
	}
	if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
		return nil
	}

	msg := p.MessageOverride
	if strings.TrimSpace(msg) == "" {
		msg = formatDoltAutoCommitMessage(p.Command, getActor(), p.IssueIDs)
	}

	if err := st.Commit(ctx, msg); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return err
	}
	return nil
}

func isDoltNothingToCommit(err error) bool {
	return issueops.IsNothingToCommitError(err)
}

func formatDoltAutoCommitMessage(cmd string, actor string, issueIDs []string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		cmd = "write"
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}

	ids := make([]string, 0, len(issueIDs))
	seen := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	slices.Sort(ids)

	const maxIDs = 5
	if len(ids) > maxIDs {
		ids = ids[:maxIDs]
	}

	if len(ids) == 0 {
		return fmt.Sprintf("bd: %s (auto-commit) by %s", cmd, actor)
	}
	return fmt.Sprintf("bd: %s (auto-commit) by %s [%s]", cmd, actor, strings.Join(ids, ", "))
}
