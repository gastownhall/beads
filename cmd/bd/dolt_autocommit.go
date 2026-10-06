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

// explicitCommitPointContext clears the dolt.auto-commit deferral for a proxied
// dual whose direct-route twin is an explicit commit point.
//
// The direct route encodes two commit classes, and it encodes them by which
// wrapper a verb picks: writes whose Dolt commit is auto-commit policy go
// through transactHonoringAutoCommit or issueOpsContext above, while the
// explicit commit points go through transact and mint a Dolt commit carrying
// the caller's message whatever the policy says. The proxied route has no such
// choice to make per verb — it applies the policy ONCE, to rootCtx in the root
// pre-run (GH#4995) — so the second class has to opt back out here, or
// doltServerTx.Commit blanks the message it was given: a proxied
// `bd batch -m "release batch"` under dolt.auto-commit=batch would persist the
// rows, discard the message and mint nothing, while the identical command and
// config on the direct route commits it.
//
// Scope is the intersection of two in-tree lists: the transact() call sites,
// and the paths the proxied front door permits (proxyPermittedPaths in
// capability_registry.go). That is batch, mol bond/pour/squash, mol wisp create
// and the WISP half of mol burn; cook and migrate issues also call transact but
// are refused in proxied mode, so they have no dual to exempt.
//
// Membership is per commit site, not per command: mol burn's persistent half
// reaches deleteBatch -> issueOpsContext on the direct route, which is the
// policy-honoring class, so runMolBurnProxiedServer exempts only the wisp
// transaction. TestProxiedDualsExemptTheirExplicitCommitPoints pins both arms.
func explicitCommitPointContext(ctx context.Context) context.Context {
	return issueops.WithImmediateVersionCommit(ctx)
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

// commitConfigWrite creates the Dolt commit for an intentional config-table
// write on the direct SQL-server route (bd config set/unset/set-many; bd
// remember/forget go through commitMemoryWrite). Nothing else commits it there
// (GH#4078): the store-level writes those verbs reach through their roles
// commit the SQL transaction only, maybeAutoCommit returns early off the
// embedded route, and plain Commit excludes config anyway (GH#2455).
// CommitConfigOnly stages ONLY the config table, so a concurrent operation's
// other dirty tables are never swept.
//
// It is a no-op wherever something else already owns that commit: embedded
// mode (the PersistentPostRun auto-commit stages config with everything
// else), the proxied route (the role's unit of work commits each write), and
// dolt.auto-commit batch/off, which defer it to `bd dolt commit` (CommitAll,
// config included) exactly as they defer every other server-mode write.
//
// Unlike commitMemoryWrite it does not screen other dirty config rows: the
// operator is writing config on purpose, the trust level of `bd dolt commit`.
// rename-prefix and migrate also write config (issue_prefix, sync.branch) and
// are deliberately not wired here: they are multi-step operations, and when
// their config row should become a commit is the GH#2455 question itself.
func commitConfigWrite(ctx context.Context, st storage.DoltStorage, command string) error {
	if err := commitConfigTable(ctx, st, command, storage.DoltStorage.CommitConfigOnly); err != nil {
		return fmt.Errorf("committing config write: %w", err)
	}
	return nil
}

// commitMemoryWrite is commitConfigWrite for bd remember/forget. A memory is
// user kv.* data, so its commit takes only kv.* config rows with it
// (CommitConfigUserKVOnly): a dirty internal key is refused, not swept in.
func commitMemoryWrite(ctx context.Context, st storage.DoltStorage, command string) error {
	if err := commitConfigTable(ctx, st, command, storage.DoltStorage.CommitConfigUserKVOnly); err != nil {
		return fmt.Errorf("committing memory write: %w", err)
	}
	return nil
}

func commitConfigTable(ctx context.Context, st storage.DoltStorage, command string,
	commit func(storage.DoltStorage, context.Context, string) error) error {
	if isEmbeddedMode() || usesProxiedServer() || st == nil {
		return nil
	}
	commitNow, err := writesCommitNow()
	if err != nil {
		return err
	}
	if !commitNow {
		return nil
	}
	if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
		return nil
	}
	msg := formatDoltAutoCommitMessage(command, getActor(), nil)
	if err := commit(st, ctx, msg); err != nil && !isDoltNothingToCommit(err) {
		return err
	}
	return nil
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
