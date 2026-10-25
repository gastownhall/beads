package doctor

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/schema"
)

// FixGate reports whether `bd doctor --fix` is safe given the schema/binary
// relationship (GH#4993): opening the store can apply pending migrations.
// Callers ask three distinct questions, so it answers three.
type FixGate struct {
	// Determined is false when a database exists but its version could not be
	// read — either the connection failed or the query did. Unknown is not safe.
	Determined bool
	// DBReachable is false whenever this process did not get a working
	// connection. That covers both "there is no database" and "there is one but
	// it could not be connected to", so it is not on its own a statement about
	// hazard — see DBPresent.
	DBReachable bool
	// DBPresent is true when a beads database exists for this repo: either the
	// connection succeeded, or a connection was impossible but the on-disk
	// database is still there (a stopped server, a wrong port). It is the hazard
	// question — a database that exists has a schema that a migrating open can
	// rewrite, whether or not this process could read it. Without a connection,
	// "exists" is databaseExistsOnDisk's verdict, and a workspace with no
	// metadata.json reads as absent there even with a data directory on disk —
	// see that function for why. DBReachable implies DBPresent; the converse
	// does not hold.
	DBPresent bool

	// RecommendFix allows printed advice to steer at `bd doctor --fix`.
	RecommendFix bool
	// AllowDBFix allows repairs that may write schema or resume migrations.
	AllowDBFix bool
	// AllowFSFix allows filesystem-only repairs, which never touch the schema.
	AllowFSFix bool

	DBVersion     int // max applied schema_migrations version (0 if unknown)
	BinaryVersion int // schema.LatestVersion() for this binary
	Reason        string
	Ahead         bool // DB schema newer than this binary
	Pending       bool // binary knows migrations the DB has not applied
}

// AssessSchemaFixGate compares schema_migrations to this binary. It yields one
// memoised verdict per invocation, evaluated before any *gated* write and
// threaded through to every consumer: probing at print time cannot guard writes
// that already happened. The bd-jgxi auto-migrate that runs earlier in the
// command (cmd/bd/version_tracking.go) is deliberately outside this gate's
// jurisdiction — it is the sanctioned migration path, not a repair the gate
// withholds — so "before any gated write" is the contract, not "before
// anything that touches the schema".
func AssessSchemaFixGate(path string) FixGate {
	binary := schema.LatestVersion()

	// No database at all: scoped allowance, not a fail-open — DB fixes stay
	// disallowed because there is no DB to fix, and no schema exists to skew.
	unreachable := FixGate{
		BinaryVersion: binary,
		Determined:    true,
		RecommendFix:  true,
		AllowFSFix:    true,
	}

	beadsDir := ResolveBeadsDirForRepo(path)
	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		// A failed connection is not evidence that there is no database. When
		// the database is still on disk — a stopped server, a moved port — the
		// schema hazard is real and unmeasured: every gated repair opens the
		// store through the migrating factory, which auto-starts the server and
		// applies pending migrations before writing. Fail closed on that, and
		// keep the genuine no-database case above as the only no-hazard shape.
		// Recovery fixers stay available either way (AllowsFix keys them on
		// DBReachable, which is false here too) because they are the cure for a
		// database that cannot be opened.
		if !databaseExistsOnDisk(beadsDir) {
			return unreachable
		}
		return FixGate{
			BinaryVersion: binary,
			DBPresent:     true,
			AllowFSFix:    true,
			Reason: "database exists but could not be opened, so its schema version is unknown; " +
				"`bd doctor --fix` may apply migrations blind — start the database and re-run",
		}
	}
	defer db.Close()

	// Reachable but unreadable: fail closed rather than claim safety.
	undetermined := FixGate{
		BinaryVersion: binary,
		DBReachable:   true,
		DBPresent:     true,
		AllowFSFix:    true,
		Reason: "database schema version could not be determined; " +
			"`bd doctor --fix` may apply migrations blind — resolve the database state first",
	}

	ctx := context.Background()
	dbVer, err := schema.CurrentVersion(ctx, db)
	if err != nil || dbVer == 0 {
		return undetermined
	}

	gate := FixGate{
		Determined:    true,
		DBReachable:   true,
		DBPresent:     true,
		DBVersion:     dbVer,
		BinaryVersion: binary,
		AllowFSFix:    true,
	}

	switch {
	case dbVer > binary:
		gate.Ahead = true
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary knows up to v%d (%s ahead). "+
				"`bd doctor --fix` can write/migrate and is unsafe on a newer schema — upgrade bd first",
			dbVer, binary, pluralMigrations(dbVer-binary),
		)
	case dbVer < binary:
		gate.Pending = true
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary expects v%d (%s pending). "+
				"`bd doctor --fix` may apply write migrations — upgrade/migrate deliberately, not as a cosmetic tip",
			dbVer, binary, pluralMigrations(binary-dbVer),
		)
	default:
		gate.RecommendFix = true
		gate.AllowDBFix = true
	}

	return gate
}

// databaseExistsOnDisk reports whether a beads database is present for this
// workspace, independent of whether anything can currently connect to it. It
// stats the path autoMigrateOnVersionBump stats (cmd/bd/version_tracking.go)
// and, like it, reads only ENOENT as absent: a path that cannot be stat'ed for
// any other reason is unknown, and unknown is not safe. An unloadable
// metadata.json reads as absent in both, which is safe: the store factory
// refuses to open such a workspace at all.
//
// It departs from that probe deliberately in two arms:
//
//   - No metadata.json reads as absent, where auto-migrate falls back to
//     DefaultConfig. The gate cannot connect without metadata.json (openDoltDB
//     requires it), so a fail-closed verdict here could never be cleared by
//     starting a server, and it would rewrite doctor's own advice for this
//     state — regenerate metadata.json with `bd doctor --fix`, a
//     filesystem-only repair — into "Do NOT run 'bd doctor --fix'". The cost is
//     one unguarded state: server mode selected by the environment or
//     config.yaml while metadata.json is missing, where a gated repair still
//     reaches the server's schema unmeasured.
//   - A proxied-server workspace reads as present when its data directory is
//     on disk. Auto-migrate skips proxied workspaces because their migration
//     runs later, at UOW-provider init, not because they lack a local database:
//     the proxied root defaults to this same directory. `bd doctor` itself is
//     refused up front in proxied-server mode (proxy.doctor.unsupported); this
//     arm fails closed so that it is already safe when doctor gains a proxied
//     route.
func databaseExistsOnDisk(beadsDir string) bool {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return false
	}
	if _, err := os.Stat(cfg.DatabasePath(beadsDir)); os.IsNotExist(err) {
		return false
	}
	return true
}

// BlocksDestructiveWrites reports whether an operation that can resume
// migrations must be refused outright. It is the hazard question rather than the
// connectivity one: a database that exists but whose schema state does not
// permit schema-writing repair blocks, whether or not this process reached it.
// A workspace with no database never blocks — there is nothing to skew.
func (g FixGate) BlocksDestructiveWrites() bool {
	return g.DBPresent && !g.AllowDBFix
}

func pluralMigrations(n int) string {
	if n == 1 {
		return "1 migration"
	}
	return fmt.Sprintf("%d migrations", n)
}

// fixAdvicePattern matches a tip steering at `bd doctor --fix`, tolerating flag
// order and spacing. Best-effort: it is still a sniff over free-form prose.
var fixAdvicePattern = regexp.MustCompile(`(?i)\bdoctor\b[^\n]*--fix\b`)

// originalTipMarker prefixes preserved advice in a rewritten tip, and doubles
// as the idempotence sentinel.
const originalTipMarker = "Original tip was: "

// MentionsFixAdvice reports whether a tip steers at `bd doctor --fix`.
func MentionsFixAdvice(fix string) bool {
	return fixAdvicePattern.MatchString(fix)
}

// SanitizeFixRecommendation rewrites Fix text that steers into `bd doctor
// --fix` when the gate says that is unsafe (GH#4993). Apply once to the
// result, not per-renderer — see sanitizeFixAdvice in cmd/bd.
func SanitizeFixRecommendation(fix string, gate FixGate) string {
	if gate.RecommendFix || fix == "" || !MentionsFixAdvice(fix) {
		return fix
	}
	// The --fix path sanitizes again after re-running diagnostics.
	if strings.Contains(fix, originalTipMarker) {
		return fix
	}

	switch {
	case gate.Ahead:
		return fmt.Sprintf("Do NOT run 'bd doctor --fix' until bd is upgraded (%s). %s%s",
			gate.Reason, originalTipMarker, fix)
	case gate.Pending:
		return fmt.Sprintf(
			"Avoid 'bd doctor --fix' for cosmetic repair while schema migrations are pending (%s). "+
				"Prefer targeted fixes or an intentional migrate. %s%s",
			gate.Reason, originalTipMarker, fix)
	case !gate.Determined:
		return fmt.Sprintf("Do NOT run 'bd doctor --fix' (%s). %s%s",
			gate.Reason, originalTipMarker, fix)
	}
	return fix
}

// filesystemOnlyFixes are repairs that provably touch only files on disk.
// Unlisted names are treated as database-touching, so a fix added later is
// guarded by default rather than escaping the gate silently.
//
// Every name in applyFixList's dispatch switch (cmd/bd/doctor_fix.go) was
// audited against that rule. The ones deliberately left out open a store, so
// being withheld under a blocked gate is correct rather than an omission:
// "Database", "Database Integrity", "Fresh Clone", "Schema Compatibility",
// "Repo Fingerprint" (fix.RepoFingerprint and fix.FixMissingMetadata both write
// through the store), "Dolt Schema", "Pending Migrations", and the row-level
// data repairs. "Sync Divergence", "JSONL Config" and "Untracked Files" are
// retired no-ops that write nothing at all; they stay unlisted because they are
// not filesystem repairs, and withholding a no-op costs the user nothing.
var filesystemOnlyFixes = map[string]bool{
	"Gitignore":             true,
	"Project Gitignore":     true,
	"Metadata Config":       true,
	"Redirect Tracking":     true,
	"Last-Touched Tracking": true,
	"Tracked Runtime Files": true,
	"Git Hooks":             true,
	"Hooks Path":            true, // git-config unset, beads-managed paths only
	"Permissions":           true,
	"Lock Files":            true,
	"Legacy MQ Files":       true,
	"Classic Artifacts":     true,
	"Btrfs NoCOW (dolt)":    true,
	"Circuit Breaker":       true, // removes stale circuit-breaker marker files
	"Database Config":       true, // rewrites metadata.json only; refuses Dolt outright
}

// IsFilesystemOnlyFix reports whether the named fix touches only the
// filesystem. Unknown names report false — see filesystemOnlyFixes.
func IsFilesystemOnlyFix(checkName string) bool {
	return filesystemOnlyFixes[strings.TrimSpace(checkName)]
}

// recoveryFixes are the repairs a user reaches for when the database cannot be
// opened at all. Each rewrites on-disk database state or metadata and none can
// migrate a schema that already exists elsewhere; each is the *cure* for an
// unreachable database rather than a schema write:
//   - Corrupt Manifest moves .dolt/ aside and runs a plain `dolt init`.
//   - Dolt Format seeds a marker file.
//   - Dolt Schema probes the server over a plain read connection to find the
//     right database name and writes only metadata.json.
//   - Database Integrity moves the local dolt directory aside and runs
//     `bd init --force`, which does open a store, but only the fresh one it
//     just created; server-mode repos, whose schema lives on a shared server,
//     are refused up front by serverModeIntegrityRecoveryGuard.
//
// Fresh Clone and Database are deliberately absent: they open the existing
// store through the migrating factory. Unlisted names are treated as
// schema-writing, as in filesystemOnlyFixes.
var recoveryFixes = map[string]bool{
	"Corrupt Manifest":   true, // backup + reinit of a corrupt .dolt/
	"Database Integrity": true, // backup + `bd init --force`
	"Dolt Format":        true, // seeds a marker file
	"Dolt Schema":        true, // backfills dolt_database in metadata.json
}

// IsRecoveryFix reports whether the named fix repairs an unopenable database
// without a schema-writing open. See recoveryFixes.
func IsRecoveryFix(checkName string) bool {
	return recoveryFixes[strings.TrimSpace(checkName)]
}

// AllowsFix is the single admission policy for applying (or previewing as
// runnable) the named fix under this gate. Filesystem-only fixes follow
// AllowFSFix. Everything else needs AllowDBFix, with one narrow exception:
// recovery fixers stay available while the database is unreachable, because
// with no readable schema there is nothing to skew, and withholding them would
// block the very repairs that make a dead database reachable again. When the
// database is reachable but skewed or undetermined they stay withheld.
func (g FixGate) AllowsFix(checkName string) bool {
	switch {
	case IsFilesystemOnlyFix(checkName):
		return g.AllowFSFix
	case g.AllowDBFix:
		return true
	case !g.DBReachable && IsRecoveryFix(checkName):
		return true
	}
	return false
}
