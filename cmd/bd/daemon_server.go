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
		log.Info("starting RPC server", "socket", socketPath)
		if err := server.Start(ctx); err != nil {
			log.Error("RPC server error", "error", err)
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		log.Error("RPC server failed to start", "error", err)
		return nil, nil, err
	case <-server.WaitReady():
		log.Info("RPC server ready (socket listening)")
	case <-time.After(5 * time.Second):
		log.Warn("server didn't signal ready after 5 seconds (may still be starting)")
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
				log.Info("parent process died, shutting down daemon", "parent_pid", parentPID)
				cancel()
				if err := server.Stop(); err != nil {
					log.Error("stopping server", "error", err)
				}
				return
			}
		case sig := <-sigChan:
			if isReloadSignal(sig) {
				log.Info("received reload signal, ignoring (daemon continues running)")
				continue
			}
			log.Info("received signal, shutting down gracefully", "signal", sig)
			cancel()
			if err := server.Stop(); err != nil {
				log.Error("stopping RPC server", "error", err)
			}
			return
		case <-ctx.Done():
			log.Info("context canceled, shutting down")
			if err := server.Stop(); err != nil {
				log.Error("stopping RPC server", "error", err)
			}
			return
		case err := <-serverErrChan:
			log.Error("RPC server failed", "error", err)
			cancel()
			if err := server.Stop(); err != nil {
				log.Error("stopping RPC server", "error", err)
			}
			return
		}
	}
}
