package schema

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// AllowRemoteMigrateEnv, when set to a boolean true ("1", "true", ...), lets
// the designated migrator apply pending schema migrations to a remote-backed
// database despite the gate below. It is consulted only when the gate would
// otherwise fire, so exporting it permanently does not warn on every store open.
const AllowRemoteMigrateEnv = "BD_ALLOW_REMOTE_MIGRATE"

// RemoteMigrateGateError is returned when bd is about to auto-apply pending
// schema migrations to an existing database that has a remote configured.
//
// gastownhall/beads#4259: bd auto-runs pending migrations the first time a new
// binary opens an existing database. If two clones that sync through a shared
// remote each upgrade independently, both migrate in place and the schema forks
// — `bd dolt pull` then fails to merge with no bd-level recovery. The supported
// flow is "only ONE machine migrates the database; every other client adopts the
// migrated database from the remote". This gate refuses the silent in-place
// migration and makes the operator choose migrate vs. adopt. It applies to both
// server mode and embedded mode (the mode the original report was filed against).
type RemoteMigrateGateError struct {
	CurrentVersion int
	LatestVersion  int
	Pending        int
	// UnrecognizedEnv carries a BD_ALLOW_REMOTE_MIGRATE value that was set but
	// not understood (only boolean values unlock, e.g. "1"/"true"), so a
	// typo'd escape hatch fails with a hint instead of silently staying locked
	// (bd-6dnrw.34).
	UnrecognizedEnv string

	// Decision records why the smart gate (#4516) stopped, when it is enabled.
	// Empty is the default blunt #4515 stop (also used for the smart gate's
	// "undetermined"/"below-floor" fallbacks); the messaging below is then
	// byte-identical to #4515. "adopt" and "fork-skew" tailor the guidance.
	Decision string
	// SkewVersions lists the migration versions whose content diverged between
	// this clone and the remote (Decision == "fork-skew").
	SkewVersions []int
}

const (
	gateDecisionAdopt    = "adopt"
	gateDecisionForkSkew = "fork-skew"
)

func (e *RemoteMigrateGateError) Error() string {
	unit := "migrations"
	if e.Pending == 1 {
		unit = "migration"
	}
	switch e.Decision {
	case gateDecisionAdopt:
		return fmt.Sprintf("refusing to migrate a remote-backed database (v%d -> v%d): the remote is already migrated — adopt it instead of migrating here (#4259)",
			e.CurrentVersion, e.LatestVersion)
	case gateDecisionForkSkew:
		return fmt.Sprintf("refusing to migrate a remote-backed database (v%d -> v%d): this clone and the remote applied different content for migration(s) %s — the schema has already forked (#4259)",
			e.CurrentVersion, e.LatestVersion, FormatMigrationVersions(e.SkewVersions))
	default:
		return fmt.Sprintf("refusing to auto-apply %d pending schema %s to a remote-backed database (v%d -> v%d): migrating clones independently forks the schema (#4259)",
			e.Pending, unit, e.CurrentVersion, e.LatestVersion)
	}
}

// FormatMigrationVersions renders migration versions as zero-padded 4-digit ids.
func FormatMigrationVersions(versions []int) string {
	if len(versions) == 0 {
		return ""
	}
	parts := make([]string, len(versions))
	for i, v := range versions {
		parts[i] = fmt.Sprintf("%04d", v)
	}
	return strings.Join(parts, ", ")
}

// UserMessage returns the full multi-line error block for terminal output.
func (e *RemoteMigrateGateError) UserMessage() string {
	msg := e.Error() + "\n" + e.userBody()
	if e.UnrecognizedEnv != "" {
		msg += "\n" +
			"  Note: " + AllowRemoteMigrateEnv + "=" + e.UnrecognizedEnv + " is set but was not recognized —\n" +
			"  use " + AllowRemoteMigrateEnv + "=1 to unlock.\n"
	}
	return msg
}

// userBody returns the decision-specific guidance block. The default (blunt
// #4515) body is byte-identical to before the smart gate existed.
func (e *RemoteMigrateGateError) userBody() string {
	switch e.Decision {
	case gateDecisionAdopt:
		return "\n" +
			"  The remote has already been migrated by another clone. Do NOT migrate here —\n" +
			"  adopt the remote's migrated database instead:\n" +
			"        bd bootstrap\n" +
			"  Re-cloning replaces your local database: any local issues you have not pushed\n" +
			"  are LOST. Push first (`bd dolt push`) or save a copy\n" +
			"  (`bd export --all -o backup.jsonl`) before re-cloning.\n"
	case gateDecisionForkSkew:
		return "\n" +
			"  This clone and the remote already applied DIFFERENT content for migration(s) " +
			FormatMigrationVersions(e.SkewVersions) + ".\n" +
			"  The schema has forked (#4259); `bd dolt pull` can no longer merge. Migrating\n" +
			"  cannot un-fork it. This is a data-loss decision, not an auto-fix:\n" +
			"    • Pick ONE clone as canonical, then re-bootstrap every other clone from it —\n" +
			"      unpushed work on the discarded clones is LOST. Export it first\n" +
			"      (`bd export --all -o backup.jsonl`) if you need it.\n" +
			"        bd bootstrap\n"
	default:
		return "\n" +
			"  This database syncs with a remote. Applying schema migrations on more than\n" +
			"  one clone independently forks the schema so `bd dolt pull` can no longer\n" +
			"  merge — the break is silent and unrecoverable.\n" +
			"\n" +
			"  Choose one:\n" +
			"    • You are the designated migrator (only ONE machine should be): migrate,\n" +
			"      then publish the migrated database to the remote:\n" +
			"        " + AllowRemoteMigrateEnv + "=1 bd migrate\n" +
			"        bd dolt push\n" +
			"    • Another machine has already migrated: adopt its database instead of\n" +
			"      migrating here — re-clone from the remote so you receive the migrated\n" +
			"      schema:\n" +
			"        bd bootstrap\n" +
			"      Re-cloning replaces your local database: any local issues you have not\n" +
			"      pushed are LOST. Push first (`bd dolt push`) or save a copy\n" +
			"      (`bd export --all -o backup.jsonl`) before re-cloning.\n"
	}
}

// EscapeHint returns the escape-hatch string for JSON error output.
func (e *RemoteMigrateGateError) EscapeHint() string {
	return AllowRemoteMigrateEnv + "=1 bd migrate"
}

// AgentDirective is the non-runnable instruction surfaced to agents in place of
// a ready-to-run migrate command. Migrating a shared remote is a coordination
// decision — only ONE clone may migrate, and a second clone migrating
// independently forks the schema unrecoverably (#4259) — so bd deliberately does
// NOT hand an agent an auto-runnable "fix". The agent should surface the options
// to the operator and let them choose, per the AgentDiagnostic contract ("Go
// observes and reports, the agent decides and acts").
func (e *RemoteMigrateGateError) AgentDirective() string {
	switch e.Decision {
	case gateDecisionAdopt:
		return "Adopt, do NOT migrate: the remote is already migrated, so migrating here would fork it (#4259). " +
			"Adoption re-clones and replaces the local database (unpushed local work is lost), so it is a destructive " +
			"operator decision — surface remote_migrate_gate.options to the operator, do NOT auto-run bd bootstrap."
	case gateDecisionForkSkew:
		return "Do NOT migrate: this clone and the remote already applied different content for migration(s) " +
			FormatMigrationVersions(e.SkewVersions) + " — the schema has forked (#4259) and migrating cannot un-fork it. " +
			"Resolving requires picking a canonical clone and re-bootstrapping the others, discarding their unpushed " +
			"work — a data-loss decision. Surface remote_migrate_gate.options to the operator; do NOT auto-run anything."
	default:
		return "Coordination decision required: only ONE clone may migrate a shared remote; " +
			"a second clone migrating independently forks the schema unrecoverably (#4259). " +
			"Do NOT auto-run a migration — surface remote_migrate_gate.options to the operator and let them choose."
	}
}

// GateOption is one conditional remediation path for the remote-migrate gate.
// It is intentionally conditional (When) and carries its Risk, so an agent
// cannot treat any single command as the unconditional fix.
type GateOption struct {
	ID       string   `json:"id"`
	When     string   `json:"when"`
	Commands []string `json:"commands"`
	Risk     string   `json:"risk"`
}

// Options returns the two mutually-exclusive remediation paths — migrate (as the
// single designated migrator) or adopt (re-clone the already-migrated DB) — each
// gated on its precondition and annotated with its risk. The migrate command is
// present but reachable only through its "single designated migrator" condition,
// never as a top-level hint.
func (e *RemoteMigrateGateError) Options() []GateOption {
	adopt := GateOption{
		ID:       "adopt",
		When:     "another machine has already migrated and pushed",
		Commands: []string{"bd bootstrap"},
		Risk:     "re-clones and replaces the local database; push or export unpushed work first or it is lost",
	}
	switch e.Decision {
	case gateDecisionAdopt:
		// Remote is confirmed ahead: migrate is not a valid path, only adopt.
		adopt.When = "the remote is already migrated (confirmed by the gate) — adopt it"
		return []GateOption{adopt}
	case gateDecisionForkSkew:
		// Already forked: neither migrate nor a plain adopt is unconditionally
		// safe; the operator must choose a canonical clone first.
		return []GateOption{{
			ID:       "reconcile-fork",
			When:     "the schema has already forked (different content for migration(s) " + FormatMigrationVersions(e.SkewVersions) + "); choose ONE clone as canonical",
			Commands: []string{"bd export --all -o backup.jsonl", "bd bootstrap"},
			Risk:     "re-bootstrapping the non-canonical clones discards their unpushed work; export it first",
		}}
	default:
		return []GateOption{
			{
				ID:       "migrate",
				When:     "you are the single designated migrator (only ONE machine, confirmed with the operator) and no other clone has migrated yet",
				Commands: []string{AllowRemoteMigrateEnv + "=1 bd migrate", "bd dolt push"},
				Risk:     "if another clone also migrates independently, the schema forks unrecoverably (#4259)",
			},
			adopt,
		}
	}
}

// IsRemoteMigrateGateError reports whether err (or any error it wraps) is a
// *RemoteMigrateGateError.
func IsRemoteMigrateGateError(err error) bool {
	var e *RemoteMigrateGateError
	return errors.As(err, &e)
}

// CheckRemoteMigrateGate refuses to auto-apply pending schema migrations when the
// database already has a recorded schema version, has pending migrations, and has
// a remote configured — unless the designated-migrator escape hatch is set. It
// returns nil (allow) for a fresh database, an already-current database, or one
// with no remote. Call it before MigrateUp/MigrateUpWithLock on every read/write
// store open. Embedded mode uses this form: its dolt_remotes table already
// reflects remotes persisted in .dolt/config on a fresh open.
func CheckRemoteMigrateGate(ctx context.Context, db DBConn) error {
	return checkRemoteMigrateGate(ctx, db, "", nil)
}

// CheckRemoteMigrateGateWithRemoteCheck is CheckRemoteMigrateGate plus an on-disk
// fallback remote probe. When the dolt_remotes SQL table reports no remote,
// extraHasRemote is consulted and a true result still trips the gate.
//
// Server mode needs this: a freshly (auto-)started dolt sql-server starts with an
// empty dolt_remotes table and only re-registers CLI remotes from .dolt/config
// later, during the post-open sync (GH#2315). Because this gate runs before that
// sync, the SQL-only check would see no remote on the first write open after an
// upgrade and silently migrate the shared database in place — exactly the
// cross-clone fork #4259 is meant to prevent. extraHasRemote (a probe of the
// persisted CLI remotes) closes that window.
//
// extraHasRemote is only invoked when the database has a pending migration AND the
// SQL table shows no remote, so the (subprocess-backed) filesystem probe stays off
// the common open path. A nil extraHasRemote disables the fallback.
func CheckRemoteMigrateGateWithRemoteCheck(ctx context.Context, db DBConn, extraHasRemote func() bool) error {
	return checkRemoteMigrateGate(ctx, db, "", extraHasRemote)
}

// CheckRemoteMigrateGateForRemoteWithRemoteCheck is CheckRemoteMigrateGate plus
// an explicit sync remote name for the smart gate's cached remote-ref read.
// The blunt gate still trips when any Dolt remote exists; the remote name only
// chooses which remote-tracking ref the opt-in smart router compares against.
func CheckRemoteMigrateGateForRemoteWithRemoteCheck(ctx context.Context, db DBConn, remoteName string, extraHasRemote func() bool) error {
	return checkRemoteMigrateGate(ctx, db, remoteName, extraHasRemote)
}

func checkRemoteMigrateGate(ctx context.Context, db DBConn, remoteName string, extraHasRemote func() bool) error {
	// CurrentVersion treats a missing schema_migrations table as version 0, so a
	// brand-new database falls through the current==0 check below — nothing to fork.
	current, err := CurrentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read current version: %w", err)
	}
	if current == 0 {
		return nil // fresh database — nothing to fork
	}

	pending, err := PendingVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read pending versions: %w", err)
	}
	if len(pending) == 0 {
		return nil // already current — nothing to migrate
	}

	hasRemote, err := anyDoltRemoteConfigured(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read remotes: %w", err)
	}
	// dolt_remotes can read empty even when a remote is configured: a freshly
	// (auto-)started server has not yet synced CLI remotes from .dolt/config
	// (GH#2315). Consult the caller's on-disk probe before allowing migration.
	if !hasRemote && extraHasRemote != nil {
		hasRemote = extraHasRemote()
	}
	if !hasRemote {
		return nil // no remote — no cross-clone fork risk
	}

	// Escape hatch — consulted only once the gate would actually fire, so an
	// operator who exports it in a shell profile is not warned on every store
	// open with nothing pending or no remote (bd-6dnrw.34). Any boolean true
	// ("1", "true", "TRUE", ...) unlocks; a set-but-unparseable value is
	// surfaced in the gate error instead of silently staying locked.
	unrecognizedEnv := ""
	if v := os.Getenv(AllowRemoteMigrateEnv); v != "" {
		if allowed, perr := strconv.ParseBool(v); perr == nil {
			if allowed {
				fmt.Fprintf(os.Stderr,
					"Warning: applying %d pending schema migration(s) to a remote-backed database (%s=%s); only one clone should migrate, then `bd dolt push`\n",
					len(pending), AllowRemoteMigrateEnv, v)
				return nil
			}
		} else {
			unrecognizedEnv = v
		}
	}

	latest := LatestVersion()

	// Smart gate (#4516): on by default, BD_SMART_GATE=0 opts out. Once the
	// blunt gate would fire and the designated-migrator escape hatch is not
	// set, consult the remote's cached schema state and auto-resolve the one
	// provably-safe case (first-mover migrate). The undetermined/below-floor
	// verdicts fall through to the blunt #4515 block below, so opting out of
	// smart mode (or an unreadable remote ref) is always at least as safe as
	// before.
	if SmartGateEnabled() {
		decision, skew := routeSmartGate(ctx, db, current, remoteName)
		switch decision {
		case smartAutoMigrate:
			smartGateAllowMigrate(len(pending), current)
			return nil
		case smartAdopt:
			return &RemoteMigrateGateError{
				CurrentVersion:  current,
				LatestVersion:   latest,
				Pending:         len(pending),
				UnrecognizedEnv: unrecognizedEnv,
				Decision:        gateDecisionAdopt,
			}
		case smartForkSkew:
			return &RemoteMigrateGateError{
				CurrentVersion:  current,
				LatestVersion:   latest,
				Pending:         len(pending),
				UnrecognizedEnv: unrecognizedEnv,
				Decision:        gateDecisionForkSkew,
				SkewVersions:    skew,
			}
		case smartUndetermined, smartBelowFloor:
			// fall through to the blunt block
		}
	}

	return &RemoteMigrateGateError{
		CurrentVersion:  current,
		LatestVersion:   latest,
		Pending:         len(pending),
		UnrecognizedEnv: unrecognizedEnv,
	}
}

// anyDoltRemoteConfigured reports whether the database has any Dolt remote
// registered. dolt_remotes is always present in a Dolt database; a
// "table not found" is treated as "no remotes" so a missing system table can
// never wedge every store open.
func anyDoltRemoteConfigured(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_remotes").Scan(&count); err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
