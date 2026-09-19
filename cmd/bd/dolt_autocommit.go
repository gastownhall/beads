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
// part of command auto-commit policy. In batch/off modes the SQL transaction
// still commits, but no Dolt version commit is created — the blank message
// makes StageAndCommit a no-op, in embedded and SQL-server mode alike
// (bd-4wamg: batch used to be silently inert in server mode).
func transactHonoringAutoCommit(ctx context.Context, s storage.DoltStorage, commitMsg string, fn func(tx storage.Transaction) error) error {
	msg := commitMsg
	committedExplicitly := strings.TrimSpace(msg) != ""
	commitNow, err := writesCommitNow()
	if err != nil {
		return err
	}
	if !commitNow {
		msg = ""
		committedExplicitly = false
	}

	err = s.RunInTransaction(ctx, msg, fn)
	if err == nil && committedExplicitly {
		commandDidExplicitDoltCommit = true
	}
	return err
}

// writesCommitNow reports whether a CLI write should create its Dolt version
// commit as part of the write (mode "on"), rather than leaving it in the
// working set for a later explicit commit point (batch/off; bd dolt commit).
// An unset value means no Dolt store resolved a default (e.g. a non-Dolt
// backend), where per-write version commits are the only behavior that
// exists — treat it as "on".
func writesCommitNow() (bool, error) {
	if strings.TrimSpace(doltAutoCommit) == "" {
		return true, nil
	}
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		return false, err
	}
	return mode == doltAutoCommitOn, nil
}

// embeddedWritesCommitNow is writesCommitNow for the embedded-only commit
// points (the PersistentPostRun working-set flush and create's post-write
// flush). In SQL-server mode those flushes never run — mode "on" writes
// version themselves inside the storage layer.
func embeddedWritesCommitNow() (bool, error) {
	if !isEmbeddedMode() {
		return false, nil
	}
	return writesCommitNow()
}

// issueOpsContext applies command auto-commit policy to the context a write verb
// hands the issue-operations facade. The facade creates its Dolt version commit
// inside the storage layer, so batch mode cannot blank a commit message the way
// transactHonoringAutoCommit does — it has to say so on the context instead.
// This is mode-driven, not embedded-only: in SQL-server mode the storage
// layer's per-write commit sites honor the same deferral (bd-4wamg).
func issueOpsContext(ctx context.Context) (context.Context, error) {
	commitNow, err := writesCommitNow()
	if err != nil {
		return nil, err
	}
	if commitNow {
		return ctx, nil
	}
	return issueops.WithDeferredVersionCommit(ctx), nil
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
