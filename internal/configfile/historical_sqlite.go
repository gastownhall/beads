package configfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LegacyWorkspaceError refuses a workspace a release before this one left in a
// layout this bd must not open or modify until it is explicitly migrated: a
// historical SQLite workspace (CheckHistoricalSQLiteWorkspace), or one of the
// legacy Dolt layouts command admission recognizes. Command admission returns
// it, for every such layout, for the workspace bd runs in. Every path that
// opens or inspects a workspace bd was not launched in returns the same error,
// with the same text, for a historical SQLite workspace before it writes
// anything there (LoadDiscoveredForOpen); the legacy Dolt layouts depend on
// the workspace's server mode and version witness, which the CLI resolves, so
// the CLI returns it for those on every path that opens a workspace it was
// not launched in (routed, external, create, planning and doctor's
// additional-repo opens), and the library opens do not. The CLI's
// exceptions, where it admits a legacy Dolt layout bd run inside the workspace
// refuses: a launcher whose merged config enables dolt.shared-server admits a
// legacy Dolt workspace whose config.yaml sets no dolt.shared-server and whose
// metadata names no dolt_mode; and server intent set in a layer the foreign
// classification does not resolve for the target (user-level config, the BD_
// environment, either workspace's .beads/.env) can admit one. For example,
// when Dolt server mode comes only from the user-level config.yaml or
// BD_DOLT_MODE/BD_DOLT_HOST and the metadata names no dolt_mode, or only from
// the workspace's own .beads/.env, a legacy Dolt server workspace whose
// version witness names bd 0.55-0.62 with no .beads/dolt root, or that has an
// embedded repository beside its legacy root or witness, is admitted. Match it
// with errors.As.
type LegacyWorkspaceError struct {
	// Reason names the layout, such as "historical SQLite workspace".
	Reason string
}

func (e *LegacyWorkspaceError) Error() string {
	return fmt.Sprintf("%s detected; explicit migration is required before this bd version can open or modify the workspace. Preserve .beads unchanged and follow docs/getting-started/upgrading.md#cross-era-upgrades", e.Reason)
}

// historicalSQLiteReason is the Reason of a historical SQLite refusal.
const historicalSQLiteReason = "historical SQLite workspace"

// CheckHistoricalSQLiteWorkspace refuses beadsDir, whose metadata is cfg (nil
// when it has none), with a *LegacyWorkspaceError when it is a historical
// SQLite workspace (IsHistoricalSQLiteWorkspace). It reads, and never writes.
// Command admission and LoadDiscoveredForOpen both refuse through it, so the
// workspace bd runs in and every workspace it reaches from there are refused
// with one text.
func CheckHistoricalSQLiteWorkspace(beadsDir string, cfg *Config) error {
	if IsHistoricalSQLiteWorkspace(beadsDir, cfg) {
		return &LegacyWorkspaceError{Reason: historicalSQLiteReason}
	}
	return nil
}

// IsHistoricalSQLiteWorkspace reports whether beadsDir, whose metadata is cfg
// (nil when it has none), is a historical SQLite workspace: a .beads directory
// a pre-Dolt bd left behind, whose SQLite database is still the only copy of
// its data. It holds no embedded Dolt repository, and either its metadata
// names a bare on-disk SQLite database and no Dolt configuration, or it has no
// metadata and its only database is a SQLite file. Such metadata usually
// predates the backend key, and Config.GetBackend reads an empty backend as
// Dolt.
//
// Command admission refuses one (CheckHistoricalSQLiteWorkspace), and bd
// doctor's Classic Artifacts check and cleanup (cmd/bd/doctor/fix.PlanArtifacts)
// leave its SQLite files alone. All of them ask this function, so they
// recognize the same layouts; the cleanup additionally deletes no SQLite
// sidecar outside a redirect stub without positive Dolt evidence, so a layout
// this classifier does not recognize is left alone too.
func IsHistoricalSQLiteWorkspace(beadsDir string, cfg *Config) bool {
	if beadsDir == "" {
		return false
	}
	if HasEmbeddedDoltRepository(beadsDir) {
		return false
	}
	if cfg != nil {
		return configuredHistoricalSQLiteWorkspace(beadsDir, cfg)
	}
	return discoveredHistoricalSQLiteWorkspace(beadsDir)
}

// HasEmbeddedDoltRepository reports whether beadsDir contains an embedded Dolt
// repository. It owns the embedded adapter's coarse .dolt marker probe: the
// marker must be a non-symlink directory with at least one entry, but entry
// names and private repository files are never interpreted or opened. It lives
// here, beside the metadata, so that the historical SQLite classifier can use
// it without importing the storage adapter; embeddeddolt.HasRepository
// forwards to it.
func HasEmbeddedDoltRepository(beadsDir string) bool {
	return len(EmbeddedDoltDatabases(beadsDir)) > 0
}

// EmbeddedDoltDatabases names, sorted, the databases of beadsDir's embedded
// Dolt repository: the non-symlink directories under embeddeddolt that pass
// HasEmbeddedDoltRepository's .dolt marker probe. It is empty when beadsDir
// holds none. embeddeddolt.Databases forwards to it.
func EmbeddedDoltDatabases(beadsDir string) []string {
	root := filepath.Join(beadsDir, "embeddeddolt")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var databases []string
	for _, database := range entries {
		if !database.IsDir() || database.Type()&os.ModeSymlink != 0 {
			continue
		}
		marker, err := os.Lstat(filepath.Join(root, database.Name(), ".dolt"))
		if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
			continue
		}
		markerEntries, err := os.ReadDir(filepath.Join(root, database.Name(), ".dolt"))
		if err == nil && len(markerEntries) > 0 {
			databases = append(databases, database.Name())
		}
	}
	return databases
}

// configuredHistoricalSQLiteWorkspace classifies a metadata-backed workspace
// whose config names a bare on-disk SQLite database. It selects the database
// name from the config exactly as legacy bd did and confirms the file exists
// beneath beadsDir; a non-SQLite backend, any Dolt configuration, or a database
// path that is not a plain basename all disqualify it.
func configuredHistoricalSQLiteWorkspace(beadsDir string, cfg *Config) bool {
	if cfg.Backend != "" && cfg.Backend != BackendSQLite {
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
