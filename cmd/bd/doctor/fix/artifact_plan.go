package fix

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Artifact finding types, shared by the Classic Artifacts scan
// (doctor.ScanForArtifacts) and its cleanup (ClassicArtifacts).
const (
	ArtifactSQLite = "sqlite"
	ArtifactCruft  = "cruft-beads"
)

// PlannedArtifact is one thing the Classic Artifacts scan reports. The cleanup
// removes exactly the paths in Remove of each one marked SafeDelete, and
// nothing else, so the two halves cannot disagree.
type PlannedArtifact struct {
	Path        string // the file, or the .beads directory of a cruft finding about the whole directory
	Type        string // ArtifactSQLite or ArtifactCruft
	Description string
	SafeDelete  bool
	Remove      []string // what the cleanup removes; empty unless SafeDelete
}

// beadsDirOwner is what doctor can positively establish about a .beads
// directory before it deletes anything in it.
type beadsDirOwner int

const (
	// ownerUndetermined: nothing positively identifies the directory's
	// backend (no metadata and no Dolt store, or an ambiguous layout).
	ownerUndetermined beadsDirOwner = iota
	// ownerUnsupportedFormat: a format this build cannot open; the
	// directory is neither reported nor touched.
	ownerUnsupportedFormat
	// ownerUnreadable: its metadata exists but does not load, or the
	// directory cannot be listed.
	ownerUnreadable
	// ownerRedirectStub: a location that should hold only a redirect, and
	// the directory holds no database state, and no Dolt metadata without a
	// redirect beside it.
	ownerRedirectStub
	// ownerOtherBackend: its metadata names a backend other than Dolt, or
	// it is a historical SQLite workspace (configfile.IsHistoricalSQLiteWorkspace).
	ownerOtherBackend
	// ownerDolt: positive Dolt evidence (hasDoltEvidence).
	ownerDolt
)

type beadsDirClass struct {
	owner            beadsDirOwner
	redirectExpected bool
	databaseState    []string // "name (why)" for each entry that is, or may be, database state, sorted
	reason           string   // why the owner is not Dolt, for review findings
}

// classifyBeadsDir is the one positive-ownership predicate behind every
// Classic Artifacts deletion. Only two answers let the cleanup delete
// anything: ownerDolt (orphaned SQLite sidecars) and ownerRedirectStub (bd's
// runtime leftovers beside, or instead of, a redirect). Every other answer
// leaves the directory byte-identical.
func classifyBeadsDir(beadsDir string) beadsDirClass {
	cfg, err := configfile.LoadAndCheckWorkspaceFormat(beadsDir)
	var formatErr *configfile.UnsupportedWorkspaceFormatError
	if errors.As(err, &formatErr) {
		return beadsDirClass{owner: ownerUnsupportedFormat}
	}
	c := beadsDirClass{redirectExpected: isRedirectExpectedLocation(beadsDir)}
	entries, listErr := os.ReadDir(beadsDir)
	// Only a stub candidate needs every entry inspected; elsewhere the
	// cleanup touches nothing but orphaned SQLite sidecars.
	if c.redirectExpected && listErr == nil {
		c.databaseState = databaseStateEntries(beadsDir, entries)
	}
	switch {
	case err != nil:
		c.owner = ownerUnreadable
		c.reason = filepath.Base(configfile.MetadataFilePath(beadsDir)) + " does not load"
	case listErr != nil:
		c.owner = ownerUnreadable
		c.reason = "the directory cannot be listed"
	// Positive evidence of another backend outranks the path heuristic: a
	// workspace on SQLite, http, postgres or a backend this build does not
	// know, or a historical SQLite workspace whose database is not named
	// *.db, is not a stub even at a redirect-only path.
	case cfg != nil && (!configfile.IsSupportedBackend(cfg.Backend) || cfg.GetBackend() != configfile.BackendDolt):
		c.owner = ownerOtherBackend
		c.reason = "its metadata names another backend"
	case configfile.IsHistoricalSQLiteWorkspace(beadsDir, cfg):
		c.owner = ownerOtherBackend
		c.reason = "it is a historical SQLite workspace"
	case c.redirectExpected && len(c.databaseState) == 0 && namesLocalDoltStore(beadsDir, cfg):
		c.owner = ownerDolt
		c.reason = "its metadata points at a local Dolt store (dolt_data_dir)"
	// Without a redirect, metadata that configures Dolt is a workspace's own
	// record of the database it opens; a server-mode workspace keeps no
	// local store. Beside a redirect the stub rule keeps that metadata
	// (#4692), so a stub with a redirect is still cleaned.
	case c.redirectExpected && len(c.databaseState) == 0 && !holdsRedirect(entries) && metadataNamesDolt(cfg):
		c.owner = ownerDolt
		c.reason = "its metadata configures Dolt and it has no redirect"
	case c.redirectExpected && len(c.databaseState) == 0:
		c.owner = ownerRedirectStub
	case hasDoltEvidence(beadsDir, cfg):
		c.owner = ownerDolt
	default:
		c.owner = ownerUndetermined
		c.reason = "nothing identifies its backend"
	}
	return c
}

// hasDoltEvidence reports positive evidence that beadsDir is on Dolt: its
// metadata configures Dolt (metadataNamesDolt), or the directory holds an
// embedded Dolt repository or its Dolt store directory, beadsDir/dolt.
// BEADS_DOLT_DATA_DIR configures the workspace bd runs in, not every .beads
// the walk reaches, so it is not evidence about them.
func hasDoltEvidence(beadsDir string, cfg *configfile.Config) bool {
	if metadataNamesDolt(cfg) || embeddeddolt.HasRepository(beadsDir) {
		return true
	}
	info, err := os.Stat(filepath.Join(beadsDir, "dolt"))
	return err == nil && info.IsDir()
}

// metadataNamesDolt reports whether cfg, a .beads directory's metadata (nil
// when it has none), configures Dolt: it says backend dolt, or sets any of
// its Dolt settings. Each of those configures only Dolt, so even one alone
// is a workspace's record of the Dolt database it opens: a lone
// dolt_server_host selects server mode (GH#3545), and a dolt_data_dir names
// the store whether or not it exists.
func metadataNamesDolt(cfg *configfile.Config) bool {
	if cfg == nil {
		return false
	}
	for _, setting := range []string{cfg.DoltMode, cfg.DoltDatabase, cfg.DoltServerHost, cfg.DoltServerSocket, cfg.DoltServerUser, cfg.DoltDataDir, cfg.GlobalDoltDatabase} {
		if setting != "" {
			return true
		}
	}
	return cfg.Backend == configfile.BackendDolt || cfg.DoltServerPort != 0 || cfg.DoltRemotesAPIPort != 0 || cfg.DoltServerTLS || cfg.DoltServerAllowCleartextPassword || cfg.DoltTeamServer
}

// holdsRedirect reports whether entries, those of a .beads directory, include
// its redirect file.
func holdsRedirect(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() == "redirect" {
			return true
		}
	}
	return false
}

// namesLocalDoltStore reports whether beadsDir's own metadata names a
// dolt_data_dir that exists: the metadata is then the pointer to a database,
// not a stub's scaffolding.
func namesLocalDoltStore(beadsDir string, cfg *configfile.Config) bool {
	if cfg == nil || cfg.DoltDataDir == "" {
		return false
	}
	store := cfg.DoltDataDir
	if !filepath.IsAbs(store) {
		store = filepath.Join(beadsDir, store)
	}
	_, err := os.Stat(store)
	return err == nil
}

// databaseDirNames name directories that hold a database or a backup of one:
// a Dolt server's data directory, an embedded Dolt repository, bd backup's
// JSONL exports, and a Dolt repository itself.
var databaseDirNames = map[string]bool{"dolt": true, "embeddeddolt": true, "backup": true, ".dolt": true}

// databaseFileSuffixes name SQLite databases and Dolt archive table files.
var databaseFileSuffixes = []string{".db", ".db3", ".sqlite", ".sqlite3", ".darc"}

// sqliteSidecarSuffixes are the files SQLite keeps beside a database.
var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// doltManifestPattern matches the start of a Dolt (NBS) chunk-store manifest,
// "<storage version>:<format>:", e.g. "5:__DOLT__:". A Dolt database in its
// raw chunk-store layout, as a file:// backup or remote holds it, has one at
// its top level and no .dolt directory.
var doltManifestPattern = regexp.MustCompile(`^[0-9]+:(__[A-Z0-9_]+__|[0-9]+\.[0-9]+):`)

// maxDatabaseScanEntries bounds the entries isDatabaseState inspects below
// one path; a path holding more than this counts as database state.
const maxDatabaseScanEntries = 10000

// maxDatabaseScanLinks bounds how many symlinks deep isDatabaseState follows;
// a deeper chain counts as database state.
const maxDatabaseScanLinks = 4

// isDatabaseState reports whether path, an entry of a .beads directory, is
// database state no doctor cleanup removes, and names what it is:
//   - a SQLite database: *.db, *.db3, *.sqlite or *.sqlite3 (a backup
//     included), or any regular file starting with the SQLite header,
//     whatever its name;
//   - an entry named for the WAL, SHM or journal of a database that still
//     exists beside it, whatever the entry is: SQLite opens a sidecar by
//     that name, so a link there is the database's sidecar wherever it
//     points;
//   - a Dolt store: a dolt/, embeddeddolt/, backup/ or .dolt directory, or a
//     chunk store in its raw layout (a manifest file starting
//     "<n>:__DOLT__:", *.darc archives, journal.idx, or 32-character NBS
//     table and journal files), which is how a file:// Dolt backup or remote
//     holds a whole database;
//   - a directory holding any of those at any depth, or a symlink to one.
//
// Symlinks are followed to classify, never to delete: a link to a database
// keeps the link. A sidecar whose database is gone is classified like any
// other entry, so an orphaned WAL file is not database state; a
// sidecar-named entry below the top level is. Names match case-insensitively.
// What cannot be inspected counts as state.
func isDatabaseState(path string) (bool, string) {
	in := &stateInspector{}
	return in.entry(path, 0, true)
}

// stateInspector carries the entry budget across one isDatabaseState call.
type stateInspector struct{ seen int }

func (in *stateInspector) entry(path string, links int, top bool) (bool, string) {
	name := filepath.Base(path)
	lower := strings.ToLower(name)
	if isDatabaseFileName(lower) {
		return true, "a database file"
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, ""
		}
		return true, "it cannot be inspected"
	}
	// Decided by the entry's own name and directory, before a link is
	// followed: SQLite resolves a sidecar beside the database, not beside
	// whatever a link there points to.
	if top {
		if db, ok := sidecarOfPresentDatabase(path); ok {
			return true, "part of " + db
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if databaseDirNames[lower] {
			return true, "a database directory"
		}
		return in.symlink(path, links)
	}
	if info.Mode().IsRegular() {
		return regularFileState(path)
	}
	if !info.IsDir() {
		return false, ""
	}
	if databaseDirNames[lower] {
		return true, "a database directory"
	}
	return in.dir(path, links)
}

// symlink classifies what the link at path points to. A dangling link guards
// nothing.
func (in *stateInspector) symlink(path string, links int) (bool, string) {
	if links >= maxDatabaseScanLinks {
		return true, "it is linked too deeply to inspect"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, ""
		}
		return true, "it cannot be inspected"
	}
	if state, reason := in.entry(resolved, links+1, false); state {
		return true, "it links to " + resolved + ": " + reason
	}
	return false, ""
}

// dir reports whether anything below dir is database state.
func (in *stateInspector) dir(dir string, links int) (bool, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, "it cannot be inspected"
	}
	for _, e := range entries {
		if in.seen++; in.seen > maxDatabaseScanEntries {
			return true, "it is too large to inspect"
		}
		if isSidecarName(strings.ToLower(e.Name())) {
			return true, "it holds " + e.Name()
		}
		if state, reason := in.entry(filepath.Join(dir, e.Name()), links, false); state {
			switch {
			case strings.HasPrefix(reason, "it holds "):
				return true, "it holds " + e.Name() + "/" + strings.TrimPrefix(reason, "it holds ")
			case reason == "a database file" || reason == "a database directory":
				return true, "it holds " + e.Name()
			}
			return true, "it holds " + e.Name() + " (" + reason + ")"
		}
	}
	return false, ""
}

// regularFileState classifies a regular file by its content.
func regularFileState(path string) (bool, string) {
	kind, err := databaseFileContent(path)
	if err != nil {
		return true, "it cannot be inspected"
	}
	return kind != "", kind
}

// sidecarOfPresentDatabase reports whether path is named for the WAL, SHM or
// journal of a database whose entry is beside path, and names that database.
// A database entry that cannot be checked counts as there.
func sidecarOfPresentDatabase(path string) (string, bool) {
	name := filepath.Base(path)
	for _, suffix := range sqliteSidecarSuffixes {
		// The suffixes are ASCII, so cutting them from the original name by
		// byte length is exact; lower-casing can change a name's byte
		// length, so the lower-cased name is never used to slice this one.
		if len(name) <= len(suffix) || !strings.EqualFold(name[len(name)-len(suffix):], suffix) {
			continue
		}
		db := name[:len(name)-len(suffix)]
		if !hasDatabaseFileSuffix(strings.ToLower(db)) {
			continue
		}
		_, err := os.Lstat(filepath.Join(filepath.Dir(path), db))
		return db, err == nil || !os.IsNotExist(err)
	}
	return "", false
}

// sqliteHeader is the first 16 bytes of every SQLite database file.
const sqliteHeader = "SQLite format 3\x00"

// databaseFileContent reads the start of the regular file at path and names
// the database it holds: "a SQLite database" for the SQLite header, "a Dolt
// database" for a Dolt chunk-store manifest, "" for neither. A file that
// cannot be opened or read returns the error.
func databaseFileContent(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- an entry of a .beads directory doctor is classifying
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 64)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	buf = buf[:n]
	switch {
	case strings.HasPrefix(string(buf), sqliteHeader):
		return "a SQLite database", nil
	case doltManifestPattern.Match(buf):
		return "a Dolt database", nil
	}
	return "", nil
}

func hasDatabaseFileSuffix(lower string) bool {
	for _, suffix := range databaseFileSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// isDatabaseFileName reports whether a lower-cased entry name alone marks
// database state: a SQLite or Dolt archive suffix, Dolt's journal.idx, or a
// 32-character NBS table or journal file name.
func isDatabaseFileName(lower string) bool {
	return hasDatabaseFileSuffix(lower) || lower == "journal.idx" || isNBSFileName(lower)
}

// isNBSFileName reports whether name is a Dolt NBS table file or journal: 32
// characters of Dolt's base32 alphabet (0-9, a-v).
func isNBSFileName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'v') {
			return false
		}
	}
	return true
}

// isSidecarName reports whether a lower-cased name is a SQLite sidecar's.
func isSidecarName(lower string) bool {
	for _, suffix := range sqliteSidecarSuffixes {
		if db := strings.TrimSuffix(lower, suffix); db != lower && hasDatabaseFileSuffix(db) {
			return true
		}
	}
	return false
}

// databaseStateEntries lists the entries of beadsDir that isDatabaseState
// keeps, each as "name (why)": a database found, or an entry that could not
// be inspected and so counts as state.
func databaseStateEntries(beadsDir string, entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if state, reason := isDatabaseState(filepath.Join(beadsDir, entry.Name())); state {
			names = append(names, entry.Name()+" ("+reason+")")
		}
	}
	sort.Strings(names)
	return names
}

// PlanArtifacts is the one decision the Classic Artifacts scan reports and its
// cleanup carries out for the .beads directory beadsDir. skip is true for a
// directory in a format this build cannot open, which neither reports nor
// touches. Only a .beads positively on Dolt (orphaned SQLite sidecars) and a
// redirect-only stub holding no database state (bd's runtime leftovers in
// it) yield findings marked SafeDelete. Everything else is left
// byte-identical; what is reported for manual review is a database file or
// sidecar outside another backend's .beads, what a stub holds beyond its
// runtime leftovers, and a redirect-only .beads that is not a stub but holds
// more than a clean stub does.
func PlanArtifacts(beadsDir string) (findings []PlannedArtifact, skip bool) {
	c := classifyBeadsDir(beadsDir)
	if c.owner == ownerUnsupportedFormat {
		return nil, true
	}
	if c.owner == ownerRedirectStub {
		return stubCruftFindings(beadsDir), false
	}
	if c.owner != ownerOtherBackend {
		findings = append(findings, sqliteFindings(beadsDir, c)...)
	}
	// A redirect-only .beads that is not a stub is reported for review only
	// when it holds more than a clean stub does (or database state). A clean
	// stub, holding just its redirect, .gitkeep and the metadata kept beside
	// a redirect, is not reported whatever that metadata says, as on main
	// (#4692).
	if c.redirectExpected && (len(c.databaseState) > 0 || holdsStubCruft(beadsDir)) {
		desc := "should be redirect-only but " + c.reason + "; review manually"
		if len(c.databaseState) > 0 {
			desc = fmt.Sprintf("should be redirect-only but cannot be cleaned safely: %s; review manually", summarizeNames(c.databaseState))
		}
		findings = append(findings, PlannedArtifact{Path: beadsDir, Type: ArtifactCruft, Description: desc})
	}
	return findings, false
}

// sqliteFindings reports beads.db, its WAL and SHM, and beads.backup-*.db. In
// a .beads positively on Dolt, a WAL or SHM whose beads.db is gone is safe to
// delete; a database, a backup, and the WAL and SHM of a database still there
// are never removed.
func sqliteFindings(beadsDir string, c beadsDirClass) []PlannedArtifact {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return nil
	}
	basis := "Dolt is active backend"
	if c.owner != ownerDolt {
		basis = "backend unknown: " + c.reason
	}
	var findings []PlannedArtifact
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(beadsDir, name)
		switch {
		case name == "beads.db":
			findings = append(findings, PlannedArtifact{Path: path, Type: ArtifactSQLite, Description: "SQLite database file (" + basis + ")"})
		case name == "beads.db-wal" || name == "beads.db-shm":
			if state, reason := isDatabaseState(path); state {
				findings = append(findings, PlannedArtifact{Path: path, Type: ArtifactSQLite, Description: "SQLite WAL/SHM kept: " + reason + " (" + basis + ")"})
				continue
			}
			f := PlannedArtifact{Path: path, Type: ArtifactSQLite, Description: "SQLite WAL/SHM without its database (" + basis + ")"}
			if c.owner == ownerDolt {
				f.SafeDelete = true
				f.Remove = []string{path}
			}
			findings = append(findings, f)
		case strings.HasPrefix(name, "beads.backup-") && strings.HasSuffix(name, ".db"):
			findings = append(findings, PlannedArtifact{Path: path, Type: ArtifactSQLite, Description: "pre-migration backup, a whole SQLite database (" + basis + ")"})
		}
	}
	return findings
}

// stubRuntimeLeftovers are the only entries the stub rule removes from a
// redirect-only stub: per-machine runtime files bd writes in a .beads it
// opens directly (bdStateFiles) or that bd's former daemon kept there
// (daemon.lock, daemon.log, daemon.pid and bd.sock). bd's .beads/.gitignore
// template ignores each of them, and they are what the orchestrator that
// writes a stub's redirect clears from it. No bd writes the daemon's files
// any more, so one in a stub is always left over; bdStateFiles are left over
// only where bd does not open the stub (stubStateIsLeftover). Anything else
// in a stub, such as files the repository tracks there (config.yaml,
// README.md, .gitignore, formulas/), metadata without a redirect beside it,
// the audit log or routes.jsonl, is kept and reported for review.
var stubRuntimeLeftovers = map[string]bool{
	".local_version": true,
	"last-touched":   true,
	"daemon.lock":    true,
	"daemon.log":     true,
	"daemon.pid":     true,
	"bd.sock":        true,
}

// bdStateFiles are the stubRuntimeLeftovers bd writes in the .beads it opens
// (beads.FindBeadsDir, which follows a redirect): its .local_version witness
// and its last-touched issue.
var bdStateFiles = map[string]bool{".local_version": true, "last-touched": true}

// stubStateIsLeftover reports whether the bdStateFiles in beadsDir, a
// redirect-only stub listing entries, are left over: only while bd does not
// open the stub. That takes a redirect bd follows (redirectFollowed), since
// without one, or beside one bd ignores, bd opens the stub and writes them
// there; and no configuration of the stub's own (config.yaml, metadata.json
// or config.json), which makes it a workspace bd opens itself once that
// redirect stops resolving. In a workspace bd opens, .local_version is the
// witness the legacy-upgrade guard reads to refuse a pre-1.0 Dolt server
// workspace, so deleting it would lift that refusal.
func stubStateIsLeftover(beadsDir string, entries []os.DirEntry) bool {
	for _, entry := range entries {
		switch entry.Name() {
		case "config.yaml", configfile.ConfigFileName, configfile.LegacyConfigFileName:
			return false
		}
	}
	return redirectFollowed(beadsDir)
}

// isStubRuntimeLeftover reports whether entry, an entry of a redirect-only
// stub, is one of stubRuntimeLeftovers as bd writes it: a regular file, or
// for bd.sock the daemon's socket. A directory or symlink by one of those
// names is not bd's.
func isStubRuntimeLeftover(entry os.DirEntry) bool {
	if !stubRuntimeLeftovers[entry.Name()] {
		return false
	}
	return entry.Type().IsRegular() || (entry.Name() == "bd.sock" && entry.Type()&os.ModeSocket != 0)
}

// stubCruftFindings reports what a redirect-only stub holds beyond its
// redirect file, .gitkeep and, beside a redirect, its own metadata:
// metadata.json, or a legacy config.json, which this walk does not migrate
// (gastownhall/beads#4692: a co-located redirect and metadata.json is a
// supported topology). Only its runtime leftovers (stubRuntimeLeftovers) are
// safe to delete, and only because the stub holds no database state, which
// classifyBeadsDir established. A stub holding nothing else is one finding
// for the directory, safe to delete as before. A stub that keeps anything
// else is a finding for review, and each runtime leftover in it is a finding
// of its own, safe to delete, as an orphaned WAL is: a finding marked
// SafeDelete never names a directory holding entries that must stay.
func stubCruftFindings(beadsDir string) []PlannedArtifact {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return nil
	}
	hasRedirect := holdsRedirect(entries)
	stateLeftOver := stubStateIsLeftover(beadsDir, entries)
	var leftovers, kept []string
	for _, entry := range entries {
		name := entry.Name()
		if name == "redirect" || name == ".gitkeep" {
			continue
		}
		if hasRedirect && (name == configfile.ConfigFileName || name == configfile.LegacyConfigFileName) {
			continue
		}
		if isStubRuntimeLeftover(entry) && (stateLeftOver || !bdStateFiles[name]) {
			leftovers = append(leftovers, name)
			continue
		}
		kept = append(kept, name)
	}
	if len(kept) == 0 {
		if len(leftovers) == 0 {
			return nil
		}
		remove := make([]string, 0, len(leftovers))
		for _, name := range leftovers {
			remove = append(remove, filepath.Join(beadsDir, name))
		}
		return []PlannedArtifact{{Path: beadsDir, Type: ArtifactCruft, Description: "should be redirect-only but contains: " + strings.Join(leftovers, ", "), SafeDelete: true, Remove: remove}}
	}
	findings := []PlannedArtifact{{Path: beadsDir, Type: ArtifactCruft, Description: "should be redirect-only but contains files the stub cleanup does not remove: " + summarizeNames(kept) + "; review manually"}}
	for _, name := range leftovers {
		path := filepath.Join(beadsDir, name)
		findings = append(findings, PlannedArtifact{Path: path, Type: ArtifactCruft, Description: "bd runtime leftover in a redirect-only .beads", SafeDelete: true, Remove: []string{path}})
	}
	return findings
}

// holdsStubCruft reports whether beadsDir holds more than a clean stub does:
// an entry beyond its redirect, .gitkeep and the metadata kept beside a
// redirect, which stubCruftFindings reports.
func holdsStubCruft(beadsDir string) bool {
	return len(stubCruftFindings(beadsDir)) > 0
}

func summarizeNames(names []string) string {
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + fmt.Sprintf(" and %d more", len(names)-5)
	}
	return strings.Join(names, ", ")
}

// RefusedError reports a removal RemoveDisposable refused.
type RefusedError struct {
	Path   string
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("refusing to remove %s: %s", e.Path, e.Reason)
}

// RemoveDisposable removes path, which a doctor cleanup identified as
// disposable. Every Classic Artifacts and Legacy MQ deletion goes through
// it, and it refuses database state (isDatabaseState) whatever its caller
// planned, returning a *RefusedError. It never follows a symlink; a path
// already gone is not an error.
func RemoveDisposable(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if state, reason := isDatabaseState(path); state {
		return &RefusedError{Path: path, Reason: reason}
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// WalkBeadsDirs calls visit for every .beads directory under root, the same
// set for the Classic Artifacts scan and its cleanup: it skips dependency
// trees (node_modules, vendor, __pycache__), enters .git only at
// .git/beads-worktrees, does not descend into a .beads directory, and skips
// what it cannot read.
func WalkBeadsDirs(root string, visit func(beadsDir string)) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		switch filepath.Base(path) {
		case ".git":
			walkGitWorktreeBeadsDirs(path, visit)
			return filepath.SkipDir
		case "node_modules", "vendor", "__pycache__":
			return filepath.SkipDir
		case ".beads":
			visit(path)
			return filepath.SkipDir
		}
		return nil
	})
}

// walkGitWorktreeBeadsDirs visits the .beads directories of .git's
// beads-worktrees area only, rather than traversing all of .git.
func walkGitWorktreeBeadsDirs(gitDir string, visit func(beadsDir string)) {
	worktreesDir := filepath.Join(gitDir, "beads-worktrees")
	if info, err := os.Stat(worktreesDir); err != nil || !info.IsDir() {
		return
	}
	_ = filepath.Walk(worktreesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || filepath.Base(path) != ".beads" {
			return nil
		}
		visit(path)
		return filepath.SkipDir
	})
}
