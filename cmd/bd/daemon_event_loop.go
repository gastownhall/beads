package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
)

// runEventDrivenLoop implements event-driven daemon architecture.
// Replaces polling ticker with reactive event handlers for:
// - File system changes (JSONL modifications)
// - RPC mutations (create, update, delete)
// - Git operations (via hooks, optional)
// - Parent process monitoring (exit if parent dies)
func runEventDrivenLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	server *rpc.Server,
	serverErrChan chan error,
	store storage.Storage,
	jsonlPath string,
	doExport func(),
	doAutoImport func(),
	parentPID int,
	log daemonLogger,
) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	// Debounced sync actions
	exportDebouncer := NewDebouncer(500*time.Millisecond, func() {
		log.log("Export triggered by mutation events")
		doExport()
	})
	defer exportDebouncer.Cancel()

	importDebouncer := NewDebouncer(500*time.Millisecond, func() {
		log.log("Import triggered by file change")
		doAutoImport()
	})
	defer importDebouncer.Cancel()

	// Start file watcher for JSONL changes
	watcher, err := NewFileWatcher(jsonlPath, func() {
		importDebouncer.Trigger()
	})
	var fallbackTicker *time.Ticker
	if err != nil {
		log.log("WARNING: File watcher unavailable (%v), using 60s polling fallback", err)
		watcher = nil
		// Fallback ticker to check for remote changes when watcher unavailable
		fallbackTicker = time.NewTicker(60 * time.Second)
		defer fallbackTicker.Stop()
	} else {
		watcher.Start(ctx, log)
		defer func() { _ = watcher.Close() }()
	}

	// Handle mutation events from RPC server
	mutationChan := server.MutationChan()
	go func() {
		for {
			select {
			case event, ok := <-mutationChan:
				if !ok {
					// Channel closed (should never happen, but handle defensively)
					log.log("Mutation channel closed; exiting listener")
					return
				}
				log.log("Mutation detected: %s %s", event.Type, event.IssueID)
				exportDebouncer.Trigger()

			case <-ctx.Done():
				return
			}
		}
	}()

	// Periodic health check
	healthTicker := time.NewTicker(60 * time.Second)
	defer healthTicker.Stop()

	// Parent process check (every 10 seconds)
	parentCheckTicker := time.NewTicker(10 * time.Second)
	defer parentCheckTicker.Stop()

	// Dropped events safety net (faster recovery than health check)
	droppedEventsTicker := time.NewTicker(1 * time.Second)
	defer droppedEventsTicker.Stop()

	for {
		select {
		case <-droppedEventsTicker.C:
			// Check for dropped mutation events every second
			dropped := server.ResetDroppedEventsCount()
			if dropped > 0 {
				log.log("WARNING: %d mutation events were dropped, triggering export", dropped)
				exportDebouncer.Trigger()
			}

		case <-healthTicker.C:
			// Periodic health validation (not sync)
			checkDaemonHealth(ctx, store, log)

		case <-parentCheckTicker.C:
			// Check if parent process is still alive
			if !checkParentProcessAlive(parentPID) {
				log.log("Parent process (PID %d) died, shutting down daemon", parentPID)
				cancel()
				if err := server.Stop(); err != nil {
					log.log("Error stopping server: %v", err)
				}
				return
			}

		case <-func() <-chan time.Time {
			if fallbackTicker != nil {
				return fallbackTicker.C
			}
			// Never fire if watcher is available
			return make(chan time.Time)
		}():
			log.log("Fallback ticker: checking for remote changes")
			importDebouncer.Trigger()

		case sig := <-sigChan:
			if isReloadSignal(sig) {
				log.log("Received reload signal, ignoring")
				continue
			}
			log.log("Received signal %v, shutting down...", sig)
			cancel()
			if err := server.Stop(); err != nil {
				log.log("Error stopping server: %v", err)
			}
			return

		case <-ctx.Done():
		log.log("Context canceled, shutting down")
		if watcher != nil {
		_ = watcher.Close()
		}
			if err := server.Stop(); err != nil {
				log.log("Error stopping server: %v", err)
			}
			return

		case err := <-serverErrChan:
		log.log("RPC server failed: %v", err)
		cancel()
		if watcher != nil {
		_ = watcher.Close()
		}
		if stopErr := server.Stop(); stopErr != nil {
			log.log("Error stopping server: %v", stopErr)
		}
		return
		}
	}
}

// checkDaemonHealth performs periodic health validation.
// Separate from sync operations - just validates state.
//
// Implements bd-e0o: Phase 3 daemon robustness for GH #353
// Implements bd-gqo: Additional health checks
func checkDaemonHealth(ctx context.Context, store storage.Storage, log daemonLogger) {
	// Health check 1: Verify metadata is accessible
	// This helps detect if external operations (like bd import --force) have modified metadata
	// Without this, daemon may continue operating with stale metadata cache
	// Try new key first, fall back to old for migration (bd-39o)
	if _, err := store.GetMetadata(ctx, "jsonl_content_hash"); err != nil {
		if _, err := store.GetMetadata(ctx, "last_import_hash"); err != nil {
			log.log("Health check: metadata read failed: %v", err)
			// Non-fatal: daemon continues but logs the issue
			// This helps diagnose stuck states in sandboxed environments
		}
	}

	// Health check 2: Database integrity check
	// Verify the database is accessible and structurally sound
	if db := store.UnderlyingDB(); db != nil {
		// Quick integrity check - just verify we can query
		var result string
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
			log.log("Health check: database integrity check failed: %v", err)
		} else if result != "ok" {
			log.log("Health check: database integrity issue: %s", result)
		}
	}

	// Health check 3: Disk space check (platform-specific)
	// Uses checkDiskSpace helper which is implemented per-platform
	dbPath := store.Path()
	if dbPath != "" {
		if availableMB, ok := checkDiskSpace(dbPath); ok {
			// Warn if less than 100MB available
			if availableMB < 100 {
				log.log("Health check: low disk space warning: %dMB available", availableMB)
			}
		}
	}

	// Health check 4: Memory usage check
	// Log warning if memory usage is unusually high
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	heapMB := memStats.HeapAlloc / (1024 * 1024)

	// Warn if heap exceeds 500MB (daemon should be lightweight)
	if heapMB > 500 {
		log.log("Health check: high memory usage warning: %dMB heap allocated", heapMB)
	}
}
