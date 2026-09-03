package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/ui"
)

const migrateLockFileName = "migrate.lock"

const migrateJournalFileName = "dolt-mode-migration.json"

type migratePhase string

const (
	migratePrepared           migratePhase = "prepared"
	migrateTargetConfigured   migratePhase = "target_configured"
	migrateOldControlsRetired migratePhase = "old_controls_retired"
	migrateVerified           migratePhase = "verified"
	migrateCommitted          migratePhase = "committed"
)

type migrateJournal struct {
	Version    int                                 `json:"version"`
	SourceMode string                              `json:"source_mode"`
	TargetMode string                              `json:"target_mode"`
	Shared     bool                                `json:"shared"`
	RootPath   string                              `json:"root_path,omitempty"`
	External   *configfile.ExternalDoltConfig      `json:"external,omitempty"`
	Sidecar    *configfile.ProxiedServerClientInfo `json:"sidecar,omitempty"`
	LogAssets  []string                            `json:"log_assets,omitempty"`
	Ownership  string                              `json:"ownership"`
	Attempt    int                                 `json:"attempt"`
	Phase      migratePhase                        `json:"phase"`
}

func migrateJournalPath(beadsDir string) string {
	return filepath.Join(beadsDir, migrateJournalFileName)
}

func loadMigrateJournal(beadsDir string) (*migrateJournal, error) {
	b, err := os.ReadFile(migrateJournalPath(beadsDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", migrateJournalFileName, err)
	}
	var j migrateJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", migrateJournalFileName, err)
	}
	if j.Version != 1 || j.SourceMode == "" || j.TargetMode == "" || j.Phase == "" || j.Attempt < 1 {
		return nil, fmt.Errorf("invalid %s: missing required migration state", migrateJournalFileName)
	}
	switch j.Phase {
	case migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted:
	default:
		return nil, fmt.Errorf("invalid %s: unknown phase %q", migrateJournalFileName, j.Phase)
	}
	if j.RootPath == "" || !filepath.IsAbs(j.RootPath) || filepath.Clean(j.RootPath) != j.RootPath {
		return nil, fmt.Errorf("invalid %s: root_path must be a clean absolute path", migrateJournalFileName)
	}
	if j.SourceMode == configfile.DoltModeServer && j.TargetMode == configfile.DoltModeProxiedServer {
		if j.Shared { /* shared-server uses server mode plus shared topology */
		}
	} else if j.SourceMode == configfile.DoltModeProxiedServer && (j.TargetMode == configfile.DoltModeServer || j.TargetMode == "shared-server") {
		if j.TargetMode == "shared-server" && !j.Shared {
			return nil, fmt.Errorf("invalid %s: shared target requires shared topology", migrateJournalFileName)
		}
		if j.TargetMode == configfile.DoltModeServer && j.Shared {
			return nil, fmt.Errorf("invalid %s: server target cannot use shared topology", migrateJournalFileName)
		}
	} else {
		return nil, fmt.Errorf("invalid %s: unsupported mode transition %s -> %s", migrateJournalFileName, j.SourceMode, j.TargetMode)
	}
	if j.Sidecar == nil {
		return nil, fmt.Errorf("invalid %s: missing sidecar topology", migrateJournalFileName)
	}
	if j.Ownership != "managed-local" && j.Ownership != "external" {
		return nil, fmt.Errorf("invalid %s: unknown ownership %q", migrateJournalFileName, j.Ownership)
	}
	if j.Ownership == "external" && j.Sidecar.External == nil {
		return nil, fmt.Errorf("invalid %s: external ownership missing endpoint", migrateJournalFileName)
	}
	if j.Ownership == "managed-local" && j.Sidecar.External != nil {
		return nil, fmt.Errorf("invalid %s: managed-local ownership has external endpoint", migrateJournalFileName)
	}
	if j.Sidecar.External != nil {
		if err := j.Sidecar.External.Validate(); err != nil {
			return nil, fmt.Errorf("invalid %s external topology: %w", migrateJournalFileName, err)
		}
	}
	if rp := j.Sidecar.ResolvedRootPath(beadsDir); rp != "" && filepath.Clean(rp) != j.RootPath {
		return nil, fmt.Errorf("invalid %s: sidecar root does not match root_path", migrateJournalFileName)
	}
	return &j, nil
}

func saveMigrateJournal(beadsDir string, j *migrateJournal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(beadsDir, ".dolt-mode-migration-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", migrateJournalFileName, err)
	}
	if err = os.Rename(tmpName, migrateJournalPath(beadsDir)); err != nil {
		return err
	}
	d, err := os.Open(beadsDir) // #nosec G304 -- beadsDir is the discovered workspace directory
	if err != nil {
		return fmt.Errorf("syncing %s directory: %w", migrateJournalFileName, err)
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("syncing %s directory: %w", migrateJournalFileName, err)
	}
	if err = d.Close(); err != nil {
		return fmt.Errorf("closing %s directory: %w", migrateJournalFileName, err)
	}
	return nil
}

func removeMigrateJournal(beadsDir string) error {
	if err := os.Remove(migrateJournalPath(beadsDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	d, err := os.Open(beadsDir) // #nosec G304 -- beadsDir is the discovered workspace directory
	if err != nil {
		return fmt.Errorf("opening migration directory: %w", err)
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("syncing migration directory: %w", err)
	}
	if err = d.Close(); err != nil {
		return fmt.Errorf("closing migration directory: %w", err)
	}
	return nil
}

func migrateFault(phase migratePhase) error {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("BEADS_MIGRATION_FAIL_PHASE")), string(phase)) {
		return fmt.Errorf("migration fault injected after phase %s", phase)
	}
	return nil
}

func checkpointMigration(beadsDir string, j *migrateJournal, phase migratePhase) error {
	j.Phase = phase
	if err := saveMigrateJournal(beadsDir, j); err != nil {
		return err
	}
	return migrateFault(phase)
}

func validateMigrationJournalAgainstConfig(j *migrateJournal, cfg *configfile.Config) error {
	if j == nil || cfg == nil {
		return nil
	}
	if j.Phase == migratePrepared {
		if cfg.GetDoltMode() != j.SourceMode && cfg.GetDoltMode() != j.TargetMode && !(j.TargetMode == "shared-server" && cfg.IsDoltServerMode()) {
			return fmt.Errorf("migration journal phase %s disagrees with metadata mode %q", j.Phase, cfg.GetDoltMode())
		}
		return nil
	}
	if j.TargetMode == configfile.DoltModeProxiedServer && !cfg.IsDoltProxiedServerMode() {
		return fmt.Errorf("migration journal phase %s requires proxied-server metadata", j.Phase)
	}
	if j.TargetMode != configfile.DoltModeProxiedServer && !cfg.IsDoltServerMode() {
		return fmt.Errorf("migration journal phase %s requires server metadata", j.Phase)
	}
	return nil
}

func migrateToProxiedRunE(metricName, checkName string, shared bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent(metricName)
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if !dryRun {
			CheckReadonly(checkName)
		}

		idleTimeout, err := resolveMigrateIdleTimeout(cmd)
		if err != nil {
			return err
		}
		return runMigrateToProxiedServer(dryRun, idleTimeout, shared)
	}
}

func migrateFromProxiedRunE(metricName, checkName string, shared bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent(metricName)
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if !dryRun {
			CheckReadonly(checkName)
		}
		return runMigrateFromProxiedServer(dryRun, shared)
	}
}

var migrateToProxiedServerCmd = &cobra.Command{
	Use:           "from-server-to-proxied-server",
	Short:         "[EXPERIMENTAL] Switch a server-mode repo to proxied-server mode",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Switch a repo from server mode (bd init --server) to proxied-server mode.

Both modes root their dolt sql-server at the same .beads/dolt directory, so this
only rewrites .beads/metadata.json (dolt_mode) and writes the proxied-server
sidecar — no Dolt data is copied or moved. Stop the running server first with
'bd dolt stop'.

Note: dolt_mode lives in the committed metadata.json, so this change propagates
to clones on the next push.`,
	Args: cobra.NoArgs,
	RunE: migrateToProxiedRunE("migrate-to-proxied-server", "migrate from-server-to-proxied-server", false),
}

var migrateSharedToProxiedServerCmd = &cobra.Command{
	Use:           "from-shared-server-to-proxied-server",
	Short:         "[EXPERIMENTAL] Switch a shared-server repo to proxied-server mode",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Switch a repo from shared-server mode to proxied-server mode.

The proxied server is rooted at the shared dolt directory
(~/.beads/shared-server/dolt), so no Dolt data is copied or moved; this rewrites
.beads/metadata.json (dolt_mode), turns off dolt.shared-server for this repo, and
writes the proxied-server sidecar. Stop the running shared server first with
'bd dolt stop' — note that stops it for every project sharing it.`,
	Args: cobra.NoArgs,
	RunE: migrateToProxiedRunE("migrate-shared-to-proxied-server", "migrate from-shared-server-to-proxied-server", true),
}

var migrateToServerCmd = &cobra.Command{
	Use:           "from-proxied-server-to-server",
	Short:         "[EXPERIMENTAL] Switch a proxied-server repo to server mode",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Switch a repo from proxied-server mode to server mode (bd init --server).

Both modes root their dolt sql-server at the same .beads/dolt directory, so this
only rewrites .beads/metadata.json (dolt_mode) and removes the proxied-server
sidecar — no Dolt data is copied or moved. Stop the running proxy first with
'bd dolt stop'.

Note: dolt_mode lives in the committed metadata.json, so this change propagates
to clones on the next push.`,
	Args: cobra.NoArgs,
	RunE: migrateFromProxiedRunE("migrate-to-server", "migrate from-proxied-server-to-server", false),
}

var migrateToSharedServerCmd = &cobra.Command{
	Use:           "from-proxied-server-to-shared-server",
	Short:         "[EXPERIMENTAL] Switch a proxied-server repo back to shared-server mode",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Switch a repo from proxied-server mode back to shared-server mode.

Only applies to a proxied-server repo rooted at the shared dolt directory
(~/.beads/shared-server/dolt) — the reverse of from-shared-server-to-proxied-server.
This rewrites .beads/metadata.json (dolt_mode), re-enables dolt.shared-server, and
removes the proxied-server sidecar; no Dolt data is copied or moved. Stop the
running proxy first with 'bd dolt stop'.`,
	Args: cobra.NoArgs,
	RunE: migrateFromProxiedRunE("migrate-to-shared-server", "migrate from-proxied-server-to-shared-server", true),
}

func resolveMigrateIdleTimeout(cmd *cobra.Command) (time.Duration, error) {
	if !cmd.Flags().Changed("idle-timeout") {
		return 0, nil
	}
	v, _ := cmd.Flags().GetDuration("idle-timeout")
	if v < 0 {
		return 0, HandleError("--idle-timeout must be 0 (never) or a positive duration, got %s", v)
	}
	if v == 0 {
		return proxy.IdleTimeoutNever, nil
	}
	return v, nil
}

func migrateModeBeadsDir() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}
	return beadsDir, nil
}

func loadMigrateModeConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, HandleError("failed to load config: %v", err)
	}
	if cfg == nil {
		return nil, HandleError("no beads database found in %s — run 'bd init' first", beadsDir)
	}
	return cfg, nil
}

// migrateGateDestRoot resolves the physical root the DESTINATION mode of a
// dolt-mode migration will use, so the migration can gate it alongside the
// source's roots. Both proxied-server migration directions keep the data
// directory in place, so this is the shared dolt dir for --shared flows and
// the per-project dolt data dir otherwise — resolved side-effect-free (no
// mkdir) because gate planning must not create the tree it is guarding.
func migrateGateDestRoot(shared bool, beadsDir string) (string, error) {
	if shared {
		return doltserver.SharedDoltPath()
	}
	return doltserver.DoltDirPath(beadsDir), nil
}

// acquireMigrateGates takes the workspace gate plus the physical-root gates
// for BOTH the source mode's roots (via ResolvePhysicalRoots inside
// acquireExclusiveWorkspaceGates) and the destination mode's root, all
// EXCLUSIVE in one AcquireAll. It must run BEFORE acquireMigrateLock:
// the normative lock ordering is workspace gate(s) → physical-root gate(s)
// → migrate.lock → embedded .lock → proxy locks → dolt-server.lock.
func acquireMigrateGates(beadsDir string, shared bool, reason string) (func(), error) {
	destRoot, err := migrateGateDestRoot(shared, beadsDir)
	if err != nil {
		return nil, HandleError("failed to resolve migration destination root: %v", err)
	}
	// getRootContext(), not the rootCtx global: it honors the per-command
	// context when globals are disabled, and normalizes the not-yet-set case
	// to context.Background().
	h, err := acquireExclusiveWorkspaceGates(getRootContext(), beadsDir, reason, destRoot)
	if err != nil {
		return nil, HandleErrorWithHint(
			fmt.Sprintf("cannot migrate while other bd activity holds this workspace: %v", err),
			"wait for running bd commands to finish, then retry")
	}
	return func() { _ = h.Release() }, nil
}

// acquireMigrateLock takes the legacy per-workspace migrate.lock. Known
// hazard, deliberately left as-is: this lock file lives INSIDE .beads and is
// removed on release, which is exactly the split-inode pattern the
// workspacegate package documents as unsafe for operations that replace
// directories. The workspace/physical-root gates acquired above (see
// acquireMigrateGates) are the durable fence; replacing migrate.lock itself
// is out of scope here (PR-B2+).
func acquireMigrateLock(beadsDir string) (func(), error) {
	lockPath := filepath.Join(beadsDir, migrateLockFileName)
	lock, err := util.TryLock(lockPath)
	if err != nil {
		if lockfile.IsLocked(err) {
			return nil, HandleErrorWithHint("another bd migrate is in progress on this workspace", "wait for it to finish, then retry")
		}
		return nil, HandleError("failed to acquire migration lock: %v", err)
	}
	return func() {
		lock.Unlock()
		_ = os.Remove(lockPath)
	}, nil
}

func migrateLockErr(what string, err error) error {
	if lockfile.IsLocked(err) {
		return HandleErrorWithHint(fmt.Sprintf("%s is still running", what), "stop it first: bd dolt stop")
	}
	return HandleError("failed to acquire %s lock: %v", what, err)
}

func runMigrateToProxiedServer(dryRun bool, idleTimeout time.Duration, shared bool) error {
	beadsDir, err := migrateModeBeadsDir()
	if err != nil {
		return err
	}
	if !dryRun {
		releaseGates, err := acquireMigrateGates(beadsDir, shared, "bd migrate to proxied-server")
		if err != nil {
			return err
		}
		defer releaseGates()
		releaseMigrateLock, err := acquireMigrateLock(beadsDir)
		if err != nil {
			return err
		}
		defer releaseMigrateLock()
	}
	cfg, err := loadMigrateModeConfig(beadsDir)
	if err != nil {
		return err
	}
	j, jerr := loadMigrateJournal(beadsDir)
	if jerr != nil {
		return HandleError("migration state is unreadable: %v", jerr)
	}
	if err := validateMigrationJournalAgainstConfig(j, cfg); err != nil {
		return HandleError("migration state is inconsistent: %v", err)
	}
	if cfg.IsDoltProxiedServerMode() && j == nil {
		info, ierr := configfile.LoadProxiedServerClientInfo(beadsDir)
		if ierr != nil {
			return HandleError("migration sidecar is unreadable: %v", ierr)
		}
		if info != nil {
			fmt.Printf("%s\n", ui.RenderPass("✓ Already in proxied-server mode"))
			return nil
		}
		return HandleError("repo is marked proxied-server but %s is missing; rerun migration with a recoverable journal", configfile.ProxiedServerClientInfoFileName)
	}

	var rootPath string
	if j != nil {
		rootPath = j.RootPath
	} else if shared {
		if !doltserver.IsSharedServerMode() {
			return HandleError("repo is not in shared-server mode; this command only migrates shared-server repos")
		}
		rootPath, err = doltserver.SharedDoltDir()
		if err != nil {
			return HandleError("failed to resolve shared dolt directory: %v", err)
		}
	} else {
		rootPath = doltserver.DoltDirPath(beadsDir)
		if !cfg.IsDoltServerMode() {
			return HandleError("repo is not in server mode (dolt_mode=%q); this command only migrates server-mode repos", cfg.GetDoltMode())
		}
		// This migration only re-points the proxy at the LOCAL Dolt root;
		// it does not copy data. A workspace whose server mode comes from a
		// remote host (GH#3545 inference or explicit config) would silently
		// switch from the remote data to an empty/stale local database.
		if host := cfg.GetDoltServerHost(); !configfile.IsLocalHostString(host) {
			return HandleError("the configured Dolt server host is remote (%s); this migration only re-points the proxy at the local Dolt root and would abandon the remote data.\nMigrate on the server host itself, or clear dolt_server_host / dolt.host / BEADS_DOLT_SERVER_HOST first", host)
		}
		if doltserver.IsSharedServerMode() {
			return HandleErrorWithHint("repo is in shared-server mode", "use 'bd migrate from-shared-server-to-proxied-server'")
		}
	}

	serverDir := doltserver.ResolveServerDir(beadsDir)
	if state, _ := doltserver.IsRunning(serverDir); state != nil && state.Running {
		return HandleErrorWithHint("dolt server is still running", "stop it first: bd dolt stop")
	}

	if dryRun {
		fmt.Println("Dry run mode - no changes will be made")
		fmt.Printf("Would set dolt_mode: %s → %s\n", configfile.DoltModeServer, configfile.DoltModeProxiedServer)
		if shared {
			fmt.Println("Would disable dolt.shared-server")
			fmt.Printf("Would root the proxy at %s\n", rootPath)
		}
		fmt.Printf("Would write %s\n", configfile.ProxiedServerClientInfoFileName)
		for _, p := range doltserver.StateFilePaths(serverDir) {
			fmt.Printf("Would remove %s\n", p)
		}
		return nil
	}

	if j == nil {
		sidecar := &configfile.ProxiedServerClientInfo{RootPath: rootPath, IdleTimeout: idleTimeout}
		j = &migrateJournal{Version: 1, SourceMode: cfg.GetDoltMode(), TargetMode: configfile.DoltModeProxiedServer, Shared: shared, RootPath: rootPath, Sidecar: sidecar, Ownership: "managed-local", Attempt: 1, Phase: migratePrepared}
		if err := saveMigrateJournal(beadsDir, j); err != nil {
			return HandleError("failed to prepare migration: %v", err)
		}
		if err := migrateFault(migratePrepared); err != nil {
			return err
		}
	} else if j.TargetMode != configfile.DoltModeProxiedServer || j.Shared != shared {
		return HandleError("migration journal belongs to a different migration")
	} else {
		j.Attempt++
		if err := saveMigrateJournal(beadsDir, j); err != nil {
			return HandleError("failed to update migration attempt: %v", err)
		}
	}

	if j.Phase == migratePrepared {
		cfg.DoltMode = configfile.DoltModeProxiedServer
		if err := cfg.Save(beadsDir); err != nil {
			return HandleError("failed to save metadata.json: %v", err)
		}
		if shared {
			if err := config.SetYamlConfigInDir(beadsDir, "dolt.shared-server", "false"); err != nil {
				return HandleError("failed to disable dolt.shared-server: %v", err)
			}
		}
		info := &configfile.ProxiedServerClientInfo{RootPath: rootPath, IdleTimeout: idleTimeout}
		if err := configfile.SaveProxiedServerClientInfo(beadsDir, info); err != nil {
			return HandleError("failed to write %s: %v", configfile.ProxiedServerClientInfoFileName, err)
		}
		if err := checkpointMigration(beadsDir, j, migrateTargetConfigured); err != nil {
			return err
		}
	}
	if j.Phase == migrateTargetConfigured {
		if info, e := configfile.LoadProxiedServerClientInfo(beadsDir); e != nil {
			return HandleError("migration sidecar is unreadable: %v", e)
		} else if info == nil && j.Sidecar != nil {
			if e := configfile.SaveProxiedServerClientInfo(beadsDir, j.Sidecar); e != nil {
				return HandleError("failed to repair %s: %v", configfile.ProxiedServerClientInfoFileName, e)
			}
		} else if j.Sidecar != nil && !reflect.DeepEqual(info, j.Sidecar) {
			return HandleError("migration sidecar does not match prepared state")
		}
		if errs := doltserver.RemoveStateFiles(serverDir); len(errs) > 0 {
			return HandleError("failed to retire Dolt server controls: %v", errs[0])
		}
		if err := checkpointMigration(beadsDir, j, migrateOldControlsRetired); err != nil {
			return err
		}
	}
	if j.Phase == migrateOldControlsRetired {
		if c, e := configfile.Load(beadsDir); e != nil || c == nil || !c.IsDoltProxiedServerMode() {
			return HandleError("migration verification failed: metadata mode is not proxied-server")
		}
		if info, e := configfile.LoadProxiedServerClientInfo(beadsDir); e != nil || info == nil {
			return HandleError("migration verification failed: proxied sidecar is missing or unreadable")
		}
		if err := checkpointMigration(beadsDir, j, migrateVerified); err != nil {
			return err
		}
	}
	if j.Phase == migrateVerified {
		if err := checkpointMigration(beadsDir, j, migrateCommitted); err != nil {
			return err
		}
		if err := removeMigrateJournal(beadsDir); err != nil {
			return HandleError("failed to finalize migration: %v", err)
		}
	}
	if j.Phase == migrateCommitted {
		if err := removeMigrateJournal(beadsDir); err != nil {
			return HandleError("failed to finalize migration: %v", err)
		}
	}

	dataDir := proxiedServerRoot(beadsDir)
	if shared {
		dataDir = rootPath
	}
	commandDidWrite.Store(true)
	fmt.Printf("%s\n\n", ui.RenderPass("✓ Switched to proxied-server mode"))
	fmt.Printf("  Data directory unchanged: %s\n", dataDir)
	fmt.Println("  The proxy starts automatically on the next bd command.")
	return nil
}

func runMigrateFromProxiedServer(dryRun bool, shared bool) error {
	beadsDir, err := migrateModeBeadsDir()
	if err != nil {
		return err
	}
	if !dryRun {
		releaseGates, err := acquireMigrateGates(beadsDir, shared, "bd migrate from proxied-server")
		if err != nil {
			return err
		}
		defer releaseGates()
		releaseMigrateLock, err := acquireMigrateLock(beadsDir)
		if err != nil {
			return err
		}
		defer releaseMigrateLock()
	}
	cfg, err := loadMigrateModeConfig(beadsDir)
	if err != nil {
		return err
	}
	j, jerr := loadMigrateJournal(beadsDir)
	if jerr != nil {
		return HandleError("migration state is unreadable: %v", jerr)
	}
	if err := validateMigrationJournalAgainstConfig(j, cfg); err != nil {
		return HandleError("migration state is inconsistent: %v", err)
	}
	var sourceSidecar *configfile.ProxiedServerClientInfo
	if j == nil {
		sourceSidecar, err = configfile.LoadProxiedServerClientInfo(beadsDir)
		if err != nil {
			return HandleError("migration sidecar is unreadable: %v", err)
		}
		if sourceSidecar == nil {
			return HandleError("migration sidecar is missing")
		}
		if sourceSidecar.External != nil {
			return HandleError("cannot migrate an externally hosted proxied Dolt endpoint to local server mode; reconfigure the endpoint on its owner first")
		}
	}
	if j == nil {
		if shared {
			if doltserver.IsSharedServerMode() {
				fmt.Printf("%s\n", ui.RenderPass("✓ Already in shared-server mode"))
				return nil
			}
		} else if cfg.IsDoltServerMode() && !doltserver.IsSharedServerMode() {
			fmt.Printf("%s\n", ui.RenderPass("✓ Already in server mode"))
			return nil
		}
	}
	if j == nil && !cfg.IsDoltProxiedServerMode() {
		return HandleError("repo is not in proxied-server mode (dolt_mode=%q); this command only migrates proxied-server repos", cfg.GetDoltMode())
	}
	expectedTarget := configfile.DoltModeServer
	if shared {
		expectedTarget = "shared-server"
	}
	if j != nil && (j.TargetMode != expectedTarget || j.Shared != shared) {
		return HandleError("migration journal belongs to a different migration")
	}
	// Leaving proxied mode would make dolt_team_server inert and resume
	// bd-driven schema migrations on the bts-owned database.
	if cfg.DoltTeamServer {
		return HandleErrorWithHint("workspace is team-server managed (dolt_team_server in metadata.json); the shared database's schema is owned by beads-team-server",
			"if the database is no longer bts-managed, remove dolt_team_server from .beads/metadata.json first")
	}

	rootDir := ""
	if j != nil {
		rootDir = j.RootPath
	} else {
		rootDir, err = resolveProxiedServerRootPath(beadsDir)
		if err != nil {
			return HandleError("%v", err)
		}
	}

	sharedDolt, sharedErr := doltserver.SharedDoltDir()
	if shared {
		if sharedErr != nil {
			return HandleError("failed to resolve shared dolt directory: %v", sharedErr)
		}
		if rootDir != sharedDolt {
			return HandleErrorWithHint(fmt.Sprintf("proxied-server root %s is not the shared dolt directory", rootDir), "use 'bd migrate from-proxied-server-to-server'")
		}
	} else if sharedErr == nil && rootDir == sharedDolt {
		return HandleErrorWithHint("proxied-server is rooted at the shared dolt directory", "use 'bd migrate from-proxied-server-to-shared-server'")
	}

	if running, _ := proxy.IsRunning(rootDir); running {
		return HandleErrorWithHint("proxied-server is still running", "stop it first: bd dolt stop")
	}

	var logAssets []string
	if j != nil {
		logAssets = append(logAssets, j.LogAssets...)
	} else {
		logAssets, err = proxiedLogAssets(beadsDir)
		if err != nil {
			return HandleError("%v", err)
		}
	}

	if dryRun {
		fmt.Println("Dry run mode - no changes will be made")
		fmt.Printf("Would set dolt_mode: %s → %s\n", configfile.DoltModeProxiedServer, configfile.DoltModeServer)
		if shared {
			fmt.Println("Would enable dolt.shared-server")
		}
		fmt.Printf("Would remove %s\n", configfile.ProxiedServerClientInfoFileName)
		for _, p := range proxy.ControlFilePaths(rootDir) {
			fmt.Printf("Would remove %s\n", p)
		}
		for _, p := range logAssets {
			fmt.Printf("Would remove %s\n", p)
		}
		return nil
	}

	serverStateDir := beadsDir
	if shared {
		serverStateDir, err = doltserver.SharedServerDir()
		if err != nil {
			return HandleError("failed to resolve shared server directory: %v", err)
		}
	}

	proxyLock, err := util.TryLock(filepath.Join(rootDir, proxy.LockFileName))
	if err != nil {
		return migrateLockErr("proxy", err)
	}
	defer proxyLock.Unlock()

	childLock, err := util.TryLock(filepath.Join(rootDir, server.LockFileName))
	if err != nil {
		return migrateLockErr("proxied dolt sql-server", err)
	}
	defer childLock.Unlock()

	serverLock, err := util.TryLock(doltserver.LockPath(serverStateDir))
	if err != nil {
		return migrateLockErr("dolt sql-server", err)
	}
	defer serverLock.Unlock()
	if j == nil {
		j = &migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: expectedTarget, Shared: shared, RootPath: rootDir, Sidecar: sourceSidecar, LogAssets: logAssets, Ownership: "managed-local", Attempt: 1, Phase: migratePrepared}
		if err := saveMigrateJournal(beadsDir, j); err != nil {
			return HandleError("failed to prepare migration: %v", err)
		}
		if err := migrateFault(migratePrepared); err != nil {
			return err
		}
	} else {
		j.Attempt++
		if err := saveMigrateJournal(beadsDir, j); err != nil {
			return HandleError("failed to update migration attempt: %v", err)
		}
	}
	if j.Phase == migratePrepared {
		if err := doltserver.MarkDoltDirCompatible(rootDir); err != nil {
			return HandleError("failed to mark dolt directory compatible: %v", err)
		}
		cfg.DoltMode = configfile.DoltModeServer
		if err := cfg.Save(beadsDir); err != nil {
			return HandleError("failed to save metadata.json: %v", err)
		}
		if shared {
			if err := config.SetYamlConfigInDir(beadsDir, "dolt.shared-server", "true"); err != nil {
				return HandleError("failed to enable dolt.shared-server: %v", err)
			}
		}
		if err := checkpointMigration(beadsDir, j, migrateTargetConfigured); err != nil {
			return err
		}
	}
	if j.Phase == migrateTargetConfigured {
		if err := os.Remove(configfile.ProxiedServerClientInfoPath(beadsDir)); err != nil && !os.IsNotExist(err) {
			return HandleError("failed to remove %s: %v", configfile.ProxiedServerClientInfoFileName, err)
		}
		if errs := proxy.PurgeControlFiles(rootDir); len(errs) > 0 {
			return HandleError("failed to retire proxy controls: %v", errs[0])
		}
		if errs := removeMigrateAssets(logAssets); len(errs) > 0 {
			return HandleError("failed to retire proxy logs: %v", errs[0])
		}
		if err := checkpointMigration(beadsDir, j, migrateOldControlsRetired); err != nil {
			return err
		}
	}
	if j.Phase == migrateOldControlsRetired {
		if c, e := configfile.Load(beadsDir); e != nil || c == nil || !c.IsDoltServerMode() {
			return HandleError("migration verification failed: metadata mode is not server")
		}
		if shared {
			if v, ok := config.WorkspaceYamlValue(beadsDir, "dolt.shared-server"); !ok || strings.ToLower(v) != "true" {
				return HandleError("migration verification failed: shared-server is not enabled")
			}
		}
		if err := checkpointMigration(beadsDir, j, migrateVerified); err != nil {
			return err
		}
	}
	if j.Phase == migrateVerified {
		if err := checkpointMigration(beadsDir, j, migrateCommitted); err != nil {
			return err
		}
		if err := removeMigrateJournal(beadsDir); err != nil {
			return HandleError("failed to finalize migration: %v", err)
		}
	}
	if j.Phase == migrateCommitted {
		if err := removeMigrateJournal(beadsDir); err != nil {
			return HandleError("failed to finalize migration: %v", err)
		}
	}

	commandDidWrite.Store(true)
	if shared {
		fmt.Printf("%s\n\n", ui.RenderPass("✓ Switched to shared-server mode"))
	} else {
		fmt.Printf("%s\n\n", ui.RenderPass("✓ Switched to server mode"))
	}
	fmt.Printf("  Data directory unchanged: %s\n", rootDir)
	fmt.Println("  The dolt sql-server starts automatically on the next bd command.")
	return nil
}

func proxiedLogAssets(beadsDir string) ([]string, error) {
	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, err
	}
	if isCustomLog {
		return nil, nil
	}
	return []string{logPath}, nil
}

func removeMigrateAssets(paths []string) []error {
	var errs []error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errs
}

func warnMigrateRemovalErrors(errs []error) {
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "Warning: could not remove migration asset: %v\n", err)
	}
}
