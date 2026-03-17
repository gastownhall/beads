package fix

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// FixMissingMetadata checks and repairs missing metadata fields in a Dolt database.
// Fields checked: bd_version, repo_id, clone_id.
// The bdVersion parameter should be the current CLI version string (from the caller
// in package main, since this package cannot import it directly).
// Returns nil if all fields are present or successfully repaired.
func FixMissingMetadata(path string, bdVersion string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	info, err := resolveRuntimeInfoForRepo(path)
	if err != nil {
		return nil // Can't load config, nothing to fix
	}
	cfg := info.Config
	if info.SourceConfig == nil && info.SourceErr == nil && info.Runtime != nil && info.Runtime.SourceBeadsDir == info.Runtime.BeadsDir {
		return nil // No source metadata.json, nothing to fix
	}
	if cfg == nil {
		return nil // No config file, nothing to fix
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not a Dolt backend, nothing to fix
	}

	ctx := context.Background()

	store, err := openDoltStoreForRepoPath(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	var repaired []string

	// Check and repair bd_version
	if val, err := store.GetMetadata(ctx, "bd_version"); err == nil && val == "" {
		if bdVersion != "" {
			if err := store.SetMetadata(ctx, "bd_version", bdVersion); err != nil {
				return fmt.Errorf("failed to set bd_version metadata: %w", err)
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
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	info, err := resolveRuntimeInfoForRepo(path)
	if err != nil {
		return fmt.Errorf("failed to load metadata.json: %w", err)
	}
	cfg, saveDir := metadataConfigForRepo(info)
	if cfg == nil {
		return fmt.Errorf("no metadata.json found")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not a Dolt backend
	}

	ctx := context.Background()

	store, err := openDoltStoreForRepoPath(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Check current state
	hasLocalID := cfg.ProjectID != ""
	dbID, _ := store.GetMetadata(ctx, "_project_id")
	hasDBID := dbID != ""

	if hasLocalID && hasDBID {
		return nil // Both already set
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
		if err := cfg.Save(saveDir); err != nil {
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
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	info, err := resolveRuntimeInfoForRepo(path)
	if err != nil {
		return nil // No config, nothing to fix
	}
	cfg, saveDir := metadataConfigForRepo(info)
	if cfg == nil {
		return nil // No config, nothing to fix
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not Dolt backend
	}

	// Only fix if dolt_database is missing (using default)
	if cfg.DoltDatabase != "" {
		return nil // Already configured explicitly
	}

	// Connect to the server and probe for the correct database
	db, err := openDoltDBForRepoPath(path)
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
	if err := cfg.Save(saveDir); err != nil {
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
