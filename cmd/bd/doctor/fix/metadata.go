package fix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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
// database. This fix probes the server for a database with beads tables and backfills
// the config. (GH#2160)
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

	// Connect to the server and probe for the correct database
	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  dolt_database fix skipped (server not reachable: %v)\n", err)
		return nil
	}
	defer db.Close()

	correctDB := probeForCorrectDoltDatabase(db, configfile.DefaultDoltDatabase)
	if correctDB == "" {
		return nil // No alternate database found
	}

	// Backfill dolt_database in metadata.json
	cfg.DoltDatabase = correctDB
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to save metadata.json: %w", err)
	}

	fmt.Printf("  Fixed dolt_database: set to %q in metadata.json (was using default %q)\n",
		correctDB, configfile.DefaultDoltDatabase)
	return nil
}

// probeForCorrectDoltDatabase checks if another database on the server has the
// expected beads tables (issues, dependencies, config). Returns the database name
// if found, empty string otherwise.
func probeForCorrectDoltDatabase(db *sql.DB, skipDB string) string {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return ""
	}
	defer rows.Close()

	skip := map[string]bool{
		"information_schema": true,
		"mysql":              true,
		skipDB:               true,
	}

	var candidates []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			continue
		}
		if skip[dbName] {
			continue
		}
		// Skip known test databases
		if strings.HasPrefix(dbName, "testdb_") || strings.HasPrefix(dbName, "doctest_") ||
			strings.HasPrefix(dbName, "doctortest_") {
			continue
		}
		candidates = append(candidates, dbName)
	}

	for _, dbName := range candidates {
		var count int
		//nolint:gosec // G201: dbName from SHOW DATABASES, not user input
		err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM `%s`.issues LIMIT 1", dbName)).Scan(&count)
		if err == nil {
			return dbName
		}
	}

	return ""
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
			from := cfg.GetDoltDatabase()
			cfg.DoltDatabase = matches[0].Name
			return true, fmt.Sprintf("repaired dolt_database: %q -> %q using project_id %s", from, matches[0].Name, cfg.ProjectID), nil
		}
	}

	current, ok := byName[cfg.GetDoltDatabase()]
	if ok && current.HasSchema && current.ProjectID != "" && cfg.ProjectID != current.ProjectID {
		from := cfg.ProjectID
		cfg.ProjectID = current.ProjectID
		if from == "" {
			return true, fmt.Sprintf("backfilled project_id %s from database %q", current.ProjectID, current.Name), nil
		}
		return true, fmt.Sprintf("repaired project_id: %s -> %s from database %q", from, current.ProjectID, current.Name), nil
	}

	if cfg.ProjectID == "" && len(schemaCandidates) == 1 {
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
	db, err := openServerCatalogDB(beadsDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("server metadata probe failed: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
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

		// Escape backticks in database name to prevent SQL injection (` → ``)
		safeName := strings.ReplaceAll(dbName, "`", "``")

		var count int
		//nolint:gosec // G201: identifier-escaped, dbName from SHOW DATABASES
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`.issues LIMIT 1", safeName)).Scan(&count); err == nil {
			meta.HasSchema = true
		} else if !isExpectedProbeError(err) {
			return nil, fmt.Errorf("probing database %q for schema: %w", dbName, err)
		}

		var projectID string
		//nolint:gosec // G201: identifier-escaped, dbName from SHOW DATABASES
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT value FROM `%s`.metadata WHERE `key` = '_project_id' LIMIT 1", safeName),
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
// simply doesn't exist — safe to treat as "not present". Permission errors,
// connection failures, and other unexpected errors should be propagated so
// that reconciliation doesn't act on an incomplete inventory.
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
		}
	}
	return false
}

func openServerCatalogDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	port := doltserver.DefaultConfig(beadsDir).Port
	connStr := doltutil.ServerDSN{
		Host:     cfg.GetDoltServerHost(),
		Port:     port,
		User:     cfg.GetDoltServerUser(),
		Password: cfg.GetDoltServerPasswordForPort(port),
		TLS:      cfg.GetDoltServerTLS(),
	}.String()
	return sql.Open("mysql", connStr)
}
