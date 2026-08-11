package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/utils"
)

// guardLegacyUpgradeWorkspace rejects the reviewed pre-Dolt and legacy-Dolt
// workspace shapes before command setup can track a version, migrate metadata,
// or construct a store. It intentionally classifies only metadata, regular
// SQLite files, and the bounded local version witness; storage internals stay
// behind the driver boundary.
func guardLegacyUpgradeWorkspace(beadsDir string) error {
	if beadsDir == "" {
		return nil
	}
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if isHistoricalSQLiteWorkspace(beadsDir, cfg) {
		return legacyUpgradeRefusal("historical SQLite workspace")
	}
	// Validate read-only discovery metadata before any caller can use Load,
	// which migrates legacy config.json to metadata.json. Removed and unknown
	// backends must fail without changing the only pointer to their data.
	if err := validateConfiguredBackend(cfg); err != nil {
		return err
	}
	serverMode := cfg != nil && strings.EqualFold(cfg.DoltMode, configfile.DoltModeServer)
	if embeddeddolt.HasRepository(beadsDir) && !serverMode {
		return nil
	}
	version, ok := legacyUpgradeVersionWitness(beadsDir)
	if serverMode && ok && legacyServerVersion(version) {
		return legacyUpgradeRefusal(fmt.Sprintf("legacy Dolt server workspace from bd %s", version))
	}
	hasLocalDoltRoot := hasLegacyDoltRoot(beadsDir)
	if !hasLocalDoltRoot {
		return nil
	}
	if serverMode {
		if currentVersionWitness(version) {
			return nil
		}
		return legacyUpgradeRefusal("legacy Dolt server workspace")
	}
	if cfg == nil || cfg.DoltMode == "" ||
		strings.EqualFold(cfg.DoltMode, configfile.DoltModeEmbedded) {
		if doltserver.IsSharedServerMode() && !(ok && legacyServerVersion(version)) {
			return nil
		}
		reason := "legacy Dolt workspace"
		if _, validVersion := legacyVersionMinor(version); ok && validVersion {
			reason = fmt.Sprintf("legacy Dolt workspace from bd %s", version)
		}
		return legacyUpgradeRefusal(reason)
	}
	return nil
}

func isHistoricalSQLiteWorkspace(beadsDir string, cfg *configfile.Config) bool {
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

// guardUndiscoveredLegacyWorkspace covers metadata-less SQLite workspaces such
// as v0.9.1's sole .beads/vc.db. Normal workspace discovery intentionally
// excludes vc.db, so this bounded fallback runs only after that discovery found
// no current workspace.
func guardUndiscoveredLegacyWorkspace() error {
	if explicit := os.Getenv("BEADS_DIR"); explicit != "" {
		beadsDir := beads.FollowRedirect(utils.CanonicalizePath(explicit))
		return guardLegacyUpgradeWorkspace(beadsDir)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	dir := utils.CanonicalizePath(cwd)
	boundary := utils.CanonicalizePath(git.GetRepoRoot())
	for {
		if err := guardLegacyUpgradeWorkspace(filepath.Join(dir, ".beads")); err != nil {
			return err
		}
		if boundary != "" && utils.PathsEqual(dir, boundary) {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func hasLegacyDoltRoot(beadsDir string) bool {
	info, err := os.Lstat(filepath.Join(beadsDir, "dolt"))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
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

func legacyUpgradeVersionWitness(beadsDir string) (string, bool) {
	path := filepath.Join(beadsDir, localVersionFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64 {
		return "", false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- bounded file beneath selected workspace
	if err != nil || int64(len(data)) != info.Size() {
		return "", false
	}
	version := strings.TrimSpace(string(data))
	return version, version != ""
}

func legacyServerVersion(version string) bool {
	minor, ok := legacyVersionMinor(version)
	return ok && minor >= 55 && minor <= 62
}

// currentVersionWitness identifies a version marker only a post-1.0 binary
// could have written: an x.y.z with major >= 1 (pre-release and build-metadata
// suffixes tolerated, see versionCore), or a Homebrew --HEAD stamp. A local
// Dolt root in server mode is ambiguous without it, so that shape must be
// refused rather than opened as a historical schema.
func currentVersionWitness(version string) bool {
	if isBrewHeadVersion(version) {
		return true
	}
	parts := strings.Split(versionCore(version), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if value, err := strconv.Atoi(part); err != nil || value < 0 {
			return false
		}
	}
	major, _ := strconv.Atoi(parts[0])
	return major >= 1
}

// versionCore reduces a version stamp to its bare x.y.z core: it drops the
// optional leading "v" and any pre-release or build-metadata suffix. Every
// classifier in this file shares it so that a suffixed stamp cannot land
// outside both the current-era and legacy-era buckets, which on the
// shared-server path would admit a legacy workspace.
func versionCore(version string) string {
	core := strings.TrimPrefix(version, "v")
	if cut := strings.IndexAny(core, "-+"); cut >= 0 {
		core = core[:cut]
	}
	return core
}

// isBrewHeadVersion recognizes the version Homebrew stamps into --HEAD
// installs of the core beads formula: HEAD-<shortsha>, a bare HEAD when the
// head spec resolves no commit, and either shape carrying Homebrew's
// _<revision> suffix.
//
// No legacy-era channel could produce this shape, so the witness is
// attributable to a current-era binary despite not parsing as semver: the
// archived legacy tap formula was GoReleaser-generated with no head spec at
// all (it only unpacked a prebuilt tarball), the Makefile stamps main.Build
// and never main.Version, and the release workflow passes an x.y.z to
// -X main.Version.
func isBrewHeadVersion(version string) bool {
	rest, ok := strings.CutPrefix(version, "HEAD")
	if !ok {
		return false
	}
	// Homebrew's pkg_version appends _<revision> once the formula carries a
	// revision, so a revision bump must not read as a legacy stamp.
	if cut := strings.IndexByte(rest, '_'); cut >= 0 {
		if revision, err := strconv.Atoi(rest[cut+1:]); err != nil || revision < 0 {
			return false
		}
		rest = rest[:cut]
	}
	// Homebrew leaves the version as a bare "HEAD" whenever the head spec
	// cannot resolve a commit.
	if rest == "" {
		return true
	}
	sha, ok := strings.CutPrefix(rest, "-")
	if !ok {
		return false
	}
	// Bound the tail to a plausible git abbreviation so that arbitrary hex
	// cannot stand in for a commit; git never abbreviates below 7 characters.
	return len(sha) >= 7 && len(sha) <= 40 && isHexObjectID(sha, len(sha))
}

func legacyVersionMinor(version string) (int, bool) {
	parts := strings.Split(versionCore(version), ".")
	if len(parts) != 3 || parts[0] != "0" {
		return 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	if _, err := strconv.Atoi(parts[2]); err != nil {
		return 0, false
	}
	return minor, true
}

func legacyUpgradeRefusal(reason string) error {
	return legacyUpgradeRefusalError{reason: reason}
}

type legacyUpgradeRefusalError struct{ reason string }

func (err legacyUpgradeRefusalError) Error() string {
	return fmt.Sprintf("%s detected; explicit migration is required before this bd version can open or modify the workspace. Preserve .beads unchanged and follow docs/getting-started/upgrading.md#cross-era-upgrades", err.reason)
}

func isLegacyUpgradeRefusal(err error) bool {
	_, ok := err.(legacyUpgradeRefusalError)
	return ok
}
