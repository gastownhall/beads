package daemonrunner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Daemon represents a running background daemon
type Daemon struct {
	cfg    Config
	log    *logger
	logF   *lumberjack.Logger
	store  storage.Storage
	server *rpc.Server
	lock   io.Closer
	cancel context.CancelFunc

	// Version is the daemon's build version
	Version string
}

// New creates a new Daemon instance
func New(cfg Config, version string) *Daemon {
	return &Daemon{
		cfg:     cfg,
		Version: version,
	}
}

// Start runs the daemon main loop
func (d *Daemon) Start() error {
	// Setup logger
	d.logF, d.log = d.setupLogger()
	defer func() { _ = d.logF.Close() }()

	// Determine database path for local daemon
	if !d.cfg.Global {
		if err := d.determineDatabasePath(); err != nil {
			return err
		}
	}

	// Acquire daemon lock
	lock, err := d.setupLock()
	if err != nil {
		return err
	}
	d.lock = lock
	defer func() { _ = d.lock.Close() }()
	defer func() { _ = os.Remove(d.cfg.PIDFile) }()

	d.log.log("Daemon started (interval: %v, auto-commit: %v, auto-push: %v)",
		d.cfg.Interval, d.cfg.AutoCommit, d.cfg.AutoPush)

	// Handle global daemon differently
	if d.cfg.Global {
		return d.runGlobalDaemon()
	}

	// Validate single canonical database
	if err := d.validateSingleDatabase(); err != nil {
		return err
	}

	d.log.log("Using database: %s", d.cfg.DBPath)

	// Clear any previous daemon-error file on successful startup
	errFile := filepath.Join(d.cfg.BeadsDir, "daemon-error")
	if err := os.Remove(errFile); err != nil && !os.IsNotExist(err) {
		d.log.log("Warning: could not remove daemon-error file: %v", err)
	}

	// Open database
	store, err := sqlite.New(d.cfg.DBPath)
	if err != nil {
		d.log.log("Error: cannot open database: %v", err)
		return fmt.Errorf("cannot open database: %w", err)
	}
	d.store = store
	defer func() { _ = d.store.Close() }()
	d.log.log("Database opened: %s", d.cfg.DBPath)

	// Validate database fingerprint
	if err := d.validateDatabaseFingerprint(); err != nil {
		if os.Getenv("BEADS_IGNORE_REPO_MISMATCH") != "1" {
			d.log.log("Error: %v", err)
			return err
		}
		d.log.log("Warning: repository mismatch ignored (BEADS_IGNORE_REPO_MISMATCH=1)")
	}

	// Validate schema version
	if err := d.validateSchemaVersion(); err != nil {
		return err
	}

	// Start RPC server
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	defer cancel()

	server, serverErrChan, err := d.startRPCServer(ctx)
	if err != nil {
		return err
	}
	d.server = server

	// Register in global registry
	if err := d.registerDaemon(); err != nil {
		d.log.log("Warning: failed to register daemon: %v", err)
	} else {
		defer d.unregisterDaemon()
	}

	// Run sync loops
	return d.runSyncLoop(ctx, serverErrChan)
}

func (d *Daemon) runGlobalDaemon() error {
	globalDir, err := getGlobalBeadsDir()
	if err != nil {
		d.log.log("Error: cannot get global beads directory: %v", err)
		return err
	}
	d.cfg.SocketPath = getSocketPath(globalDir)

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	defer cancel()

	server, _, err := d.startRPCServer(ctx)
	if err != nil {
		return err
	}
	d.server = server

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	sig := <-sigChan
	d.log.log("Received signal: %v", sig)
	d.log.log("Shutting down global daemon...")

	cancel()
	if err := d.server.Stop(); err != nil {
		d.log.log("Error stopping server: %v", err)
	}

	d.log.log("Global daemon stopped")
	return nil
}

func getGlobalBeadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot get home directory: %w", err)
	}

	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create global beads directory: %w", err)
	}

	return beadsDir, nil
}

func (d *Daemon) setupLock() (io.Closer, error) {
	beadsDir := filepath.Dir(d.cfg.PIDFile)
	lock, err := acquireDaemonLock(beadsDir, d.cfg.DBPath, d.Version)
	if err != nil {
		if err == ErrDaemonLocked {
			d.log.log("Daemon already running (lock held), exiting")
		} else {
			d.log.log("Error acquiring daemon lock: %v", err)
		}
		return nil, err
	}

	if err := ensurePIDFileCorrect(d.cfg.PIDFile); err != nil {
		d.log.log("Warning: failed to verify PID file: %v", err)
	}

	return lock, nil
}

// Stop gracefully shuts down the daemon
func (d *Daemon) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	if d.server != nil {
		return d.server.Stop()
	}
	return nil
}

func (d *Daemon) determineDatabasePath() error {
	if d.cfg.DBPath != "" {
		return nil
	}

	// Use public API to find database
	foundDB := beads.FindDatabasePath()
	if foundDB == "" {
		d.log.log("Error: no beads database found")
		d.log.log("Hint: run 'bd init' to create a database or set BEADS_DB environment variable")
		return fmt.Errorf("no beads database found")
	}

	d.cfg.DBPath = foundDB
	d.cfg.BeadsDir = filepath.Dir(foundDB)
	d.cfg.WorkspacePath = filepath.Dir(d.cfg.BeadsDir)
	d.cfg.SocketPath = getSocketPath(d.cfg.BeadsDir)
	return nil
}

func (d *Daemon) validateSingleDatabase() error {
	// Check for multiple .db files (ambiguity error)
	matches, err := filepath.Glob(filepath.Join(d.cfg.BeadsDir, "*.db"))
	if err == nil && len(matches) > 1 {
		// Filter out backup files
		var validDBs []string
		for _, match := range matches {
			baseName := filepath.Base(match)
			if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
				validDBs = append(validDBs, match)
			}
		}
		if len(validDBs) > 1 {
			errMsg := fmt.Sprintf("Error: Multiple database files found in %s:\n", d.cfg.BeadsDir)
			for _, db := range validDBs {
				errMsg += fmt.Sprintf("  - %s\n", filepath.Base(db))
			}
			errMsg += fmt.Sprintf("\nBeads requires a single canonical database: %s\n", beads.CanonicalDatabaseName)
			errMsg += "Run 'bd init' to migrate legacy databases or manually remove old databases\n"
			errMsg += "Or run 'bd doctor' for more diagnostics"

			d.log.log(errMsg)

			// Write error to file so user can see it without checking logs
			errFile := filepath.Join(d.cfg.BeadsDir, "daemon-error")
			// nolint:gosec // G306: Error file needs to be readable for debugging
			_ = os.WriteFile(errFile, []byte(errMsg), 0644)

			return fmt.Errorf("multiple database files found")
		}
	}

	// Validate using canonical name
	dbBaseName := filepath.Base(d.cfg.DBPath)
	if dbBaseName != beads.CanonicalDatabaseName {
		d.log.log("Error: Non-canonical database name: %s", dbBaseName)
		d.log.log("Expected: %s", beads.CanonicalDatabaseName)
		d.log.log("")
		d.log.log("Run 'bd init' to migrate to canonical name")
		return fmt.Errorf("non-canonical database name: %s", dbBaseName)
	}

	return nil
}

func (d *Daemon) validateSchemaVersion() error {
	ctx := context.Background()
	dbVersion, err := d.store.GetMetadata(ctx, "bd_version")
	if err != nil && err.Error() != "metadata key not found: bd_version" {
		d.log.log("Error: failed to read database version: %v", err)
		return fmt.Errorf("failed to read database version: %w", err)
	}

	mismatch, missing := checkVersionMismatch(dbVersion, d.Version)

	if mismatch {
		d.log.log("Error: Database schema version mismatch")
		d.log.log("  Database version: %s", dbVersion)
		d.log.log("  Daemon version: %s", d.Version)
		d.log.log("")
		d.log.log("The database was created with a different version of bd.")
		d.log.log("This may cause compatibility issues.")
		d.log.log("")
		d.log.log("Options:")
		d.log.log("  1. Run 'bd migrate' to update the database to the current version")
		d.log.log("  2. Upgrade/downgrade bd to match database version: %s", dbVersion)
		d.log.log("  3. Set BEADS_IGNORE_VERSION_MISMATCH=1 to proceed anyway (not recommended)")
		d.log.log("")

		if os.Getenv("BEADS_IGNORE_VERSION_MISMATCH") != "1" {
			return fmt.Errorf("database version mismatch")
		}
		d.log.log("Warning: Proceeding despite version mismatch (BEADS_IGNORE_VERSION_MISMATCH=1)")
	} else if missing {
		d.log.log("Warning: Database missing version metadata, setting to %s", d.Version)
		if err := d.store.SetMetadata(ctx, "bd_version", d.Version); err != nil {
			d.log.log("Error: failed to set database version: %v", err)
			return fmt.Errorf("failed to set database version: %w", err)
		}
	}

	return nil
}

func (d *Daemon) registerDaemon() error {
	registry, err := daemon.NewRegistry()
	if err != nil {
		return err
	}

	entry := daemon.RegistryEntry{
		WorkspacePath: d.cfg.WorkspacePath,
		SocketPath:    d.cfg.SocketPath,
		DatabasePath:  d.cfg.DBPath,
		PID:           os.Getpid(),
		Version:       d.Version,
		StartedAt:     time.Now(),
	}

	if err := registry.Register(entry); err != nil {
		return err
	}

	d.log.log("Registered in global registry")
	return nil
}

func (d *Daemon) unregisterDaemon() {
	registry, err := daemon.NewRegistry()
	if err != nil {
		d.log.log("Warning: failed to create registry for unregister: %v", err)
		return
	}

	if err := registry.Unregister(d.cfg.WorkspacePath, os.Getpid()); err != nil {
		d.log.log("Warning: failed to unregister daemon: %v", err)
	}
}
