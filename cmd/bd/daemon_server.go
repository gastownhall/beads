package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
)

// startRPCServer initializes and starts the RPC server
func startRPCServer(ctx context.Context, socketPath string, store storage.Storage, workspacePath string, dbPath string, log daemonLogger) (*rpc.Server, chan error, error) {
	// Sync daemon version with CLI version
	rpc.ServerVersion = Version
	
	server := rpc.NewServer(socketPath, store, workspacePath, dbPath)
	serverErrChan := make(chan error, 1)

	go func() {
		log.log("Starting RPC server: %s", socketPath)
		if err := server.Start(ctx); err != nil {
			log.log("RPC server error: %v", err)
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		log.log("RPC server failed to start: %v", err)
		return nil, nil, err
	case <-server.WaitReady():
		log.log("RPC server ready (socket listening)")
	case <-time.After(5 * time.Second):
		log.log("WARNING: Server didn't signal ready after 5 seconds (may still be starting)")
	}

	return server, serverErrChan, nil
}

// checkParentProcessAlive checks if the parent process is still running.
// Returns true if parent is alive, false if it died.
// Returns true if parent PID is 0 or 1 (not tracked, or adopted by init).
func checkParentProcessAlive(parentPID int) bool {
	if parentPID == 0 {
		// Parent PID not tracked (older lock files)
		return true
	}
	
	if parentPID == 1 {
		// Adopted by init/launchd - this is normal for detached daemons on macOS/Linux
		// Don't treat this as parent death
		return true
	}
	
	// Check if parent process is running
	return isProcessRunning(parentPID)
}

// runEventLoop runs the daemon event loop (polling mode)
func runEventLoop(ctx context.Context, cancel context.CancelFunc, ticker *time.Ticker, doSync func(), server *rpc.Server, serverErrChan chan error, parentPID int, log daemonLogger) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	// Parent process check (every 10 seconds)
	parentCheckTicker := time.NewTicker(10 * time.Second)
	defer parentCheckTicker.Stop()

	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			doSync()
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
		case sig := <-sigChan:
			if isReloadSignal(sig) {
				log.log("Received reload signal, ignoring (daemon continues running)")
				continue
			}
			log.log("Received signal %v, shutting down gracefully...", sig)
			cancel()
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		case <-ctx.Done():
			log.log("Context canceled, shutting down")
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		case err := <-serverErrChan:
			log.log("RPC server failed: %v", err)
			cancel()
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		}
	}
}
