package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
)

// runEventDrivenLoop implements event-driven daemon architecture.
// Replaces polling ticker with reactive event handlers for:
// - File system changes (JSONL modifications)
// - RPC mutations (create, update, delete)
// - Git operations (via hooks, optional)
func runEventDrivenLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	server *rpc.Server,
	serverErrChan chan error,
	store storage.Storage,
	jsonlPath string,
	doExport func(),
	doAutoImport func(),
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
	if err != nil {
		log.log("WARNING: File watcher unavailable (%v), mutations will trigger export only", err)
		watcher = nil
	} else {
		watcher.Start(ctx, log)
		defer watcher.Close()
	}

	// Handle mutation events from RPC server
	mutationChan := server.MutationChan()
	go func() {
		for {
			select {
			case event := <-mutationChan:
				log.log("Mutation detected: %s %s", event.Type, event.IssueID)
				exportDebouncer.Trigger()

			case <-ctx.Done():
				return
			}
		}
	}()

	// Optional: Periodic health check (not a sync poll)
	healthTicker := time.NewTicker(60 * time.Second)
	defer healthTicker.Stop()

	for {
		select {
		case <-healthTicker.C:
			// Periodic health validation (not sync)
			checkDaemonHealth(ctx, store, log)

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
				watcher.Close()
			}
			if err := server.Stop(); err != nil {
				log.log("Error stopping server: %v", err)
			}
			return

		case err := <-serverErrChan:
			log.log("RPC server failed: %v", err)
			cancel()
			if watcher != nil {
				watcher.Close()
			}
			return
		}
	}
}

// checkDaemonHealth performs periodic health validation.
// Separate from sync operations - just validates state.
func checkDaemonHealth(ctx context.Context, store storage.Storage, log daemonLogger) {
	// TODO: Add health checks:
	// - Database integrity check
	// - Disk space check
	// - Memory usage check
	// For now, this is a no-op placeholder
}
