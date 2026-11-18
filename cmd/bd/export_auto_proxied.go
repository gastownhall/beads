package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// proxiedAutoExportBackendForCommand returns the backend post-command
// auto-export reads through on a proxied-server workspace, or nil when it
// must not run at all (GH#8490).
//
// There is no topology gate here, unlike proxiedAutoBackupBackend: auto-backup
// registers a server-global backup remote, export only reads SQL and writes a
// client-local file, so every proxied topology may run it.
//
// It never opens a provider or connection of its own — a command that did not
// open one has nothing to read through. Skips are a debug log and a nil
// backend (a silent no-op is the fix's one regression risk, so every skip is
// at least diagnosable with BD_DEBUG):
//
//   - no provider: the command never opened one (or closed it, as restore
//     does);
//   - no maintenance seam: a provider decorator dropped RunNonTx, the same
//     assertion runProxiedNonTx makes for every other maintenance user;
//   - a database override (--database/--db): the JSONL and its export-state
//     belong to the workspace's configured database, not to the one this
//     command happened to serve.
func proxiedAutoExportBackendForCommand() autoExportBackend {
	if uowProvider == nil {
		debug.Logf("auto-export: skipping — no proxied provider this run\n")
		return nil
	}
	mp, ok := uowProvider.(uow.MaintenanceProvider)
	if !ok {
		debug.Logf("auto-export: skipping — provider %T has no maintenance seam\n", uowProvider)
		return nil
	}
	if commandDatabaseOverride != "" {
		debug.Logf("auto-export: skipping — provider opened with --database %q; the JSONL belongs to the workspace's configured database\n", commandDatabaseOverride)
		return nil
	}
	return proxiedAutoExportBackend{provider: uowProvider, mp: mp}
}

// proxiedAutoExportBackend is the auto-export backend over the provider this
// command opened.
//
// Versioning reads (state hash, diff anchor, the dolt_diff and history
// deletion proofs) run on the provider's non-transactional seam — the same
// pinned-connection seam proxiedLocalBackup uses — through the same
// conn-level helpers DoltStore delegates to, so both modes run identical SQL.
// Issue-data reads (guards and writer) run inside ONE read unit of work, the
// same snapshot discipline proxied `bd export` uses, so the guards and the
// writer see one consistent view and the output is byte-identical to classic
// mode.
//
// It offers no incremental capability (no issuesByIDs): a hash change always
// takes the full exportToFile branch. The dolt_diff machinery is still
// exposed — proveDeletedIssueIDs' P2 needs it.
type proxiedAutoExportBackend struct {
	provider uow.UnitOfWorkProvider
	mp       uow.MaintenanceProvider
}

func (b proxiedAutoExportBackend) stateHash(ctx context.Context) (string, error) {
	var hash string
	err := b.mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		hash, err = versioncontrolops.StateHashWithHeadFallback(ctx, conn)
		return err
	})
	return hash, err
}

func (b proxiedAutoExportBackend) currentCommit(ctx context.Context) (string, error) {
	var hash string
	err := b.mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		hash, err = versioncontrolops.CurrentCommit(ctx, conn)
		return err
	})
	return hash, err
}

func (b proxiedAutoExportBackend) commitExists(ctx context.Context, commitHash string) (bool, error) {
	var exists bool
	err := b.mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		exists, err = versioncontrolops.CommitExists(ctx, conn, commitHash)
		return err
	})
	return exists, err
}

func (b proxiedAutoExportBackend) changedIssueIDs(ctx context.Context, fromCommit, toCommit string) (storage.ChangedIssueIDs, error) {
	var changed storage.ChangedIssueIDs
	err := b.mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		changed, err = issueops.ChangedIssueIDsInTx(ctx, conn, fromCommit, toCommit)
		return err
	})
	return changed, err
}

func (b proxiedAutoExportBackend) historicalIssueIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	var present map[string]struct{}
	err := b.mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		present, err = issueops.HistoricalIssueIDsInTx(ctx, conn, ids)
		return err
	})
	return present, err
}

// read runs fn inside one read unit of work over the provider. RunTxRead
// never commits, so the guards and the writer below read one consistent
// snapshot — the same shape proxied `bd export` runs under its
// byte-identity oracle.
func (b proxiedAutoExportBackend) read(ctx context.Context, fn func(ctx context.Context, r autoExportReader) error) error {
	_, err := uow.RunTxRead(ctx, b.provider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
		return struct{}{}, fn(ctx, &uowAutoExportReader{&uowExportSource{uw: uw}})
	})
	return err
}

// uowAutoExportReader is the proxied autoExportReader: the exportSource
// proxied `bd export` already reads through, plus the issue search the
// guards and the full writer use.
type uowAutoExportReader struct {
	*uowExportSource
}

// searchIssues searches both planes through the unit of work's issue use
// case, the same reader DoltStore's SearchIssues feeds from. It FAILS on a
// truncated page instead of returning a short set: every caller here is a
// data-integrity guard or the export writer itself, and a silent short set
// is exactly the torn store they exist to refuse.
func (r *uowAutoExportReader) searchIssues(ctx context.Context, filter types.IssueFilter) ([]*types.Issue, error) {
	page, err := r.uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}
	if page.HasMore {
		return nil, fmt.Errorf("search issues: result truncated (%d rows with more beyond the limit); refusing to export a partial set", len(page.Items))
	}
	return page.Items, nil
}
