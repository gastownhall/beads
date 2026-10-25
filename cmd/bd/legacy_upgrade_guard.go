package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/ceiling"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/historicalsqlite"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/mod/semver"
)

// guardLegacyUpgradeWorkspace rejects the reviewed pre-Dolt and legacy-Dolt
// workspace shapes, and workspaces whose metadata declares a format this build
// cannot open (checkWorkspaceFormat), before command setup can track a version,
// migrate metadata, or construct a store. It intentionally classifies only
// metadata, regular SQLite files, and the bounded local version witness;
// storage internals stay behind the driver boundary. Render its refusals with
// handleWorkspaceGuardError.
func guardLegacyUpgradeWorkspace(beadsDir string) error {
	return guardLegacyUpgradeWorkspaceForInit(beadsDir, false)
}

// guardLegacyUpgradeWorkspaceForInit is guardLegacyUpgradeWorkspace for a caller
// that has already resolved server intent from a source the workspace does not
// yet record: `bd init --server`, whose flag is the only place that choice lives
// until init has written it. The guard cannot see the flag, so initSelectsServer
// carries it in. It is consulted for exactly one thing, the empty-root admission
// below, and is deliberately not folded into the workspace's own server mode:
// that would also move a non-empty root with an unreadable witness onto the
// warn-and-open arm, and an embedded repository beside a non-empty .beads/dolt
// onto the refusing one.
func guardLegacyUpgradeWorkspaceForInit(beadsDir string, initSelectsServer bool) error {
	if beadsDir == "" {
		return nil
	}
	// This guard admits only the ordinary Issue store. A workspace in another
	// format is refused before any legacy classification, which could
	// otherwise admit it, or report it as a legacy store to migrate.
	cfg, err := loadAndCheckWorkspaceFormat(beadsDir)
	if err != nil {
		return err
	}
	if historicalsqlite.IsWorkspace(beadsDir, cfg) {
		return legacyUpgradeRefusal("historical SQLite workspace")
	}
	// Validate read-only discovery metadata before any caller can use Load,
	// which migrates legacy config.json to metadata.json. Removed and unknown
	// backends must fail without changing the only pointer to their data.
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return err
	}
	serverMode := workspaceSelectsServerMode(cfg)
	if embeddeddolt.HasRepository(beadsDir) && !serverMode {
		return nil
	}
	version, present := legacyUpgradeVersionWitness(beadsDir)
	if serverMode && present && legacyServerVersion(version) {
		return legacyUpgradeRefusal(fmt.Sprintf("legacy Dolt server workspace from bd %s", version))
	}
	hasLocalDoltRoot := hasLegacyDoltRoot(beadsDir)
	if !hasLocalDoltRoot {
		return nil
	}
	// Positive evidence that this workspace belongs to the current era settles
	// it for every mode. Only the ambiguity that remains after these is
	// mode-specific, so the refusals below are the only part that branches.
	if classifyVersionWitness(version) == witnessEraCurrent {
		// A witness naming bd 1.0 or later can only have been written by a
		// current-era binary, and only after this same guard admitted the
		// workspace (PersistentPreRunE runs the guard before trackBdVersion,
		// and doctor runs it before its own copy of that call). No legacy
		// release could have produced it. Reading it as proof only in server
		// mode left the escape hatch unreachable for a workspace the running
		// binary demonstrably created itself.
		return nil
	}
	if doltserver.IsSharedServerMode() && !(present && legacyServerVersion(version)) {
		// A shared Dolt sql-server owns .beads/dolt, so there is no embedded
		// historical schema to protect — unless the witness affirmatively names
		// the 0.55–0.62 server era. This admission used to sit inside the
		// embedded arm below. Leaving it there while config.yaml started
		// selecting server mode would have moved shared-server workspaces bd
		// admits today onto the refusing arm: the mode changed, the evidence
		// did not.
		return nil
	}
	if initSelectsServer && !present && isEmptyDirectory(filepath.Join(beadsDir, "dolt")) {
		// Init has already chosen a server, so this is the empty root the
		// serverMode arm below admits for a workspace that records the choice: it
		// holds no Dolt data a legacy release could have left, and bd makes it
		// itself on use, so a retry after a first attempt that could not reach a
		// server finds exactly this. A present witness, a root holding any entry,
		// and every caller that does not pass the flag are decided as before.
		return nil
	}
	if serverMode {
		if !present && isEmptyDirectory(filepath.Join(beadsDir, "dolt")) {
			// An empty .beads/dolt holds no Dolt data, so no legacy release
			// could have left anything in it to protect: it carries no more
			// evidence than a server workspace without that directory, which
			// is admitted above. bd creates exactly this shape itself — the
			// local Dolt root is made on use even when the data lives on an
			// external server — so refusing it made `bd init --server
			// --external` fail over a provisioner's empty root, and made a
			// workspace that lost its gitignored witness unusable with no bd
			// command able to repair it. Admitting it lets the command re-seed
			// the witness. A present pre-1.0 witness was refused above, and a
			// root holding anything at all still refuses below.
			return nil
		}
		if present && classifyVersionWitness(version) == witnessEraUnknown {
			// A witness that is present but unreadable — including one left
			// blank or whitespace-only by an interrupted or disk-full
			// best-effort write — is not evidence of a legacy workspace: every
			// pre-1.0 bd wrote a plain X.Y.Z through the same writer, so no
			// legacy release could have produced a witness this parse rejects.
			// Report the ambiguity and open the workspace so the admitted
			// command can self-heal the witness, instead of refusing every
			// command over an advisory, gitignored file. A genuinely absent
			// witness is not present and still refuses below, preserving the
			// pre-1.0 guard invariant.
			warnUnreadableVersionWitness(beadsDir, version)
			return nil
		}
		return legacyUpgradeRefusal("legacy Dolt server workspace")
	}
	if cfg == nil || cfg.DoltMode == "" ||
		strings.EqualFold(cfg.DoltMode, configfile.DoltModeEmbedded) {
		reason := "legacy Dolt workspace"
		if _, validVersion := legacyVersionMinor(version); present && validVersion {
			reason = fmt.Sprintf("legacy Dolt workspace from bd %s", version)
		}
		return legacyUpgradeRefusal(reason)
	}
	return nil
}

// workspaceSelectsServerMode reports whether bd will open beadsDir through a
// Dolt sql-server rather than as an embedded database.
//
// It asks configfile's own resolver instead of reading metadata.json's
// dolt_mode directly. That field is only the highest-priority layer of the
// decision: a workspace that declares `dolt.mode: server` in .beads/config.yaml
// and has no metadata.json at all — the shape gc provisions — is just as much a
// server workspace, and IsDoltServerMode documents the full precedence chain
// that store selection then uses. Reading the top layer alone classified those
// as embedded, and an embedded workspace owning a .beads/dolt root is precisely
// what this guard refuses, so bd rejected server workspaces it had created
// itself moments earlier.
//
// On the PersistentPreRunE and doctor paths the config.yaml layer resolves
// against the SELECTED workspace: main.go binds the discovered target before
// admission for exactly this reason, which is also what makes
// doltserver.IsSharedServerMode() above read the target's config rather than
// the caller's. The ancestor scans are the known exception —
// guardUndiscoveredLegacyWorkspace below and findParentConfig in bootstrap.go
// pass candidates that are by construction not the bound target, so for those
// the env and config.yaml layers come from the caller's process-global state,
// not from the candidate. Do not rely on the binding from a caller that walks
// ancestors.
func workspaceSelectsServerMode(cfg *configfile.Config) bool {
	return normalizeLoadedConfig(cfg).IsDoltServerMode()
}

// guardUndiscoveredLegacyWorkspace covers metadata-less SQLite workspaces such
// as v0.9.1's sole .beads/vc.db. Normal workspace discovery intentionally
// excludes vc.db, so this bounded fallback runs only after that discovery found
// no current workspace.
func guardUndiscoveredLegacyWorkspace() error {
	for _, beadsDir := range undiscoveredWorkspaceCandidates() {
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			return err
		}
	}
	return nil
}

// checkUndiscoveredWorkspaceFormat refuses a workspace in a format this build
// cannot open (checkWorkspaceFormat) that discovery passed over, such as one
// marked only by a legacy config.json, and nothing else. It reads metadata
// without migrating it.
func checkUndiscoveredWorkspaceFormat() error {
	for _, beadsDir := range undiscoveredWorkspaceCandidates() {
		if err := checkDiscoveredWorkspaceFormat(beadsDir); err != nil {
			return err
		}
	}
	return nil
}

// undiscoveredWorkspaceCandidates lists, nearest first, the .beads directories
// the undiscovered-workspace checks read: the one BEADS_DIR names or, without
// it, each .beads from the working directory up to the repository root and
// below BEADS_CEILING_DIRECTORIES.
func undiscoveredWorkspaceCandidates() []string {
	if explicit := os.Getenv("BEADS_DIR"); explicit != "" {
		return []string{beads.FollowRedirect(utils.CanonicalizePath(explicit))}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	dir := utils.CanonicalizePath(cwd)
	boundary := utils.CanonicalizePath(git.GetRepoRoot())
	bound := ceiling.For(dir)
	var candidates []string
	for {
		if bound.Excludes(dir) {
			return candidates
		}
		candidates = append(candidates, filepath.Join(dir, ".beads"))
		if boundary != "" && utils.PathsEqual(dir, boundary) {
			return candidates
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return candidates
		}
		dir = parent
	}
}

func hasLegacyDoltRoot(beadsDir string) bool {
	info, err := os.Lstat(filepath.Join(beadsDir, "dolt"))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

// isEmptyDirectory reports whether path is a real (non-symlink) directory with
// no entries. Any error reading it answers false, so callers fail closed.
func isEmptyDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	dir, err := os.Open(path) // #nosec G304 -- bounded probe beneath selected workspace
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(1)
	return len(names) == 0 && err == io.EOF
}

// legacyUpgradeVersionWitness reads the advisory local version witness beneath
// beadsDir. The returned bool reports whether a witness file is present as a
// bounded regular file, independent of whether its trimmed contents name a
// version. A present but blank or whitespace-only witness — the shape an
// interrupted or disk-full best-effort writeLocalVersion leaves behind — is
// returned as ("", true) so guardLegacyUpgradeWorkspace can tell it apart from
// a genuinely absent witness, which is ("", false). Only a present witness is
// ever admitted as an unknown-era workspace; a missing, non-regular, or
// oversized witness stays absent so the pre-1.0 refusal cannot relax.
func legacyUpgradeVersionWitness(beadsDir string) (string, bool) {
	path := filepath.Join(beadsDir, localVersionFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64 {
		return "", false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- bounded file beneath selected workspace
	if err != nil || int64(len(data)) != info.Size() {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func legacyServerVersion(version string) bool {
	minor, ok := legacyVersionMinor(version)
	return ok && minor >= 55 && minor <= 62
}

// witnessEra is the release era a local version witness places a workspace in.
type witnessEra int

const (
	// witnessEraUnknown means the witness is missing or cannot be read as a
	// version. It is an absence of evidence, not evidence of a legacy layout.
	witnessEraUnknown witnessEra = iota
	// witnessEraLegacy means the witness names a pre-1.0 bd.
	witnessEraLegacy
	// witnessEraCurrent means the witness names bd 1.0 or later.
	witnessEraCurrent
)

// classifyVersionWitness places a local version witness in a release era.
//
// The witness is whatever string the bd that last touched the workspace held
// in main.Version, which release tooling injects verbatim: a plain release
// ("1.1.0"), a release candidate ("v1.2.0-rc.1"), a build carrying metadata,
// or a Go pseudo-version ("v1.1.1-0.20260805093327-bf97b73749ac"). All of
// those are valid semantic versions, so the era comes from a semver parse
// rather than from counting dots.
func classifyVersionWitness(version string) witnessEra {
	version = strings.TrimSpace(version)
	if version == "" {
		return witnessEraUnknown
	}
	if canonical := "v" + strings.TrimPrefix(version, "v"); semver.IsValid(canonical) {
		if semver.Major(canonical) == "v0" {
			return witnessEraLegacy
		}
		return witnessEraCurrent
	}
	// Strict semver rejects shapes a distributor could still stamp onto a
	// legacy build, such as a zero-padded or four-component version. Classify
	// those by their major component alone so the pre-1.0 guard never relaxes.
	if major, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "."); major != "" {
		if value, err := strconv.Atoi(major); err == nil && value == 0 {
			return witnessEraLegacy
		}
	}
	return witnessEraUnknown
}

// legacyVersionMinor returns the minor component of a pre-1.0 version witness.
func legacyVersionMinor(version string) (int, bool) {
	version = strings.TrimSpace(version)
	canonical := "v" + strings.TrimPrefix(version, "v")
	if !semver.IsValid(canonical) || semver.Major(canonical) != "v0" {
		return 0, false
	}
	minor, err := strconv.Atoi(strings.TrimPrefix(semver.MajorMinor(canonical), "v0."))
	if err != nil {
		return 0, false
	}
	return minor, true
}

// legacyUpgradeWarnWriter receives guard warnings; tests redirect it.
var legacyUpgradeWarnWriter io.Writer = os.Stderr

// warnUnreadableVersionWitness reports a workspace bd opened despite being
// unable to read its version witness, so an operator can still see that the
// era check was inconclusive.
func warnUnreadableVersionWitness(beadsDir, version string) {
	fmt.Fprintf(legacyUpgradeWarnWriter,
		"Warning: unreadable version witness %q in %s; assuming a current workspace. Run 'bd doctor' to reset version tracking.\n",
		version, filepath.Join(beadsDir, localVersionFile))
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
