// Package historicalsqlite recognizes a historical SQLite workspace: a .beads
// directory a pre-Dolt bd left behind, whose SQLite database is still the only
// copy of its data. Command admission (cmd/bd's guardLegacyUpgradeWorkspace)
// refuses one before anything can open or rewrite it, and bd doctor's Classic
// Artifacts check and cleanup (cmd/bd/doctor/fix.SelectsDolt) leave its SQLite
// files alone. Both ask this package, so they recognize the same layouts.
package historicalsqlite

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// IsWorkspace reports whether beadsDir, whose metadata is cfg (nil when it has
// none), is a historical SQLite workspace. It holds no embedded Dolt
// repository, and either its metadata names a bare on-disk SQLite database and
// no Dolt configuration (configuredHistoricalSQLiteWorkspace), or it has no
// metadata and its only database is a SQLite file
// (discoveredHistoricalSQLiteWorkspace). Such metadata usually predates the
// backend key, and configfile.Config.GetBackend reads an empty backend as Dolt.
func IsWorkspace(beadsDir string, cfg *configfile.Config) bool {
	if beadsDir == "" {
		return false
	}
	if embeddeddolt.HasRepository(beadsDir) {
		return false
	}
	if cfg != nil {
		return configuredHistoricalSQLiteWorkspace(beadsDir, cfg)
	}
	return discoveredHistoricalSQLiteWorkspace(beadsDir)
}

// configuredHistoricalSQLiteWorkspace classifies a metadata-backed workspace
// whose config names a bare on-disk SQLite database. It selects the database
// name from the config exactly as legacy bd did and confirms the file exists
// beneath beadsDir; a non-SQLite backend, any Dolt configuration, or a database
// path that is not a plain basename all disqualify it.
func configuredHistoricalSQLiteWorkspace(beadsDir string, cfg *configfile.Config) bool {
	if cfg.Backend != "" && cfg.Backend != configfile.BackendSQLite {
		return false
	}
	if cfg.DoltMode != "" || cfg.DoltDatabase != "" {
		return false
	}
	databaseName := cfg.SQLitePath
	if databaseName == "" {
		databaseName = cfg.Database
	}
	if databaseName == "" {
		databaseName = "beads.db"
	}
	if filepath.Base(databaseName) != databaseName {
		return false
	}
	return isNonEmptyRegularFile(filepath.Join(beadsDir, databaseName))
}

// discoveredHistoricalSQLiteWorkspace classifies a metadata-less workspace by
// its on-disk layout: exactly one non-empty regular .db file carrying a real
// SQLite header, and no entries beyond that database, its WAL/SHM sidecars, an
// optional issues.jsonl export, and an embeddeddolt directory.
func discoveredHistoricalSQLiteWorkspace(beadsDir string) bool {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return false
	}
	databaseName, ok := soleLegacySQLiteDatabase(beadsDir, entries)
	if !ok {
		return false
	}
	if !hasSQLiteHeader(filepath.Join(beadsDir, databaseName)) {
		return false
	}
	return onlyLegacySQLiteEntries(beadsDir, entries, databaseName)
}

// soleLegacySQLiteDatabase returns the single .db filename directly beneath a
// metadata-less workspace. It fails closed if any entry is a symlink, if a .db
// file is empty or not a regular file, or if more than one .db file is present.
func soleLegacySQLiteDatabase(beadsDir string, entries []os.DirEntry) (string, bool) {
	databaseName := ""
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return "", false
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".db") {
			continue
		}
		if !isNonEmptyRegularFile(filepath.Join(beadsDir, name)) || databaseName != "" {
			return "", false
		}
		databaseName = name
	}
	return databaseName, databaseName != ""
}

// onlyLegacySQLiteEntries reports whether every entry beneath a metadata-less
// workspace belongs to the recognized legacy SQLite shape: the database file,
// its WAL/SHM sidecars, an optional issues.jsonl export, and an embeddeddolt
// directory. Any other entry, or an expected entry that is not a regular file,
// disqualifies the workspace.
func onlyLegacySQLiteEntries(beadsDir string, entries []os.DirEntry, databaseName string) bool {
	for _, entry := range entries {
		name := entry.Name()
		if name == "embeddeddolt" && entry.IsDir() {
			continue
		}
		if name == databaseName || name == "issues.jsonl" || name == databaseName+"-wal" || name == databaseName+"-shm" {
			if !isRegularFile(filepath.Join(beadsDir, name)) {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func isNonEmptyRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func hasSQLiteHeader(path string) bool {
	if !isNonEmptyRegularFile(path) {
		return false
	}
	file, err := os.Open(path) // #nosec G304 -- bounded header read beneath selected workspace
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, len("SQLite format 3\x00"))
	if _, err := io.ReadFull(file, header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
