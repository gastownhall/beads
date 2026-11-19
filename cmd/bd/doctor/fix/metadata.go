package fix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

type serverDatabaseMetadata struct {
	Name      string
	HasSchema bool
	ProjectID string
}

var listServerMetadataDatabases = inspectServerMetadataDatabases

// FixMissingMetadata checks and repairs missing metadata fields in a Dolt database.
// Fields checked: bd_version, repo_id, clone_id.
// The bdVersion parameter should be the current CLI version string (from the caller
// in package main, since this package cannot import it directly).
// Returns nil if all fields are present or successfully repaired.
func FixMissingMetadata(path string, bdVersion string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil // Can't load config, nothing to fix
	}
	if cfg == nil {
		return nil // No config file, nothing to fix
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not a Dolt backend, nothing to fix
	}

	ctx := context.Background()

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	var repaired []string

	// Check and repair bd_version (clone-local, dolt-ignored)
	if val, err := store.GetLocalMetadata(ctx, "bd_version"); err == nil && val == "" {
		if bdVersion != "" {
			if err := store.SetLocalMetadata(ctx, "bd_version", bdVersion); err != nil {
				return fmt.Errorf("failed to set bd_version local metadata: %w", err)
			}
			repaired = append(repaired, "bd_version")
		}
	}

	// Check and repair repo_id
	if val, err := store.GetMetadata(ctx, "repo_id"); err == nil && val == "" {
		repoID, err := beads.ComputeRepoIDForPath(path)
		if err != nil {
			// Non-git environment: warn and skip (FR-015)
			fmt.Printf("  Warning: could not compute repo_id (not in a git repo?): %v\n", err)
		} else {
			if err := store.SetMetadata(ctx, "repo_id", repoID); err != nil {
				return fmt.Errorf("failed to set repo_id metadata: %w", err)
			}
			repaired = append(repaired, "repo_id")
		}
	}

	// Check and repair clone_id
	if val, err := store.GetMetadata(ctx, "clone_id"); err == nil && val == "" {
		cloneID, err := beads.GetCloneIDForPath(path)
		if err != nil {
			// Non-standard environment: warn and skip (FR-016)
			fmt.Printf("  Warning: could not compute clone_id: %v\n", err)
		} else {
			if err := store.SetMetadata(ctx, "clone_id", cloneID); err != nil {
				return fmt.Errorf("failed to set clone_id metadata: %w", err)
			}
			repaired = append(repaired, "clone_id")
		}
	}

	// Report results (FR-011: count and names; FR-012: silent if none)
	if len(repaired) > 0 {
		fmt.Printf("  Repaired %d metadata field(s): %s\n", len(repaired), strings.Join(repaired, ", "))
	}

	return nil
}

// FixProjectIdentity generates a project_id UUID and backfills it into both
// metadata.json and the database metadata table. For pre-GH#2372 projects that
// lack cross-project identity verification.
func FixProjectIdentity(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to load metadata.json: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("no metadata.json found")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not a Dolt backend
	}

	if msg, err := resolveAuthoritativeServerMetadata(path, cfg, true); err != nil {
		return err
	} else if msg != "" {
		fmt.Printf("  %s\n", msg)
		return nil
	}

	ctx := context.Background()

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Check current state
	hasLocalID := cfg.ProjectID != ""
	dbID, _ := store.GetMetadata(ctx, "_project_id")
	hasDBID := dbID != ""

	if hasLocalID && hasDBID {
		if cfg.ProjectID == dbID {
			return nil // Both already set
		}
		return fmt.Errorf(
			"project identity mismatch persists after repair attempt: metadata.json=%s, database=%s",
			cfg.ProjectID,
			dbID,
		)
	}

	// Determine the ID to use: prefer an existing one, otherwise generate new
	projectID := cfg.ProjectID
	if projectID == "" {
		projectID = dbID
	}
	if projectID == "" {
		projectID = configfile.GenerateProjectID()
	}

	var repaired []string

	// Backfill metadata.json
	if !hasLocalID {
		cfg.ProjectID = projectID
		if err := cfg.Save(beadsDir); err != nil {
			return fmt.Errorf("failed to save project_id to metadata.json: %w", err)
		}
		repaired = append(repaired, "metadata.json")
	}

	// Backfill database
	if !hasDBID {
		if err := store.SetMetadata(ctx, "_project_id", projectID); err != nil {
			return fmt.Errorf("failed to write _project_id to database: %w", err)
		}
		repaired = append(repaired, "database")
	}

	fmt.Printf("  Backfilled project_id %s into: %s\n", projectID, strings.Join(repaired, ", "))
	return nil
}

// FixMissingDoltDatabase detects and repairs missing dolt_database in metadata.json.
// Pre-#2142 migrations created databases without writing dolt_database to config,
// so after upgrading, bd falls back to default "beads" (empty) instead of the real
// database (GH#2160). The authoritative reconcile runs first; when it makes no
// change, a fallback selects from the same identity-aware server catalog, adopting
// another database only when the selection is unambiguous (GH#5576): an exact
// project_id match, or a sole candidate without an identity on a non-shared
// server. Unsafe or ambiguous selections are skipped with the candidates and the
// manual remedy, never silently taken by enumeration order.
func FixMissingDoltDatabase(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil // No config, nothing to fix
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not Dolt backend
	}

	if msg, err := resolveAuthoritativeServerMetadata(path, cfg, true); err != nil {
		return err
	} else if msg != "" {
		fmt.Printf("  %s\n", msg)
		return nil
	}

	// Only fall back to schema-only probing when dolt_database is missing.
	if cfg.DoltDatabase != "" {
		return nil
	}

	// GH#5576: select from the same identity-aware server catalog the
	// authoritative reconcile reads, never a raw first-match probe. The
	// reconcile above already declined to auto-adopt in exactly the risky
	// cases (shared-server mode without a project_id anchor, conflicting or
	// ambiguous identity); this fallback must not undo those refusals by
	// adopting whatever database enumerates first. A catalog read failure
	// here is a skip, not an error: the reconcile's own probe failures
	// still propagate above.
	databases, err := listServerMetadataDatabases(beadsDir, cfg)
	if err != nil {
		return skipFix(err)
	}
	selected, err := selectFallbackDoltDatabase(cfg.ProjectID, cfg.GetDoltDatabase(), doltserver.IsSharedServerMode(), databases)
	if err != nil {
		return err
	}
	if selected == nil {
		return nil // Nothing safe to adopt
	}

	// Backfill dolt_database in metadata.json. A known local project_id is
	// preserved; an absent one is backfilled from the selected candidate's
	// identity, as the authoritative sole-candidate adoption does.
	cfg.DoltDatabase = selected.Name
	if cfg.ProjectID == "" && selected.ProjectID != "" {
		cfg.ProjectID = selected.ProjectID
		if err := cfg.Save(beadsDir); err != nil {
			return fmt.Errorf("failed to save metadata.json: %w", err)
		}
		fmt.Printf("  Fixed dolt_database: set to %q in metadata.json (was using default %q); backfilled project_id %s\n",
			selected.Name, configfile.DefaultDoltDatabase, remedy.PrintedValue(selected.ProjectID))
		return nil
	}
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to save metadata.json: %w", err)
	}

	fmt.Printf("  Fixed dolt_database: set to %q in metadata.json (was using default %q)\n",
		selected.Name, configfile.DefaultDoltDatabase)
	return nil
}

// fallbackManualRemedy is the manual remedy every fallback selection skip
// names. <name> stays a literal placeholder: no server-supplied value is ever
// spliced into a shell instruction.
const fallbackManualRemedy = "set the correct database with `bd dolt set database <name>`"

// fallbackDatabaseEligible reports whether db may be adopted by the
// FixMissingDoltDatabase fallback. Eligible databases have Beads schema and
// are not system databases, test fixtures (the testdb_/doctest_/doctortest_
// prefixes), or the global issue database. The literal "beads" name is not
// excluded: the effective current database name is a separate input to the
// selector, and a workspace's real database can legitimately be named beads.
func fallbackDatabaseEligible(db serverDatabaseMetadata) bool {
	if !db.HasSchema {
		return false
	}
	if db.Name == "information_schema" || db.Name == "mysql" {
		return false
	}
	if db.ProjectID == doltserver.GlobalProjectID {
		return false
	}
	if strings.HasPrefix(db.Name, "testdb_") || strings.HasPrefix(db.Name, "doctest_") ||
		strings.HasPrefix(db.Name, "doctortest_") {
		return false
	}
	return true
}

// selectFallbackDoltDatabase selects the server database FixMissingDoltDatabase's
// schema-only fallback may adopt (GH#5576), from the same identity-aware catalog
// the authoritative reconcile reads. It is pure: the caller passes the local
// project_id, the effective current database (configfile GetDoltDatabase, which
// honors the environment), and the shared-server mode it runs in. A returned
// database is a catalog copy; nil/nil means no applicable repair.
//
// Eligible alternatives are those fallbackDatabaseEligible admits; the effective
// current database is kept aside for the safety checks regardless of
// eligibility. The rules, in order:
//
//  1. local project_id set, multiple eligible databases carry it: hard error
//     (the reconcile's ambiguous-match refusal).
//  2. local project_id set, exactly one eligible database carries it: select
//     it, unless a different current database with schema asserts a
//     conflicting project_id — the reconcile's conflict refusal. This also
//     covers a mode-gated reconcile that did not run.
//  3. no eligible alternatives: no rewrite.
//  4. local project_id set but no eligible alternative carries it: skip.
//     Absence of identity is not a match, even for a sole candidate, in
//     either mode.
//  5. no local project_id, shared-server mode: skip, sole candidate or
//     several: without an anchor, adoption can redirect this workspace to
//     another project's data.
//  6. no local project_id, non-shared mode, exactly one eligible
//     alternative: select it — the GH#2160 legacy migration recovery. The
//     caller backfills an absent local project_id from the selected
//     candidate's identity.
//  7. no local project_id, non-shared mode, several eligible alternatives:
//     skip; never choose by enumeration order.
//
// Every skip says why selection is unsafe, lists the eligible alternatives in
// sorted order, and names the manual remedy. Server-supplied names and IDs are
// rendered with remedy.PrintedValue/PrintedList so they cannot redraw the
// diagnostic.
func selectFallbackDoltDatabase(localProjectID, currentDatabase string, sharedServerMode bool, databases []serverDatabaseMetadata) (*serverDatabaseMetadata, error) {
	var eligible []serverDatabaseMetadata
	var current serverDatabaseMetadata
	hasCurrent := false
	for _, db := range databases {
		if db.Name == currentDatabase {
			current = db
			hasCurrent = true
		}
		if fallbackDatabaseEligible(db) {
			eligible = append(eligible, db)
		}
	}

	eligibleNames := make([]string, 0, len(eligible))
	for _, db := range eligible {
		eligibleNames = append(eligibleNames, db.Name)
	}
	sort.Strings(eligibleNames)

	if localProjectID != "" {
		var matches []serverDatabaseMetadata
		for _, db := range eligible {
			if db.ProjectID == localProjectID {
				matches = append(matches, db)
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf(
				"multiple server databases match project_id %s: %s",
				remedy.PrintedValue(localProjectID),
				remedy.PrintedList(eligibleNames, ", "))
		}
		if len(matches) == 1 {
			selected := matches[0]
			// Safety guard, the reconcile's: when a different current database
			// has its own project_id that disagrees with the local one the
			// match carries, the identity signals conflict, and auto-switching
			// can silently attach this workspace to another project.
			if hasCurrent && current.Name != selected.Name && current.HasSchema &&
				current.ProjectID != "" && current.ProjectID != localProjectID {
				return nil, fmt.Errorf(
					"conflicting project identity: metadata.json project_id %s matches database %s, but configured database %s reports project_id %s",
					remedy.PrintedValue(localProjectID),
					remedy.PrintedValue(selected.Name),
					remedy.PrintedValue(current.Name),
					remedy.PrintedValue(current.ProjectID))
			}
			return &selected, nil
		}
	}

	if len(eligible) == 0 {
		return nil, nil
	}

	if localProjectID != "" {
		return nil, skipFix(fmt.Errorf(
			"no server database with Beads schema carries project_id %s (candidates: %s); %s",
			remedy.PrintedValue(localProjectID),
			remedy.PrintedList(eligibleNames, ", "),
			fallbackManualRemedy))
	}

	if sharedServerMode {
		return nil, skipFix(fmt.Errorf(
			"shared Dolt server and no local project_id to anchor selection (candidates: %s); adopting one can redirect this workspace to another project's data; %s",
			remedy.PrintedList(eligibleNames, ", "),
			fallbackManualRemedy))
	}

	if len(eligible) == 1 {
		// The GH#2160 legacy migration recovery: a sole schema-matching
		// database on the workspace's own server is the one this workspace's
		// pre-#2142 migration created. Shared-server mode never gets here
		// (declined above): there, a sole candidate can belong to a
		// different workspace.
		selected := eligible[0]
		return &selected, nil
	}

	return nil, skipFix(fmt.Errorf(
		"multiple server databases have Beads schema and no local project_id disambiguates them (candidates: %s); %s",
		remedy.PrintedList(eligibleNames, ", "),
		fallbackManualRemedy))
}

// ResolveAuthoritativeServerMetadata reconciles local metadata.json against the
// authoritative server state. In shared-server/server mode this repairs two
// drift cases without guessing across unrelated projects:
//  1. A stale dolt_database when another server DB matches the local project_id.
//  2. A stale/missing local project_id when the currently configured DB has one.
func ResolveAuthoritativeServerMetadata(path string, apply bool) (*configfile.Config, string, error) {
	if err := validateBeadsWorkspace(path); err != nil {
		return nil, "", err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return cfg, "", err
	}
	msg, err := resolveAuthoritativeServerMetadata(path, cfg, apply)
	return cfg, msg, err
}

func resolveAuthoritativeServerMetadata(path string, cfg *configfile.Config, apply bool) (string, error) {
	if cfg == nil || cfg.GetBackend() != configfile.BackendDolt || !(cfg.IsDoltServerMode() || doltserver.IsSharedServerMode()) {
		return "", nil
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	databases, err := listServerMetadataDatabases(beadsDir, cfg)
	if err != nil {
		return "", err
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, databases)
	if err != nil || !changed {
		return msg, err
	}
	if apply {
		if err := cfg.Save(beadsDir); err != nil {
			return "", fmt.Errorf("failed to save metadata.json: %w", err)
		}
		return msg, nil
	}
	return "would " + msg, nil
}

func reconcileAuthoritativeServerMetadata(cfg *configfile.Config, databases []serverDatabaseMetadata) (bool, string, error) {
	if cfg == nil {
		return false, "", nil
	}

	byName := make(map[string]serverDatabaseMetadata, len(databases))
	var schemaCandidates []serverDatabaseMetadata
	for _, db := range databases {
		byName[db.Name] = db
		if db.HasSchema {
			schemaCandidates = append(schemaCandidates, db)
		}
	}
	current, hasCurrent := byName[cfg.GetDoltDatabase()]

	if cfg.ProjectID != "" {
		var matches []serverDatabaseMetadata
		for _, db := range schemaCandidates {
			if db.ProjectID == cfg.ProjectID {
				matches = append(matches, db)
			}
		}
		if len(matches) > 1 {
			names := make([]string, 0, len(matches))
			for _, match := range matches {
				names = append(names, match.Name)
			}
			sort.Strings(names)
			return false, "", fmt.Errorf(
				"multiple server databases match project_id %s: %s",
				cfg.ProjectID,
				strings.Join(names, ", "),
			)
		}
		if len(matches) == 1 && cfg.DoltDatabase != matches[0].Name {
			// Safety guard: when the configured database has its own project_id that
			// disagrees with metadata.json, and a different database matches the local
			// project_id, we have conflicting identity signals. Auto-switching databases
			// here can silently attach this workspace to another project.
			if hasCurrent && current.HasSchema && current.ProjectID != "" && current.ProjectID != cfg.ProjectID {
				return false, "", fmt.Errorf(
					"conflicting project identity: metadata.json project_id %s matches database %q, but configured database %q reports project_id %s",
					cfg.ProjectID,
					matches[0].Name,
					current.Name,
					current.ProjectID,
				)
			}
			from := cfg.GetDoltDatabase()
			cfg.DoltDatabase = matches[0].Name
			return true, fmt.Sprintf("repaired dolt_database: %q -> %q using project_id %s", from, matches[0].Name, cfg.ProjectID), nil
		}
	}

	if hasCurrent && current.HasSchema && current.ProjectID != "" && cfg.ProjectID != current.ProjectID {
		from := cfg.ProjectID
		cfg.ProjectID = current.ProjectID
		if from == "" {
			return true, fmt.Sprintf("backfilled project_id %s from database %q", current.ProjectID, current.Name), nil
		}
		return true, fmt.Sprintf("repaired project_id: %s -> %s from database %q", from, current.ProjectID, current.Name), nil
	}

	// In shared-server mode, a sole schema candidate can belong to a different
	// workspace. Without a local project_id anchor, auto-adopting that database
	// can redirect this workspace to another project's data.
	if cfg.ProjectID == "" && len(schemaCandidates) == 1 && !doltserver.IsSharedServerMode() {
		candidate := schemaCandidates[0]
		var repairs []string
		if cfg.DoltDatabase != candidate.Name {
			repairs = append(repairs, fmt.Sprintf("dolt_database: %q -> %q", cfg.GetDoltDatabase(), candidate.Name))
			cfg.DoltDatabase = candidate.Name
		}
		if candidate.ProjectID != "" {
			repairs = append(repairs, fmt.Sprintf("project_id: %s", candidate.ProjectID))
			cfg.ProjectID = candidate.ProjectID
		}
		if len(repairs) > 0 {
			return true, "repaired metadata from the only server database with Beads schema (" + strings.Join(repairs, "; ") + ")", nil
		}
	}

	return false, "", nil
}

func inspectServerMetadataDatabases(beadsDir string, cfg *configfile.Config) ([]serverDatabaseMetadata, error) {
	// The catalog read is of the server the store dials, so the guard below
	// and the dial take one endpoint (doltserver.ResolveEndpoint).
	ep := doltserver.ResolveEndpoint(beadsDir, cfg)
	// With no address there is no server catalog to read: never dial
	// 127.0.0.1:0. When a bd command will start the workspace's own server,
	// its databases are the ones in the data directory that server will
	// serve; with none there (a fresh clone of a `bd init --server` project,
	// before its first open) there is nothing to reconcile. Otherwise the
	// reconcile cannot run, and says why: every store open refuses too, with
	// this advice, or the server is merely stopped over existing databases.
	if !ep.Dialable() {
		if !ep.BDStartsServer() {
			return nil, ep.UnresolvedError()
		}
		dataDir := doltserver.ResolveDoltDir(beadsDir)
		if !dataDirHoldsDatabase(dataDir) {
			return nil, nil
		}
		return nil, fmt.Errorf("this workspace's Dolt server is not running, so the databases in %s could not be read to reconcile metadata.json; "+
			"start the server with 'bd dolt start' (or any bd command), then re-run this command", remedy.PrintedValue(dataDir))
	}
	// A server only a unix socket names has no port, and the catalog read
	// stays TCP-only (openServerCatalogDB): refuse it here, never dial
	// 127.0.0.1:0. bd bootstrap's later steps cannot reach such a server yet
	// either, so bootstrap stops at this reconcile and writes nothing.
	if !ep.Resolved() {
		return nil, errors.New("only a unix socket names this workspace's Dolt server (no port resolved), and the metadata reconcile does not read a server's catalog over a socket yet")
	}
	db, err := openServerCatalogDB(ep)
	if err != nil {
		return nil, fmt.Errorf("server metadata probe failed: %w", err)
	}
	defer db.Close()

	return readServerMetadataDatabases(context.Background(), db)
}

// readServerMetadataDatabases reads the catalog of the server at db: every
// non-system database, whether it has Beads schema (an issues table), and the
// project_id in its metadata table. The endpoint checks and connection
// ownership stay with inspectServerMetadataDatabases; this is the SQL body,
// so its actual queries stay testable with sqlmock. A database the beads user
// cannot read is cataloged without schema or identity (isExpectedProbeError),
// never an error: shared sql-servers often host unrelated databases (GH#4931).
func readServerMetadataDatabases(ctx context.Context, db *sql.DB) ([]serverDatabaseMetadata, error) {
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var databases []serverDatabaseMetadata
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return nil, err
		}
		if dbName == "information_schema" || dbName == "mysql" {
			continue
		}
		meta := serverDatabaseMetadata{Name: dbName}

		quotedName := doltutil.QuoteIdentifierUnvalidated(dbName)

		var count int
		//nolint:gosec // G201: identifier quoted+escaped via doltutil.QuoteIdentifierUnvalidated
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.issues LIMIT 1", quotedName)).Scan(&count); err == nil {
			meta.HasSchema = true
		} else if !isExpectedProbeError(err) {
			return nil, fmt.Errorf("probing database %q for schema: %w", dbName, err)
		}

		var projectID string
		//nolint:gosec // G201: identifier quoted+escaped via doltutil.QuoteIdentifierUnvalidated
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT value FROM %s.metadata WHERE `key` = '_project_id' LIMIT 1", quotedName),
		).Scan(&projectID); err == nil {
			meta.ProjectID = projectID
		} else if !isExpectedProbeError(err) {
			return nil, fmt.Errorf("probing database %q for project_id: %w", dbName, err)
		}

		databases = append(databases, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return databases, nil
}

// isExpectedProbeError returns true for errors that indicate the table/row
// simply doesn't exist — or that this user cannot inspect that database —
// safe to treat as "not present" when enumerating SHOW DATABASES.
//
// GH#4931: shared sql-servers often host non-beads databases the beads
// user cannot read. Access denied on those peers must not abort bootstrap
// / metadata reconciliation for the configured beads database.
//
// Hard connection failures (server down, auth to the catalog itself) still
// surface via openServerCatalogDB / SHOW DATABASES, not this probe helper.
func isExpectedProbeError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1049: // Unknown database
			return true
		case 1146: // Table doesn't exist
			return true
		case 1054: // Unknown column
			return true
		case 1044: // Access denied for user to database
			return true
		case 1142: // Access denied for user to table
			return true
		case 1143: // Access denied for user to column
			return true
		case 1227: // Access denied (requires privilege)
			return true
		case 1105: // HY000 — Dolt often wraps access denied here (GH#4931)
			if strings.Contains(strings.ToLower(mysqlErr.Message), "access denied") {
				return true
			}
		}
	}
	// Driver / wrapper may not expose MySQLError; match the common message.
	if strings.Contains(strings.ToLower(err.Error()), "access denied") {
		return true
	}
	return false
}

// openServerCatalogDB opens the catalog of the server at ep, the endpoint the
// store dials: its port (DialConfig's, the legacy BEADS_DOLT_PORT included,
// which DefaultConfig leaves out) and the password keyed to that port. With
// DefaultConfig's port a workspace only BEADS_DOLT_PORT names had its catalog
// read at 127.0.0.1:0, or at a port file's server no store open dials.
//
// It dials TCP only; inspectServerMetadataDatabases refuses a socket-only
// server before calling it. Like openFixDB, it does not check the server
// against doltversion.RequiredMin: bd doctor --fix is exempt from the floor.
func openServerCatalogDB(ep doltserver.Endpoint) (*sql.DB, error) {
	password := ep.Password()
	if password != "" {
		// Refuse to send BEADS_DOLT_PASSWORD to a shared-config host the
		// workspace never pinned, as the store's open does (GH#7886).
		if err := ep.EnsureTrustedForCredential(nil); err != nil {
			return nil, err
		}
	}
	connStr := doltutil.ServerDSN{
		Host:     ep.Host,
		Port:     ep.Port,
		User:     ep.User,
		Password: password,
		TLS:      ep.TLS,
	}.String()
	return sql.Open("mysql", connStr)
}

// dataDirHoldsDatabase reports whether dir, a Dolt server's data directory,
// holds a database: a subdirectory with a .dolt directory.
func dataDirHoldsDatabase(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, entry.Name(), ".dolt")); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
