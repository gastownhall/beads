package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"golang.org/x/term"
)

// stderr is the writer for migration progress messages. Defaults to os.Stderr
// when stderr is a terminal so humans see progress, and to io.Discard otherwise
// so the lines don't pollute machine-parsed output (tests, CI, piped callers).
// Overridable in tests.
var stderr io.Writer = defaultStderr()

func defaultStderr() io.Writer {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		return os.Stderr
	}
	return io.Discard
}

const largeRigThreshold = 10000

// issueRowCounter returns the current issues-table row count, or an error if
// the table is unreachable (fresh install → table doesn't exist yet). The
// caller uses the error as the "no warning" signal. Variable so tests in this
// package that exercise runMigrations against a non-DB mock can stub out the
// query without panicking on QueryRowContext.
var issueRowCounter = func(ctx context.Context, db DBConn) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&n)
	return n, err
}

// emitLargeRigNotice writes the one-line large-rig warning to out when the
// issues count exceeds largeRigThreshold. An error from the counter is
// treated as "fresh install / table missing" and suppresses the warning —
// see be-8ja for the UX rationale.
func emitLargeRigNotice(out io.Writer, count int64, err error) {
	if err != nil || count <= largeRigThreshold {
		return
	}
	fmt.Fprintf(out, "Large rig detected (%d issues). This migration may take up to 90 seconds; do not interrupt.\n", count)
}

// humanMigrationName turns "0033_add_date_indexes.up.sql" into
// "add_date_indexes" for the progress line.
func humanMigrationName(filename string) string {
	s := strings.TrimSuffix(filename, ".up.sql")
	parts := strings.SplitN(s, "_", 2)
	if len(parts) < 2 {
		return s
	}
	return parts[1]
}

// DBConn is the minimal interface satisfied by *sql.DB, *sql.Tx, and *sql.Conn.
// It provides query and exec methods needed by the migration runner.
type DBConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SchemaSkewError is returned when the DB schema version is ahead of the
// binary's known version (forward drift). Stale binary queries may fail with
// cryptic SQL errors like "column X could not be found in any table in scope".
type SchemaSkewError struct {
	DBVersion     int
	BinaryVersion int
}

func (e *SchemaSkewError) Error() string {
	delta := e.DBVersion - e.BinaryVersion
	unit := "migrations"
	if delta == 1 {
		unit = "migration"
	}
	return fmt.Sprintf("schema version mismatch: database is at v%d, binary knows up to v%d (%d %s ahead)",
		e.DBVersion, e.BinaryVersion, delta, unit)
}

// UserMessage returns the full multi-line error block for terminal output.
func (e *SchemaSkewError) UserMessage() string {
	return e.Error() + "\n" +
		"\n" +
		"  Your bd binary is stale. Queries for dropped or renamed columns will fail\n" +
		"  with cryptic SQL errors (e.g. \"column X could not be found in any table in scope\").\n" +
		"\n" +
		"  Rebuild from main:\n" +
		"    CGO_ENABLED=0 go build -tags gms_pure_go ./cmd/bd\n" +
		"\n" +
		"  Or install the latest release:\n" +
		"    CGO_ENABLED=0 go install -tags gms_pure_go github.com/steveyegge/beads/cmd/bd@latest\n" +
		"\n" +
		"  To proceed despite the risk (some read commands may still work):\n" +
		"    BD_IGNORE_SCHEMA_SKEW=1 bd <command>\n" +
		"    bd --ignore-schema-skew <command>\n"
}

// EscapeHint returns the escape-hatch string for JSON error output.
func (e *SchemaSkewError) EscapeHint() string {
	return "BD_IGNORE_SCHEMA_SKEW=1 bd <command>  or  bd --ignore-schema-skew <command>"
}

// IsSchemaSkewError reports whether err (or any error it wraps) is a
// *SchemaSkewError.
func IsSchemaSkewError(err error) bool {
	var e *SchemaSkewError
	return errors.As(err, &e)
}

// checkSchemaSkew queries the DB's current schema version and returns a
// *SchemaSkewError if the DB is ahead of the binary. Returns nil for a fresh
// DB (version=0) or when BD_IGNORE_SCHEMA_SKEW=1 (prints a warning instead).
func checkSchemaSkew(ctx context.Context, db DBConn) error {
	// CurrentVersion treats a missing schema_migrations table as version 0, so
	// this is safe to call before migrations have created the table: a
	// brand-new database (version 0) falls through the no-op check below. That
	// matters on the writable open path, where the guard runs before initSchema
	// creates the table on a fresh database.
	currentVersion, err := CurrentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("schema skew check: %w", err)
	}
	if currentVersion == 0 || currentVersion <= LatestVersion() {
		return nil
	}
	if os.Getenv("BD_IGNORE_SCHEMA_SKEW") == "1" {
		fmt.Fprintf(os.Stderr,
			"Warning: schema skew ignored — database (v%d) is ahead of binary (v%d); some queries may fail\n",
			currentVersion, LatestVersion())
		return nil
	}
	return &SchemaSkewError{DBVersion: currentVersion, BinaryVersion: LatestVersion()}
}

// CheckForwardDrift reports a *SchemaSkewError when the database's schema
// version is AHEAD of the binary's (forward drift). It accepts any DBConn (a
// pooled *sql.DB or a pinned *sql.Conn), so both the read-only store path
// (where MigrateUp is skipped) and the writable open path (where MigrateUp
// no-ops on a forward-drifted DB rather than erroring) can fail fast before a
// query hits a dropped or renamed column.
func CheckForwardDrift(ctx context.Context, db DBConn) error {
	return checkSchemaSkew(ctx, db)
}

// SchemaBehindError is returned when a database is opened on a path that
// cannot migrate it (read-only opens) and its schema version is behind the
// binary's. Without this check the open succeeds and queries fail later with
// cryptic unknown-column/table errors (bd-578h9.12).
type SchemaBehindError struct {
	DBVersion     int
	BinaryVersion int
}

func (e *SchemaBehindError) Error() string {
	return fmt.Sprintf("schema version mismatch: database is at v%d, binary expects v%d, and the read-only open cannot migrate it; run any bd write command in that workspace to migrate, or set BD_IGNORE_SCHEMA_SKEW=1 to read anyway (queries touching newer schema may fail)",
		e.DBVersion, e.BinaryVersion)
}

// IsSchemaBehindError reports whether err (or any error it wraps) is a
// *SchemaBehindError.
func IsSchemaBehindError(err error) bool {
	var e *SchemaBehindError
	return errors.As(err, &e)
}

// CheckBehindDrift returns a *SchemaBehindError when the database's schema
// version is behind the binary's. Used by read-only opens, which skip
// MigrateUp by design (bd-6dnrw.32) — the paths that previously auto-migrated
// foreign databases (GH#3231) now need a clear open-time failure instead of
// unknown-column errors at query time. BD_IGNORE_SCHEMA_SKEW=1 downgrades it
// to a warning, mirroring forward drift. A fresh DB (version 0) is reported
// as behind too: it has no readable schema at all.
func CheckBehindDrift(ctx context.Context, db *sql.DB) error {
	var currentVersion int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&currentVersion); err != nil {
		return fmt.Errorf("schema behind-drift check: %w", err)
	}
	if currentVersion >= LatestVersion() {
		return nil
	}
	if os.Getenv("BD_IGNORE_SCHEMA_SKEW") == "1" {
		fmt.Fprintf(os.Stderr,
			"Warning: schema skew ignored — database (v%d) is behind binary (v%d) and was opened read-only; some queries may fail\n",
			currentVersion, LatestVersion())
		return nil
	}
	return &SchemaBehindError{DBVersion: currentVersion, BinaryVersion: LatestVersion()}
}

type dirtyTableState struct {
	staged bool
}

var doltStatusTableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

//go:embed migrations/*.up.sql
var upMigrations embed.FS

//go:embed migrations/ignored/*.up.sql
var upIgnoredMigrations embed.FS

type migrationSource struct {
	files       embed.FS
	dir         string
	cursorTable string
	// sentinelTables are tables this series is responsible for creating. A
	// non-zero cursor is only believed while they all exist: the cursor is a
	// claim about the schema, and a claim contradicted by the schema is worth
	// less than no claim at all. See cursorContradictedBySchema.
	//
	// INVARIANT: no future migration in this series may DROP or RENAME a
	// sentinel. Older binaries in the field check their own sentinel list
	// against the live schema, so removing one would make every healthy newer
	// database read as "contradicted" to them and re-run their whole series.
	// TestSentinelTablesAreCreatedByTheSeries enforces the creating side only;
	// the dropping side is this comment.
	sentinelTables []string
	// sentinelColumns are clone-local columns whose absence contradicts an
	// otherwise at-latest cursor just as strongly as an absent sentinel table.
	//
	// INVARIANT: no future migration in this series may DROP or RENAME a
	// sentinel column (or the table carrying it). Older binaries in the field
	// check their own sentinel list against the live schema, so removing one
	// would make every healthy newer database read as "contradicted" to them
	// and re-run their whole series. TestSentinelColumnsAreCreatedByTheSeries
	// enforces the creating side only; the dropping side is this comment.
	sentinelColumns []schemaSentinelColumn
}

type schemaSentinelColumn struct {
	table  string
	column string
}

var (
	mainSource = migrationSource{
		files:       upMigrations,
		dir:         "migrations",
		cursorTable: "schema_migrations",
	}
	ignoredSource = migrationSource{
		files:       upIgnoredMigrations,
		dir:         "migrations/ignored",
		cursorTable: "ignored_schema_migrations",
		// Created by ignored 0001 (the series' foundation) and required by
		// the wisp write path. wisp_dependencies is the one gh 5033 reports
		// missing; wisps is checked too so a partially materialized database
		// is caught by whichever is absent.
		sentinelTables: []string{"wisps", "wisp_dependencies"},
		// A historical ignored-v16 ordinal collision can leave the local
		// leases table present but without the column frozen 0016 adds.
		sentinelColumns: []schemaSentinelColumn{{table: "leases", column: "granted_node"}},
	}
)

var (
	latestOnce        sync.Once
	latestVer         int
	latestIgnoredOnce sync.Once
	latestIgnoredVer  int
)

// doltIgnorePatterns is the canonical set of clone-local table patterns every
// database must carry in dolt_ignore. Four of them are historically seeded as
// one-shot up.sql side effects of migrations 0019/0028, which never re-execute
// once the schema_migrations cursor is at-latest: a database materialized
// out-of-band (table-by-table copy/rename, dump restore) arrives with the
// cursor at-latest and misses them permanently, so wisp/local-state churn
// pollutes dolt_status and feeds the dirty-table migration gates. MigrateUp
// re-asserts the full set idempotently at the top of every write-mode open.
var doltIgnorePatterns = []string{
	// The events journal tables (bd-opisf) are seeded here rather than
	// version-gated: they have never existed on the versioned plane, so
	// asserting the pattern before 0064 runs is what keeps the CREATE from
	// landing as tracked-at-HEAD in the first place.
	"bd_events_journal",
	"bd_events_seq",
	"ignored_schema_migrations",
	"leases",
	"local_metadata",
	"repo_mtimes",
	"wisp_%",
	"wisps",
}

// versionGatedDoltIgnorePatterns are ignore patterns whose table was moved
// onto the ignored plane by a specific main-lane migration, so re-asserting
// them is only correct once the main cursor has reached that version. Seeding
// them unconditionally would strand a pre-flip database whose migration pass
// is refused (remote-migrate gate, dirty-table gate): the table would still
// be tracked-and-versioned while the pattern suppressed all staging of it, so
// its writes would silently stop being committed. The gate matters only for
// the out-of-band heal path — on a normal upgrade the flip migration itself
// registers the pattern in the same pass.
var versionGatedDoltIgnorePatterns = []struct {
	pattern        string
	minMainVersion int
}{
	{"events", 62}, // 0062_events_dolt_ignore (bd-red8u)
}

// seedDoltIgnorePatterns idempotently asserts the canonical dolt_ignore
// patterns and reports whether it actually changed anything. INSERT IGNORE
// leaves existing rows untouched, so a healthy database sees no working-set
// change and an explicit operator override (pattern present with
// ignored=false) is respected. On an under-seeded database the new rows land
// in the working set and take effect immediately; who commits them depends on
// the pass: when migration work is needed, MigrateUp exempts dolt_ignore from
// the pre-existing-dirty guards as pass-owned state (same treatment as the
// aux-rekey tables) and stageSchemaTables commits it with the pass; on the
// no-work short-circuit, MigrateUp commits the seed itself in a scoped,
// labeled commit (keyed off the changed return value) so the heal converges
// in one pass instead of riding along inside an unrelated later commit.
func seedDoltIgnorePatterns(ctx context.Context, db DBConn) (bool, error) {
	changed := false
	seedOne := func(pattern string) error {
		res, err := db.ExecContext(ctx, "INSERT IGNORE INTO dolt_ignore VALUES (?, true)", pattern)
		if err != nil {
			return fmt.Errorf("seeding dolt_ignore pattern %q: %w", pattern, err)
		}
		// A RowsAffected error degrades to changed=false for that row: the
		// seed then stays an uncommitted working-set diff swept up by the
		// next commit, exactly the pre-scoped-commit behavior.
		if n, raErr := res.RowsAffected(); raErr == nil && n > 0 {
			changed = true
		}
		return nil
	}
	for _, pattern := range doltIgnorePatterns {
		if err := seedOne(pattern); err != nil {
			return changed, err
		}
	}
	// Version-gated patterns seed only once the main cursor proves the flip
	// migration applied. The cursor table may not exist yet (first-ever run,
	// before mainSource.migrate bootstraps it): treat that as version 0 and
	// skip — the flip migration registers its own pattern when it applies.
	mainVersion, err := mainSource.currentVersion(ctx, db)
	if err != nil {
		mainVersion = 0
	}
	for _, gated := range versionGatedDoltIgnorePatterns {
		if mainVersion < gated.minMainVersion {
			continue
		}
		if err := seedOne(gated.pattern); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// commitSeededDoltIgnore stages and commits freshly seeded dolt_ignore rows
// in a scoped, labeled commit. Both MigrateUp paths use it: on the no-work
// short-circuit nothing downstream would ever commit the seed, and on the
// migration path the seed must be committed before the first step so an
// interrupted pass leaves a clean working set (#4566 self-heal contract).
func commitSeededDoltIgnore(ctx context.Context, db DBConn) error {
	if err := DrainCall(ctx, db, "CALL DOLT_ADD('dolt_ignore')"); err != nil {
		return fmt.Errorf("staging seeded dolt_ignore patterns: %w", err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'schema: seed dolt_ignore patterns')"); err != nil {
		return fmt.Errorf("committing seeded dolt_ignore patterns: %w", err)
	}
	return nil
}

func LatestVersion() int {
	latestOnce.Do(func() {
		latestVer = mainSource.latest()
	})
	return latestVer
}

func LatestIgnoredVersion() int {
	latestIgnoredOnce.Do(func() {
		latestIgnoredVer = ignoredSource.latest()
	})
	return latestIgnoredVer
}

func CurrentVersion(ctx context.Context, db DBConn) (int, error) {
	return mainSource.currentVersion(ctx, db)
}

func CurrentIgnoredVersion(ctx context.Context, db DBConn) (int, error) {
	return ignoredSource.currentVersion(ctx, db)
}

func PendingVersions(ctx context.Context, db DBConn) ([]int, error) {
	return mainSource.pendingVersions(ctx, db)
}

func PendingIgnoredVersions(ctx context.Context, db DBConn) ([]int, error) {
	return ignoredSource.pendingVersions(ctx, db)
}

func AllMigrationsSQL() string {
	var b strings.Builder
	b.WriteString(mainSource.bootstrapSQL())
	b.WriteString(";\n")
	for _, f := range mainSource.list() {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			continue
		}
		b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "\nINSERT IGNORE INTO %s (version, content_hash) VALUES (%d, '%s');\n",
			mainSource.cursorTable, f.version, hex.EncodeToString(sum[:]))
	}
	return b.String()
}

// MigrationSQL returns the frozen SQL content of a main-source migration
// file by name (e.g. "0057_events_value_columns_idempotent_longtext.up.sql"),
// read from the same embedded FS runMigrations applies in production
// (see mainSource.files above). It exists for tests outside this package
// (internal/storage/embeddeddolt's engine-based frozen-guard tests) that need
// to execute the byte-exact frozen migration content through a real engine:
// reading the file from disk by a package-relative path does not reliably
// resolve across every CI execution context, while the embedded FS is always
// available and is, in fact, higher-fidelity -- it is the literal bytes
// runMigrations itself applies, not a copy that could drift from the build.
// Read-only; callers cannot use it to mutate or bypass the frozen-file
// hygiene guard (scripts/check-migration-hygiene.sh).
func MigrationSQL(name string) (string, error) {
	data, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IgnoredMigrationSQL is MigrationSQL's ignored-lane counterpart: the frozen
// bytes of an ignored-source migration file (e.g. "0019_create_events.up.sql"),
// for engine-based frozen-guard tests of clone-local DDL.
func IgnoredMigrationSQL(name string) (string, error) {
	data, err := ignoredSource.files.ReadFile(ignoredSource.dir + "/" + name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parseVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no version prefix")
	}
	return strconv.Atoi(parts[0])
}

// MigrateUpTo applies main-source migrations up to and including maxVersion,
// without the dirty-table guards, backfills, rekeys, or ignored-source pass
// that MigrateUp layers on. It exists so cross-upgrade-boundary tests
// (bd-6dnrw.16) can reconstruct the schema as it stood at a historical
// release and use it as a Dolt merge ancestor. Production code must use
// MigrateUp: stopping short of the latest version on a real database leaves
// it half-upgraded by design.
func MigrateUpTo(ctx context.Context, db DBConn, maxVersion int) (int, error) {
	applied, _, err := mainSource.migrate(ctx, db, maxVersion)
	return applied, err
}

func MigrateUp(ctx context.Context, db DBConn) (int, error) {
	// Re-assert the canonical dolt_ignore patterns before anything else, and
	// in particular before the migrationWorkNeeded short-circuit: a database
	// whose migration cursors arrived at-latest without executing the seeding
	// migrations (out-of-band table copy) reports no work needed and
	// would otherwise never be healed.
	seedChanged, err := seedDoltIgnorePatterns(ctx, db)
	if err != nil {
		return 0, err
	}

	needed, err := migrationWorkNeeded(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("checking schema migration work: %w", err)
	}
	if !needed {
		// No migration pass will run, so nothing downstream commits the seed:
		// it would sit as an uncommitted working-set diff until an unrelated
		// write/pull sweeps it into its commit (and a read-only repo carries
		// it indefinitely). Commit it here, scoped and labeled, so the
		// out-of-band-copy heal converges in one pass.
		if seedChanged {
			if err := commitSeededDoltIgnore(ctx, db); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}

	dirtyBeforeAll, err := dirtyTables(ctx, db, false)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration status: %w", err)
	}
	// dolt_ignore is pass-owned state: seedDoltIgnorePatterns above may have
	// just dirtied it on an under-seeded database. Exempting it from the
	// pre-existing-dirty guards (like the aux-rekey tables below) keeps the
	// pending-migration and changed-signature gates off it.
	delete(dirtyBeforeAll, "dolt_ignore")
	if err := unstagePreExistingTables(ctx, db, dirtyBeforeAll); err != nil {
		return 0, fmt.Errorf("unstaging pre-migration tables: %w", err)
	}
	// The seed must not ride the migration pass: every step of the pass
	// commits atomically, and a pass killed between steps must leave a CLEAN
	// working set (the #4566 self-heal contract) — but the seeded dolt_ignore
	// rows would stay dirty until the pass's final commit, so an interrupted
	// retry sees a dirty working set and refuses to converge. Commit the seed
	// scoped and labeled now, after pre-existing staged tables were unstaged
	// (so nothing else rides into the commit) and before the first step runs.
	if seedChanged {
		if err := commitSeededDoltIgnore(ctx, db); err != nil {
			return 0, err
		}
	}
	dirtyBefore, err := committableDirtyTables(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration status: %w", err)
	}
	delete(dirtyBefore, "dolt_ignore")
	// A previous pass that crashed mid-aux-rekey left its partial UPDATEs
	// dirty in the working set with the in-progress sentinel still recorded
	// (bd-578h9.16). Those tables are this pass's own migration state, not
	// pre-existing user writes: dropping them from dirtyBefore exempts them
	// from the changed-signature guard (the resumed rekey is about to change
	// them) and lets stageSchemaTables commit them with the rest of the pass.
	if resuming, err := anyAuxRekeyResumePending(ctx, db); err != nil {
		return 0, fmt.Errorf("reading aux rekey sentinel: %w", err)
	} else if resuming {
		for _, t := range auxRekeyTables {
			delete(dirtyBefore, t.name)
		}
	}
	touchedDirtyTables, err := mainSource.pendingMigrationDirtyTables(ctx, db, dirtyBefore)
	if err != nil {
		return 0, fmt.Errorf("checking dirty tables against pending migrations: %w", err)
	}
	if len(touchedDirtyTables) > 0 {
		return 0, &DirtyTablesError{Tables: touchedDirtyTables}
	}
	dirtyBeforeSignatures, err := dirtyTableSignatures(ctx, db, dirtyBefore)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration dirty table diffs: %w", err)
	}
	// Captured before the main migrations run: the aux re-key uses it to
	// distinguish the lineage's first rekey-aware migration (run the pass)
	// from a fresh clone of an already-converged lineage (record the marker
	// only, bd-578h9.4).
	mainVersionBefore, err := mainSource.currentVersion(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration schema version: %w", err)
	}

	applied, mainColumnAdded, err := mainSource.migrate(ctx, db, 0)
	if err != nil {
		return applied, err
	}

	backfilled, err := ensureBackfilledCustomStatusesCustomTypes(ctx, db)
	if err != nil {
		return applied, fmt.Errorf("backfill custom tables: %w", err)
	}

	// #4259: rewrite any per-clone-random dependency ids (minted by 0043's
	// DEFAULT (UUID()) before this fix) to the deterministic key, so independently
	// migrated clones converge to byte-identical, merge-safe dependencies. Runs
	// here, after the schema migrations (0050 has asserted the canonical schema),
	// and only on a pass where migration work was needed.
	rekeyed, err := rekeyDependencyIDs(ctx, db)
	if err != nil {
		return applied, fmt.Errorf("rekey dependency ids: %w", err)
	}
	backfilled = backfilled || rekeyed

	// bd-6dnrw.2: converge the events/comments/snapshots primary keys that
	// migration 0037 randomized per-clone, the same hazard class on the aux
	// tables. Gated on the clone-local ignored marker (recorded later in this
	// pass by ignoredSource.migrate) so it runs exactly once per clone instead
	// of churning synced rows on every later migration pass — and on the
	// pre-pass main cursor, so fresh clones of converged lineages record the
	// marker without re-running the rewrite (bd-578h9.4).
	// ...and the bd-ri8bd sibling: one more pass over the same tables for the
	// rows minted with random UUIDv7 ids between the initial backfill and the
	// switch to content-derived ids at insert time. Same machinery, own
	// marker/sentinel/shipped-version gates, one shared cursor read.
	auxRekeyed, err := rekeyAuxRowIDsAllPasses(ctx, db, mainVersionBefore)
	if err != nil {
		return applied, fmt.Errorf("rekey aux row ids: %w", err)
	}
	backfilled = backfilled || auxRekeyed

	touchedIgnoredDirtyTables, err := ignoredSource.pendingMigrationDirtyTables(ctx, db, dirtyBeforeAll)
	if err != nil {
		return applied, fmt.Errorf("checking dirty tables against pending ignored migrations: %w", err)
	}
	if len(touchedIgnoredDirtyTables) > 0 {
		// Deliberately a plain, untyped error (unlike the main-source guard
		// above, which returns *DirtyTablesError): this check fires mid-pass,
		// after the main-source migrations have already applied. A lenient
		// caller (embeddeddolt's openReadOnlyCommand / openWorkingSetReconcile
		// intents) skipping this and returning as if the open succeeded would
		// let a reconcile commit checkpoint a half-applied migration pass.
		// The ignored source also tracks bd-internal state (dolt_ignore'd
		// tables like ignored_schema_migrations), not expected user data, so
		// there is no dirty-commit recovery story to support here the way
		// there is for the main-source guard (#4566 scope).
		return applied, fmt.Errorf("pending ignored schema migrations alter pre-existing dirty tables: %s", strings.Join(touchedIgnoredDirtyTables, ", "))
	}

	appliedIgnored, ignoredColumnAdded, err := ignoredSource.migrate(ctx, db, 0)
	if err != nil {
		return applied, fmt.Errorf("ignored migrations: %w", err)
	}
	if err := unstageIgnoredTables(ctx, db); err != nil {
		return applied, fmt.Errorf("unstaging ignored migration tables: %w", err)
	}

	if applied == 0 && !backfilled && appliedIgnored == 0 && !mainColumnAdded && !ignoredColumnAdded {
		return applied, nil
	}
	changedDirtyTables, err := changedDirtyTableSignatures(ctx, db, dirtyBeforeSignatures)
	if err != nil {
		return applied, fmt.Errorf("checking pre-existing dirty table diffs: %w", err)
	}
	if len(changedDirtyTables) > 0 {
		return applied, fmt.Errorf("pre-existing dirty tables changed during schema migration: %s", strings.Join(changedDirtyTables, ", "))
	}

	staged, err := stageSchemaTables(ctx, db, dirtyBefore)
	if err != nil {
		return applied, fmt.Errorf("staging migrations: %w", err)
	}
	if !staged {
		return applied, nil
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')"); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			return applied, fmt.Errorf("committing migrations: %w", err)
		}
	}

	return applied, nil
}

func migrationWorkNeeded(ctx context.Context, db DBConn) (bool, error) {
	if !mainSource.atLatest(ctx, db) || !ignoredSource.atLatest(ctx, db) {
		return true, nil
	}
	// A database already at the latest numbered migration still needs work if it
	// predates the content_hash column (gastownhall/beads#4259 reporter fix No.2).
	// Without this, MigrateUp short-circuits before migrate() runs the idempotent
	// ALTER, so the recording/detection surface is never installed on exactly the
	// already-upgraded databases the fix is meant to protect.
	hasMainHash, err := mainSource.hasContentHashColumn(ctx, db)
	if err != nil {
		return false, err
	}
	if !hasMainHash {
		return true, nil
	}
	hasIgnoredHash, err := ignoredSource.hasContentHashColumn(ctx, db)
	if err != nil {
		return false, err
	}
	if !hasIgnoredHash {
		return true, nil
	}
	return needsBackfilledCustomStatusesCustomTypes(ctx, db)
}

func committableDirtyTables(ctx context.Context, db DBConn) (map[string]dirtyTableState, error) {
	tables, err := dirtyTables(ctx, db, true)
	if err != nil {
		return nil, err
	}
	delete(tables, mainSource.cursorTable)
	delete(tables, ignoredSource.cursorTable)
	return tables, nil
}

func stagedDirtyTables(tables map[string]dirtyTableState) []string {
	var staged []string
	for table, state := range tables {
		if state.staged {
			staged = append(staged, table)
		}
	}
	sort.Strings(staged)
	return staged
}

func unstagePreExistingTables(ctx context.Context, db DBConn, tables map[string]dirtyTableState) error {
	staged := stagedDirtyTables(tables)
	if len(staged) > 0 {
		log.Printf("schema migration unstaging pre-existing staged tables: %s", strings.Join(staged, ", "))
	}
	for _, table := range staged {
		if err := DrainCall(ctx, db, "CALL DOLT_RESET(?)", table); err != nil {
			return fmt.Errorf("dolt reset %s: %w", table, err)
		}
	}
	return nil
}

func unstageIgnoredTables(ctx context.Context, db DBConn) error {
	tables, err := existingIgnoredTables(ctx, db)
	if err != nil {
		return err
	}
	return unstagePreExistingTables(ctx, db, tables)
}

func dirtyTableSignatures(ctx context.Context, db DBConn, tables map[string]dirtyTableState) (map[string]string, error) {
	signatures := make(map[string]string, len(tables))
	names := sortedDirtyTableNames(tables)
	for _, table := range names {
		signature, err := dirtyTableSignature(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		signatures[table] = signature
	}
	return signatures, nil
}

func changedDirtyTableSignatures(ctx context.Context, db DBConn, before map[string]string) ([]string, error) {
	var changed []string
	names := sortedSignatureTableNames(before)
	for _, table := range names {
		signature, err := dirtyTableSignature(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if signature != before[table] {
			changed = append(changed, table)
		}
	}
	return changed, nil
}

func sortedDirtyTableNames(tables map[string]dirtyTableState) []string {
	names := make([]string, 0, len(tables))
	for table := range tables {
		names = append(names, table)
	}
	sort.Strings(names)
	return names
}

func sortedSignatureTableNames(signatures map[string]string) []string {
	names := make([]string, 0, len(signatures))
	for table := range signatures {
		names = append(names, table)
	}
	sort.Strings(names)
	return names
}

func dirtyTableSignature(ctx context.Context, db DBConn, table string) (string, error) {
	if !doltStatusTableNameRE.MatchString(table) {
		return "", fmt.Errorf("unsafe dolt status table name %q", table)
	}
	//nolint:gosec // table comes from dolt_status; dolt_diff requires a literal table argument.
	rows, err := db.QueryContext(ctx, "SELECT * FROM dolt_diff('HEAD', 'WORKING', "+sqlStringLiteral(table)+")")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var rowSignatures []string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return "", err
		}

		var b strings.Builder
		for i, column := range columns {
			if isDiffMetadataColumn(column) {
				continue
			}
			b.WriteString(column)
			b.WriteByte('=')
			writeSignatureValue(&b, values[i])
			b.WriteByte(0)
		}
		rowSignatures = append(rowSignatures, b.String())
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(rowSignatures)

	h := sha256.New()
	for _, row := range rowSignatures {
		_, _ = h.Write([]byte(row))
		_, _ = h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isDiffMetadataColumn(column string) bool {
	switch strings.ToLower(column) {
	case "from_commit", "to_commit", "from_commit_date", "to_commit_date":
		return true
	default:
		return false
	}
}

func sqlStringLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

func writeSignatureValue(b *strings.Builder, v any) {
	switch typed := v.(type) {
	case nil:
		b.WriteString("<nil>")
	case []byte:
		b.Write(typed)
	default:
		b.WriteString(fmt.Sprintf("%v", typed))
	}
}

func stageSchemaTables(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) (bool, error) {
	dirtyAfter, err := dirtyTables(ctx, db, true)
	if err != nil {
		return false, err
	}

	tableSet := make(map[string]struct{})
	for table := range dirtyAfter {
		if _, wasDirty := dirtyBefore[table]; wasDirty {
			continue
		}
		tableSet[table] = struct{}{}
	}
	tablesAfter, err := existingCommittableTables(ctx, db)
	if err != nil {
		return false, err
	}
	for table := range tablesAfter {
		if _, wasDirty := dirtyBefore[table]; wasDirty {
			continue
		}
		tableSet[table] = struct{}{}
	}

	tables := make([]string, 0, len(tableSet))
	for table := range tableSet {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', ?)", table); err != nil {
			return false, fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	return len(tables) > 0, nil
}

func existingCommittableTables(ctx context.Context, db DBConn) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.TABLE_NAME
		FROM INFORMATION_SCHEMA.TABLES t
		WHERE t.TABLE_SCHEMA = DATABASE()
		  AND t.TABLE_TYPE = 'BASE TABLE'
		  AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			  AND t.TABLE_NAME LIKE di.pattern
		  )
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]struct{})
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables[table] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func existingIgnoredTables(ctx context.Context, db DBConn) (map[string]dirtyTableState, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.table_name, s.staged
		FROM dolt_status s
		WHERE EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			  AND s.table_name LIKE di.pattern
		)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]dirtyTableState)
	for rows.Next() {
		var table string
		var staged bool
		if err := rows.Scan(&table, &staged); err != nil {
			return nil, err
		}
		tables[table] = dirtyTableState{staged: staged}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func dirtyTables(ctx context.Context, db DBConn, excludeIgnored bool) (map[string]dirtyTableState, error) {
	query := `
		SELECT s.table_name, s.staged
		FROM dolt_status s
	`
	if excludeIgnored {
		query += `
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)
		`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]dirtyTableState)
	for rows.Next() {
		var table string
		var staged bool
		if err := rows.Scan(&table, &staged); err != nil {
			return nil, err
		}
		state := tables[table]
		state.staged = state.staged || staged
		tables[table] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

type migrationFile struct {
	version int
	name    string
}

func (m migrationSource) bootstrapSQL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	version INT PRIMARY KEY,
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	content_hash CHAR(64)
)`, m.cursorTable)
}

// hasContentHashColumn reports whether the cursor table already carries the
// content_hash column. A not-yet-created table simply reports false.
//
// It probes a single table with SHOW COLUMNS rather than INFORMATION_SCHEMA.COLUMNS,
// whose predicate Dolt does not push down. The LIKE narrows the result set, but
// we still compare the Field name exactly because '_' is a LIKE single-character
// wildcard.
func (m migrationSource) hasContentHashColumn(ctx context.Context, db DBConn) (bool, error) {
	//nolint:gosec // G201: m.cursorTable is a hardcoded constant; the LIKE literal is fixed.
	rows, err := db.QueryContext(ctx, "SHOW COLUMNS FROM "+m.cursorTable+" LIKE 'content_hash'")
	if err != nil {
		// SHOW COLUMNS errors on a missing table; the old INFORMATION_SCHEMA
		// probe returned count 0 instead. Preserve that: an absent cursor table
		// has no content_hash column.
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s.content_hash: %w", m.cursorTable, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return false, fmt.Errorf("checking %s.content_hash: %w", m.cursorTable, err)
	}
	// SHOW COLUMNS returns Field, Type, Null, Key, Default, Extra (and possibly
	// more on some servers); scan every column into RawBytes and read the first
	// ("Field"), which is the column name.
	cells := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return false, fmt.Errorf("checking %s.content_hash: %w", m.cursorTable, err)
		}
		if len(cells) > 0 && string(cells[0]) == "content_hash" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("checking %s.content_hash: %w", m.cursorTable, err)
	}
	return false, nil
}

// ensureContentHashColumn adds the content_hash column to an existing cursor
// table that predates it (gastownhall/beads#4259 reporter fix No.2: record a
// per-migration content hash so two clones at the same MAX(version) but with
// divergent migration content are detectable). Fresh tables already have it via
// bootstrapSQL; this idempotently upgrades older databases without a numbered
// migration. Already-applied rows keep a NULL hash — their migration content is
// not re-read. It reports whether it actually added the column, so MigrateUp can
// treat that ALTER as committable schema work even when no numbered migration or
// backfill ran.
func (m migrationSource) ensureContentHashColumn(ctx context.Context, db DBConn) (bool, error) {
	has, err := m.hasContentHashColumn(ctx, db)
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}
	//nolint:gosec // G201: m.cursorTable is a hardcoded constant.
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+m.cursorTable+" ADD COLUMN content_hash CHAR(64)"); err != nil {
		return false, fmt.Errorf("adding %s.content_hash: %w", m.cursorTable, err)
	}
	return true, nil
}

func checkNoDuplicateVersions(files []migrationFile) {
	seen := make(map[int]string, len(files))
	for _, m := range files {
		if prior, ok := seen[m.version]; ok {
			panic(fmt.Sprintf(
				"schema: duplicate migration version %d: %q and %q — renumber one before commit",
				m.version, prior, m.name,
			))
		}
		seen[m.version] = m.name
	}
}

func (m migrationSource) list() []migrationFile {
	entries, err := fs.ReadDir(m.files, m.dir)
	if err != nil {
		panic(fmt.Sprintf("schema: failed to read embedded %s: %v", m.dir, err))
	}
	var files []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, err := parseVersion(e.Name())
		if err != nil {
			panic(fmt.Sprintf("schema: invalid migration filename %q: %v", e.Name(), err))
		}
		files = append(files, migrationFile{version: v, name: e.Name()})
	}
	checkNoDuplicateVersions(files)
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files
}

func (m migrationSource) latest() int {
	files := m.list()
	if len(files) == 0 {
		return 0
	}
	return files[len(files)-1].version
}

func (m migrationSource) atLatest(ctx context.Context, db DBConn) bool {
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return false
	}
	return current >= m.latest()
}

func (m migrationSource) currentVersion(ctx context.Context, db DBConn) (int, error) {
	// Probe existence with a query that always SUCCEEDS before ever issuing one
	// that can fail. A Dolt session that issues a failing statement stays
	// pinned to its pre-statement catalog snapshot, so a bare SELECT against a
	// not-yet-created cursor table poisons the pooled connection: tables
	// created afterwards on other connections stay invisible to this one for
	// the rest of its life in the pool (be-bv7x).
	var cursorExists int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		m.cursorTable,
	).Scan(&cursorExists); err != nil {
		return 0, fmt.Errorf("probing %s existence: %w", m.cursorTable, err)
	}
	if cursorExists == 0 {
		return 0, nil
	}

	var current int
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM "+m.cursorTable).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		if dberrors.IsTableNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s version: %w", m.cursorTable, err)
	}
	if current == 0 {
		return 0, nil
	}
	// A missing cursor TABLE already meant "nothing applied". A cursor whose
	// tables are absent means the same thing and was previously believed
	// (gh 5033, gh 4356): ignored_schema_migrations is itself dolt-ignored and
	// clone-local, so a database materialized out of band — table-by-table
	// copy, dump restore, or a clone that picked up the cursor rows without
	// the clone-local tables they describe — arrives claiming at-latest with
	// no wisps tables. atLatest() then short-circuits migrationWorkNeeded()
	// and the series never re-runs, surfacing much later and much further away
	// as "table not found: wisp_dependencies" on `bd close`.
	contradicted, cerr := m.cursorContradictedBySchema(ctx, db)
	if cerr != nil {
		return 0, cerr
	}
	if contradicted {
		return 0, nil
	}
	return current, nil
}

// cursorContradictedBySchema reports whether this series' cursor claims work
// that the schema does not corroborate.
//
// Returning "cursor is 0" rather than an error is deliberate: the series is
// written to be re-runnable against a database that already has some of it.
// migrations/ignored/0001 builds each table as __temp__<name> and then
// `RENAME TABLE __temp__x TO x` only when x does not already exist, DROPping
// the temp otherwise; later migrations gate their ALTERs on
// INFORMATION_SCHEMA lookups. So re-running the series repairs the missing
// tables and leaves existing data untouched — which is why this can heal
// rather than merely diagnose.
func (m migrationSource) cursorContradictedBySchema(ctx context.Context, db DBConn) (bool, error) {
	for _, table := range m.sentinelTables {
		present, err := sentinelTableExists(ctx, db, table)
		if err != nil {
			return false, fmt.Errorf("checking %s sentinel table %s: %w", m.cursorTable, table, err)
		}
		if !present {
			return true, nil
		}
	}
	for _, column := range m.sentinelColumns {
		present, err := sentinelColumnExists(ctx, db, column.table, column.column)
		if err != nil {
			return false, fmt.Errorf("checking %s sentinel column %s.%s: %w", m.cursorTable, column.table, column.column, err)
		}
		if !present {
			return true, nil
		}
	}
	return false, nil
}

// sentinelTableExists is a function variable for the same reason
// issueRowCounter is: it lets the cursor-reality tests exercise the real
// decision without a live database.
var sentinelTableExists = func(ctx context.Context, db DBConn, table string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
		table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

var sentinelColumnExists = schemaColumnExists

func (m migrationSource) pendingVersions(ctx context.Context, db DBConn) ([]int, error) {
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	files := m.list()
	pending := make([]int, 0, len(files))
	for _, mf := range files {
		if mf.version > current {
			pending = append(pending, mf.version)
		}
	}
	return pending, nil
}

func (m migrationSource) pendingMigrationDirtyTables(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) ([]string, error) {
	if len(dirtyBefore) == 0 {
		return nil, nil
	}
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return nil, err
	}

	dirtyNames := sortedDirtyTableNames(dirtyBefore)
	touched := make(map[string]struct{})
	for _, mf := range m.list() {
		if mf.version <= current {
			continue
		}
		data, err := m.files.ReadFile(m.dir + "/" + mf.name)
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", mf.name, err)
		}
		sqlText := string(data)
		for _, table := range dirtyNames {
			if migrationSQLTouchesTable(sqlText, table) {
				touched[table] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(touched))
	for table := range touched {
		names = append(names, table)
	}
	sort.Strings(names)
	return names, nil
}

func migrationSQLTouchesTable(sqlText, table string) bool {
	tableRef := "`?" + regexp.QuoteMeta(table) + "`?"
	// This intentionally scans raw migration text so PREPARE strings that run
	// DDL/DML are treated as real table touches.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:alter\s+table|update|delete\s+from|insert(?:\s+ignore)?\s+into|replace\s+into|truncate\s+table|drop\s+table|create\s+table(?:\s+if\s+not\s+exists)?|rename\s+table)\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\brename\s+table\b[^;]*\bto\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\bcreate\s+(?:unique\s+)?index\b[^;]*\bon\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\b(?:create\s+(?:or\s+replace\s+)?view|alter\s+view)\s+` + tableRef + `\b`),
	}
	for _, pattern := range patterns {
		if pattern.MatchString(sqlText) {
			return true
		}
	}
	return false
}

// procedureCallRe matches a stored-procedure invocation (CALL ...) at a
// statement boundary, case-insensitively. It decides whether a migration body
// must have its result sets drained explicitly (see execMigrationBody).
var procedureCallRe = regexp.MustCompile(`(?i)(?:^|;|\n)\s*CALL\s`)

// DrainCall runs a statement (or multi-statement body) that invokes a Dolt
// stored procedure and fully consumes EVERY result set it returns, leaving the
// pinned connection clean for the next command.
//
// Why this matters is an ERROR-PATH asymmetry in go-sql-driver/mysql, not a
// happy-path gap: mysqlConn.exec (behind ExecContext) does end with
// handleOk.discardResults(), so a CALL that succeeds is drained already. But
// every failure before that line — readResultSetHeaderPacket, skipColumns,
// skipRows — returns early, leaving whatever the server still has queued
// unread on the wire. mysqlRows.Close() has no such exit: it skips unread rows
// and discards remaining result sets unconditionally, which is why routing
// through QueryContext + a deferred Close is drain-safe on both paths.
//
// That asymmetry is reachable here precisely because these call sites tolerate
// an error and keep using the same pinned connection: a multi-statement body
// like 0040 (four INSERT/CALL DOLT_COMMIT pairs) can fail on statement 4 with
// more results queued behind it, and MigrateUp and commitMigrationStep both
// swallow "nothing to commit" and carry on. The next command on that conn —
// the version-record INSERT, a later DOLT_ADD, or RELEASE_LOCK — then dies on
// the still-busy connection ("busy buffer" -> "driver: bad connection"). The
// same shape recurs, at far higher call volume, on every transaction-pinned
// *sql.Tx / *sql.Conn in internal/storage/dolt that runs a tolerate-and-continue
// CALL DOLT_ADD/DOLT_COMMIT/DOLT_MERGE/DOLT_BRANCH pair — unlike a pooled
// *sql.DB connection, a pinned connection can't be discarded by the pool on
// the next checkout, and database/sql does not reset a driver conn before
// handing it back to a future borrower, so an undrained buffer poisons
// whoever acquires that connection next.
//
// This is the necessary half of the fix, not the sufficient half: a
// load-induced transient (or a "nothing to commit" from a no-op commit) can
// still surface mid-migration, and the init retry loop then re-runs the whole
// migration. The frozen non-idempotent migrations (0040 bare-INSERTs its
// dolt_nonlocal_tables rows, 0041 DELETEs then commits them) are made
// replay-safe by pre-migration repairs keyed to their version, not by editing
// their shipped SQL — see preMigrationRepair and migration_repairs.go.
// Most Dolt procedures are called for their side effects alone, and DrainCall
// discards what they return. That is a statement about these CALL SITES, not
// about stored procedures in general: DOLT_PULL and DOLT_MERGE report what they
// did — whether anything merged, and whether it conflicted — only in the row
// they return. A caller that needs that report uses CallReturningRow, which
// drains identically.
func DrainCall(ctx context.Context, db DBConn, query string, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	return drainResultSets(rows, nil)
}

// CallRow is the first row of a CALL's first result set, keyed by column name.
// Dolt's procedure rows mix integers with a nullable message, and their column
// ORDER is a documented-but-unversioned detail (dolt_pull.go pins the
// fast_forward index in a const with a comment warning it must be updated if
// the schema changes), so values are read by name and scanned as NullString.
type CallRow map[string]sql.NullString

// Str returns the named column's text. ok is false when the column is absent
// from the row or is SQL NULL — DOLT_PULL returns a NULL message whenever its
// internal message is empty, which is a distinct outcome from any message it
// might actually spell out.
func (r CallRow) Str(name string) (value string, ok bool) {
	v, present := r[name]
	if !present || !v.Valid {
		return "", false
	}
	return v.String, true
}

// Int returns the named column parsed as an integer. ok is false when the
// column is absent, NULL, or not a number.
func (r CallRow) Int(name string) (value int, ok bool) {
	s, present := r.Str(name)
	if !present {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}

// CallReturningRow runs a CALL exactly as DrainCall does — consuming every row
// of every result set, so the pinned connection is left clean for the next
// command — and additionally returns the FIRST row of the FIRST result set.
//
// The drain is the load-bearing part and is shared with DrainCall verbatim; see
// DrainCall's comment for the go-sql-driver error-path asymmetry that makes it
// necessary. Scanning a row does not shorten the drain: the loop keeps running
// to the end of every result set whether or not the scan succeeded. Reaching
// for QueryRowContext instead would drain only the first result set and
// reintroduce the busy-buffer bug on the pinned *sql.Tx that the pull path uses.
//
// A CALL that returns no rows at all yields a nil CallRow and a nil error;
// reading a column from a nil CallRow reports absent rather than panicking.
func CallReturningRow(ctx context.Context, db DBConn, query string, args ...any) (CallRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var first CallRow
	if err := drainResultSets(rows, &first); err != nil {
		return nil, err
	}
	return first, nil
}

// drainResultSets consumes every row of every result set the statement
// produced. When capture is non-nil the first row of the first result set is
// also scanned into it; every other row is read and discarded, which is what
// frees the connection buffer for the next command.
//
// A scan failure is recorded and the drain continues, so a row this package
// cannot decode still leaves a usable connection behind. The drain's own error
// wins over the scan's: a broken result set explains a failed scan, not the
// other way round.
func drainResultSets(rows *sql.Rows, capture *CallRow) error {
	var scanErr error
	firstSet := true
	for {
		for rows.Next() {
			if capture != nil && firstSet && *capture == nil && scanErr == nil {
				row, err := scanRowByColumn(rows)
				if err != nil {
					scanErr = err
					continue
				}
				*capture = row
			}
		}
		firstSet = false
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return scanErr
}

// scanRowByColumn reads the row the cursor is on into a column-name-keyed map.
func scanRowByColumn(rows *sql.Rows) (CallRow, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]sql.NullString, len(cols))
	targets := make([]any, len(cols))
	for i := range values {
		targets[i] = &values[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return nil, err
	}
	row := make(CallRow, len(cols))
	for i, name := range cols {
		row[name] = values[i]
	}
	return row, nil
}

// execMigrationBody applies one migration file's SQL on the pinned migration
// connection. Bodies that invoke a stored procedure (today 0040 and 0041, both
// CALL DOLT_COMMIT) are routed through DrainCall so their result sets are
// consumed; all other migrations keep the unchanged ExecContext path.
func execMigrationBody(ctx context.Context, db DBConn, sqlText string) error {
	if !procedureCallRe.MatchString(sqlText) {
		_, err := db.ExecContext(ctx, sqlText)
		return err
	}
	return DrainCall(ctx, db, sqlText)
}

// migrate brings the source up to its latest version and returns the number of
// numbered migrations applied plus whether it added the content_hash column to a
// pre-existing cursor table. The column signal lets MigrateUp stage and commit
// that ALTER as schema work even when no numbered migration was applied.
// migrate applies pending migrations from this source. upTo bounds the highest
// version applied; pass 0 for the latest (the production path — only the
// MigrateUpTo test-support path passes a real bound).
func (m migrationSource) migrate(ctx context.Context, db DBConn, upTo int) (int, bool, error) {
	if _, err := db.ExecContext(ctx, m.bootstrapSQL()); err != nil {
		return 0, false, fmt.Errorf("creating %s: %w", m.cursorTable, err)
	}
	columnAdded, err := m.ensureContentHashColumn(ctx, db)
	if err != nil {
		return 0, false, err
	}

	target := m.latest()
	if upTo > 0 && upTo < target {
		target = upTo
	}

	// The cursor is read through currentVersion, never raw: that is where the
	// cursor-reality check lives (gh 5033). A raw read here would believe the
	// contradicted cursor that migrationWorkNeeded just disbelieved — MigrateUp
	// would decide "work needed" on every open, run the whole pass, and then
	// apply nothing, leaving the missing tables missing and the pass to repeat
	// forever. The heal only happens if the applier disbelieves the cursor too.
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return 0, columnAdded, err
	}

	if current >= target {
		return 0, columnAdded, nil
	}

	// commitEachStep gates the per-migration commit to the production path
	// (upTo==0) on the main source. MigrateUpTo's test-support path (upTo>0)
	// and the dolt-ignored source (its cursor and tables are never committed to
	// shared history) both keep the single terminal commit MigrateUp runs.
	commitEachStep := upTo == 0 && m.cursorTable == mainSource.cursorTable
	count, err := runMigrations(ctx, db, m, current, target, commitEachStep)
	return count, columnAdded, err
}

// runMigrations applies migration files from src where minVersion < version <= upTo.
// Pass upTo=0 to apply through the latest version. It is package-private so
// tests can call it directly without needing a live cursor table.
//
// When commitEachStep is set, each numbered migration is committed atomically
// as it applies (see commitMigrationStep): its ALTERs and cursor row land in a
// Dolt commit before the next migration runs, so a crash/kill/timeout anywhere
// in MigrateUp's later backfill/rekey/ignored tail cannot strand this pass's
// applied migrations uncommitted for a retry to trip over (#4566). A residual
// window remains within a single step: a kill between a migration's SQL and
// its commitMigrationStep still leaves that one step's debris in the working
// set for the dirty-table guard to refuse. The window shrinks from the whole
// pass tail to one step; it does not close entirely.
func runMigrations(ctx context.Context, db DBConn, src migrationSource, minVersion, upTo int, commitEachStep bool) (int, error) {
	if upTo == 0 {
		upTo = src.latest()
	}

	// One-shot large-rig notice, ahead of the migration loop below — gated to
	// the main-source pass only. MigrateUp calls runMigrations once for
	// mainSource and once for ignoredSource in the same pass; without this
	// gate a large rig with pending migrations in both sources would print
	// the warning twice (and issue the COUNT(*) query twice) despite the
	// "one-shot" framing. Treats a missing issues table as "fresh install"
	// and emits nothing — on a first-ever run there is no rig to warn about,
	// and the COUNT(*) query would error on the missing table.
	// issueRowCounter/emitLargeRigNotice predate this call site (be-8ja);
	// this just threads them onto main's existing TTY-gated `stderr` writer
	// instead of a second progressOut.
	if src.cursorTable == mainSource.cursorTable {
		rowCount, rowCountErr := issueRowCounter(ctx, db)
		emitLargeRigNotice(stderr, rowCount, rowCountErr)
	}

	count := 0
	for _, mf := range src.list() {
		if mf.version <= minVersion || mf.version > upTo {
			continue
		}
		data, err := src.files.ReadFile(src.dir + "/" + mf.name)
		if err != nil {
			return count, fmt.Errorf("reading migration %s: %w", mf.name, err)
		}

		// Snapshot the working set BEFORE the pre-migration repair runs, not
		// just before the migration's own SQL. preMigrationRepair (below) can
		// itself mutate synced tables (e.g. #4690's ensureDependenciesIDColumn
		// ALTERs `dependencies`); snapshotting after it ran would misclassify
		// that mutation as pre-existing dirt to exclude from this step's
		// commit, so the repair would sit uncommitted in the working set while
		// the cursor row for this version was already committed -- a killed
		// process between this step and the pass's final commit would leave
		// history claiming the version applied while the repaired table's
		// change was never durably recorded, and the version-gated repair
		// hook cannot re-run to fix it (its version is no longer pending).
		// Snapshotting first makes repair-hook mutations count as this step's
		// own newly-dirtied work, so they land in the same atomic commit as
		// the migration and its cursor row.
		var dirtyBeforeStep map[string]dirtyTableState
		if commitEachStep {
			dirtyBeforeStep, err = dirtyTables(ctx, db, true)
			if err != nil {
				return count, fmt.Errorf("snapshotting dirty tables before %s: %w", mf.name, err)
			}
		}

		if err := src.preMigrationRepair(ctx, db, mf.version); err != nil {
			return count, fmt.Errorf("pre-repair for migration %s: %w", mf.name, err)
		}

		fmt.Fprintf(stderr, "Applying migration %04d: %s…\n", mf.version, humanMigrationName(mf.name))
		start := time.Now()
		if err := execMigrationBody(ctx, db, string(data)); err != nil {
			return count, fmt.Errorf("migration %s: %w", mf.name, err)
		}
		sum := sha256.Sum256(data)
		contentHash := hex.EncodeToString(sum[:])
		if _, err := db.ExecContext(ctx, "INSERT IGNORE INTO "+src.cursorTable+" (version, content_hash) VALUES (?, ?)", mf.version, contentHash); err != nil {
			return count, fmt.Errorf("recording %s in %s: %w", mf.name, src.cursorTable, err)
		}
		count++

		// commitEachStep's DOLT_ADD/DOLT_COMMIT is the expensive, fallible
		// part of this step on the production embedded path. The "done" line
		// (and its timing) must land after that commit succeeds, not before
		// it: printing "done" and then hitting a commit error would show an
		// operator a false completion, and timing that stopped before the
		// commit would understate the step's real cost. A failed commit
		// returns before either print statement below runs.
		if commitEachStep {
			if err := commitMigrationStep(ctx, db, src.cursorTable, mf.name, dirtyBeforeStep); err != nil {
				return count, fmt.Errorf("committing migration %s: %w", mf.name, err)
			}
		}
		fmt.Fprintf(stderr, "  done (%.1fs)\n", time.Since(start).Seconds())

		if migrateStepFaultHook != nil {
			if err := migrateStepFaultHook(ctx, db, mf.version); err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

// migrateStepFaultHook is a test-only seam. When non-nil it runs at the end of
// each applied migration step (after the step's per-step commit on the
// production path); returning an error aborts the pass, emulating a
// crash/kill/timeout mid-migration so tests can prove the retry converges.
// Production leaves it nil.
var migrateStepFaultHook func(ctx context.Context, db DBConn, version int) error

// SetMigrateStepFaultHookForTest installs fn as the per-step migration fault
// hook and returns a function that restores the previous value. Test-only.
func SetMigrateStepFaultHookForTest(fn func(ctx context.Context, db DBConn, version int) error) func() {
	prev := migrateStepFaultHook
	migrateStepFaultHook = fn
	return func() { migrateStepFaultHook = prev }
}

// commitMigrationStep commits one numbered migration atomically: the tables it
// newly dirtied (diffed against dirtyBeforeStep, the working set snapshotted
// before the migration ran) plus the cursor row that records it. It force-
// stages only those tables, so pre-existing writes to tables the migration did
// not touch stay in the working set, and tolerates "nothing to commit" for a
// migration whose SQL was an idempotent no-op.
//
// MigrateUp's pre-flight guard proves no table a pending migration touches
// holds uncommitted user rows before the pass runs, so every table this step
// newly dirties is the migration's own DDL/DML — safe to commit on its own.
func commitMigrationStep(ctx context.Context, db DBConn, cursorTable, migrationName string, dirtyBeforeStep map[string]dirtyTableState) error {
	dirtyAfter, err := dirtyTables(ctx, db, true)
	if err != nil {
		return err
	}
	tableSet := map[string]struct{}{cursorTable: {}}
	for table := range dirtyAfter {
		if _, wasDirty := dirtyBeforeStep[table]; wasDirty {
			continue
		}
		tableSet[table] = struct{}{}
	}
	tables := make([]string, 0, len(tableSet))
	for table := range tableSet {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', ?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', ?)", "schema: apply migration "+migrationName); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			return fmt.Errorf("committing migration step: %w", err)
		}
	}
	return nil
}
