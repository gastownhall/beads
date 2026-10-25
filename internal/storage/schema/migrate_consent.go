package schema

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Migration consent (gastownhall Beads 1.2 release remediation).
//
// bd used to auto-apply pending schema migrations the first time a new binary
// opened an existing database — on ANY command, including reads. The 1.2.x
// releases turned that into a trap: users upgraded by a package manager had
// their databases migrated in place with no prompt, and older binaries then
// refuse the migrated database (schema-skew guard). The remote-migrate gate
// (#4259) already refuses the silent migration for remote-backed databases;
// this gate extends the same contract to EVERY existing database: bd never
// initiates a schema migration without explicit operator consent.
//
// Consent is any of:
//   - `bd migrate schema` (the verb that names the migration; the root
//     command records the intent via SetLocalMigrateConsent before the store
//     opens — bare `bd migrate` reconciles metadata and does not consent),
//   - --force on `bd migrate` or `bd migrate schema` (the remote-gate
//     override, a strict superset), or
//   - BD_ALLOW_MIGRATE=1 (scripted/CI form; BD_ALLOW_REMOTE_MIGRATE=1 is
//     honored as an alias for continuity with the remote gate's hatch).
//
// Fresh databases (no schema_migrations table, version 0) are exempt: creating
// a database is consent for its schema. A server bootstrap holding
// fresh-bootstrap heal authority keeps that consent across a retry whose
// interrupted first pass left a non-zero cursor (MigrateUpWithLock). The same
// creation principle covers a workspace this invocation just CLONED into
// existence (`bd init --remote`, `bd bootstrap` sync) — those paths record the
// consent explicitly, and the #4259 remote-migrate gate still applies to them
// unchanged. Databases already at this binary's latest version never consult
// the gate. The ignored (clone-local, dolt_ignored) migration sequence is
// deliberately NOT gated on its own: it only materializes per-clone
// bookkeeping tables and runs on every fresh clone — but when the MAIN
// sequence is pending and unconsented, MigrateUp refuses before ANY write,
// ignored sequence and dolt_ignore seeding included.
//
// A refusal fails the open, except for the working-set-reconcile commands
// (bd dolt commit, bd vc commit), which commit at the current schema without
// migrating: the refusal comes before the #4566 dirty-table guard, whose
// documented recovery is that commit (WorkingSetWarning).

// AllowMigrateEnv, when set to a boolean true ("1", "true", ...), consents to
// applying pending schema migrations to an existing database. It is consulted
// only when migrations are actually pending, so exporting it permanently does
// not warn on every store open.
const AllowMigrateEnv = "BD_ALLOW_MIGRATE"

// localMigrateConsent is the programmatic consent recorded by
// `bd migrate schema` and by the paths that just cloned the database into
// existence. Process-local by design, like forceAllowRemoteMigrate: it cannot
// leak into child processes.
var localMigrateConsent bool

// SetLocalMigrateConsent records (or clears) the in-process consent to apply
// pending schema migrations to an existing database. Set by the root command
// when the invoked command is `bd migrate schema`, before both
// autoMigrateOnVersionBump and the main store open. External test packages may
// reset it to false after each test case.
func SetLocalMigrateConsent(v bool) { localMigrateConsent = v }

// MigrateConsentError is returned when bd would apply pending schema
// migrations to an existing database without operator consent.
type MigrateConsentError struct {
	CurrentVersion int
	LatestVersion  int
	Pending        int
	// UnrecognizedEnv carries a BD_ALLOW_MIGRATE value that was set but not
	// understood (only boolean values consent), so a typo'd hatch fails with
	// a hint instead of silently staying locked (0054-gate precedent,
	// bd-6dnrw.34).
	UnrecognizedEnv string
}

func (e *MigrateConsentError) Error() string {
	return fmt.Sprintf("%s — run `bd migrate schema`, or keep using a bd release that matches schema v%d",
		e.Refusal(), e.CurrentVersion)
}

// Refusal states what was refused without prescribing a remedy, for surfaces
// that carry the remedy separately and must not hand an agent a runnable
// migration (cmd/bd's JSON refusal, whose remedies live in Options).
func (e *MigrateConsentError) Refusal() string {
	return fmt.Sprintf(
		"database schema is at v%d; this bd requires v%d (%d pending migration(s)), and bd no longer migrates a database without explicit consent",
		e.CurrentVersion, e.LatestVersion, e.Pending)
}

// AgentDirective is the non-runnable instruction surfaced to agents in place
// of a ready-to-run migrate command, following the remote-migrate gate's
// convention (RemoteMigrateGateError.AgentDirective). Migrating is one-way,
// and whether anything still has to open this database with an older bd is
// not observable from this process, so the decision goes to the operator.
func (e *MigrateConsentError) AgentDirective() string {
	return fmt.Sprintf(
		"Operator decision required: this database is at schema v%d and this bd requires v%d. Migrating is ONE-WAY — "+
			"bd releases built for an older schema refuse the migrated database, and whether one still has to open it "+
			"is not observable from this process. Do NOT auto-run the migration or set a consent environment variable "+
			"to get past this refusal: surface migrate_consent.options to the operator and let them choose.",
		e.CurrentVersion, e.LatestVersion)
}

// Options returns the two mutually exclusive paths, migrate or keep the
// current schema, each gated on its precondition and annotated with its risk.
// The migrate command is reachable only through the operator's decision,
// never as a top-level hint. Its verb is the shared-store gate's consent verb
// too, so cmd/bd retargets both with the same --global rewrite.
func (e *MigrateConsentError) Options() []GateOption {
	return []GateOption{
		{
			ID:       "migrate",
			When:     "the operator chooses to upgrade this database, and nothing that still opens it runs a bd release built for an older schema (confirmed with the operator)",
			Commands: []string{SharedConsentCommand},
			Risk:     fmt.Sprintf("one-way: v%d -> v%d; bd releases built for an older schema refuse the migrated database", e.CurrentVersion, e.LatestVersion),
		},
		{
			ID:       "keep",
			When:     fmt.Sprintf("something still has to open this database with a bd release built for schema v%d", e.CurrentVersion),
			Commands: []string{},
			Risk:     "none to the database; this bd release refuses it until it is migrated, so keep using the release that matches it",
		},
	}
}

// UserMessage renders the full operator-facing refusal.
func (e *MigrateConsentError) UserMessage() string {
	msg := fmt.Sprintf(
		"This database is at schema v%d; this bd release uses schema v%d.\n"+
			"bd no longer upgrades a database automatically (see the release notes).\n"+
			"\n"+
			"  To upgrade this database (one-way; older bd releases cannot read it):\n"+
			"      bd migrate schema\n"+
			"\n"+
			"  To keep the current schema, keep using the bd release that created it.\n"+
			"\n"+
			"  Scripted/CI consent: BD_ALLOW_MIGRATE=1 bd <command>\n",
		e.CurrentVersion, e.LatestVersion)
	if e.UnrecognizedEnv != "" {
		msg += fmt.Sprintf(
			"\nNote: %s=%q was set but not understood (use a boolean value, e.g. \"1\" or \"true\"), so it did not consent.\n",
			AllowMigrateEnv, e.UnrecognizedEnv)
	}
	return msg
}

// WorkingSetWarning renders the stderr warning for a working-set-reconcile
// open (bd dolt commit, bd vc commit) that continues past this refusal. The
// commit needs no migration, and MigrateUp refused before any write, so it
// runs on the current schema; the migration still waits for consent. Both
// storage modes print it (embeddeddolt's openWorkingSetReconcile, server
// mode's LenientOpen).
func (e *MigrateConsentError) WorkingSetWarning() string {
	return fmt.Sprintf(
		"Warning: %s.\n"+
			"  Working-set reconcile command: continuing on schema v%d without\n"+
			"  migrating; the commit applies to the working set at the current schema.\n"+
			"  To upgrade this database afterwards (one-way): bd migrate schema\n",
		e.Refusal(), e.CurrentVersion)
}

// IsMigrateConsentError reports whether err (or any error it wraps) is a
// *MigrateConsentError.
func IsMigrateConsentError(err error) bool {
	var e *MigrateConsentError
	return errors.As(err, &e)
}

// checkMigrateConsent refuses an unconsented migration of an existing
// database. Called at the top of MigrateUp, before any write (dolt_ignore
// seeding included). Returns nil for a fresh database (version 0), a database
// with no pending main-sequence migrations, or when consent was given.
func checkMigrateConsent(ctx context.Context, db DBConn) error {
	if localMigrateConsent || forceAllowRemoteMigrate {
		return nil // programmatic consent — skip the version reads entirely
	}
	current, err := mainSource.currentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate consent: read current version: %w", err)
	}
	if current == 0 {
		return nil // fresh database — creating it is consent for its schema
	}
	pending, err := mainSource.pendingVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate consent: read pending versions: %w", err)
	}
	return migrateConsentDecision(current, len(pending))
}

// migrateConsentDecision is checkMigrateConsent's pure decision core: nil when
// nothing is pending or consent was given (programmatic or env), a typed
// *MigrateConsentError otherwise. current is known to be > 0 here except when
// callers short-circuit earlier; a zero current is still treated as consent
// (fresh database).
func migrateConsentDecision(current, pending int) error {
	if current == 0 || pending == 0 {
		return nil
	}
	if localMigrateConsent || forceAllowRemoteMigrate {
		return nil
	}
	unrecognized := ""
	for _, env := range []string{AllowMigrateEnv, AllowRemoteMigrateEnv} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		allowed, perr := strconv.ParseBool(v)
		if perr != nil {
			if env == AllowMigrateEnv {
				unrecognized = v
			}
			continue
		}
		if allowed {
			fmt.Fprintf(defaultStderr(),
				"Applying %d pending schema migration(s) (%s=%s): v%d -> v%d\n",
				pending, env, v, current, LatestVersion())
			return nil
		}
	}
	return &MigrateConsentError{
		CurrentVersion:  current,
		LatestVersion:   LatestVersion(),
		Pending:         pending,
		UnrecognizedEnv: unrecognized,
	}
}
