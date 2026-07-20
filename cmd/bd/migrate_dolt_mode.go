package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	if cfg.IsDoltProxiedServerMode() {
		fmt.Printf("%s\n", ui.RenderPass("✓ Already in proxied-server mode"))
		return nil
	}

	var rootPath string
	if shared {
		if !doltserver.IsSharedServerMode() {
			return HandleError("repo is not in shared-server mode; this command only migrates shared-server repos")
		}
		rootPath, err = doltserver.SharedDoltDir()
		if err != nil {
			return HandleError("failed to resolve shared dolt directory: %v", err)
		}
	} else {
		if !cfg.IsDoltServerMode() {
			return HandleError("repo is not in server mode (dolt_mode=%q); this command only migrates server-mode repos", cfg.GetDoltMode())
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

	cfg.DoltMode = configfile.DoltModeProxiedServer
	if err := cfg.Save(beadsDir); err != nil {
		return HandleError("failed to save metadata.json: %v", err)
	}

	if shared {
		if err := config.SetYamlConfigInDir(beadsDir, "dolt.shared-server", "false"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not disable dolt.shared-server: %v\n", err)
		}
	}

	info := &configfile.ProxiedServerClientInfo{RootPath: rootPath, IdleTimeout: idleTimeout}
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, info); err != nil {
		return HandleError("failed to write %s: %v", configfile.ProxiedServerClientInfoFileName, err)
	}

	warnMigrateRemovalErrors(doltserver.RemoveStateFiles(serverDir))

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
	if shared {
		if doltserver.IsSharedServerMode() {
			fmt.Printf("%s\n", ui.RenderPass("✓ Already in shared-server mode"))
			return nil
		}
	} else if cfg.IsDoltServerMode() && !doltserver.IsSharedServerMode() {
		fmt.Printf("%s\n", ui.RenderPass("✓ Already in server mode"))
		return nil
	}
	if !cfg.IsDoltProxiedServerMode() {
		return HandleError("repo is not in proxied-server mode (dolt_mode=%q); this command only migrates proxied-server repos", cfg.GetDoltMode())
	}

	rootDir, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return HandleError("%v", err)
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

	logAssets, err := proxiedLogAssets(beadsDir)
	if err != nil {
		return HandleError("%v", err)
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

	if err := doltserver.MarkDoltDirCompatible(rootDir); err != nil {
		return HandleError("failed to mark dolt directory compatible: %v", err)
	}

	cfg.DoltMode = configfile.DoltModeServer
	if err := cfg.Save(beadsDir); err != nil {
		return HandleError("failed to save metadata.json: %v", err)
	}

	if shared {
		if err := config.SetYamlConfigInDir(beadsDir, "dolt.shared-server", "true"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not enable dolt.shared-server: %v\n", err)
		}
	}

	if err := os.Remove(configfile.ProxiedServerClientInfoPath(beadsDir)); err != nil && !os.IsNotExist(err) {
		return HandleError("failed to remove %s: %v", configfile.ProxiedServerClientInfoFileName, err)
	}

	warnMigrateRemovalErrors(proxy.PurgeControlFiles(rootDir))
	warnMigrateRemovalErrors(removeMigrateAssets(logAssets))

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
