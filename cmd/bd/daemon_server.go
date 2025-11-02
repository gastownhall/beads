package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
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

// runGlobalDaemon runs the global routing daemon
func runGlobalDaemon(log daemonLogger) {
	globalDir, err := getGlobalBeadsDir()
	if err != nil {
		log.log("Error: cannot get global beads directory: %v", err)
		os.Exit(1)
	}
	socketPath := filepath.Join(globalDir, "bd.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, _, err := startRPCServer(ctx, socketPath, nil, globalDir, "", log)
	if err != nil {
		return
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	sig := <-sigChan
	log.log("Received signal: %v", sig)
	log.log("Shutting down global daemon...")

	cancel()
	if err := server.Stop(); err != nil {
		log.log("Error stopping server: %v", err)
	}

	log.log("Global daemon stopped")
}

// runEventLoop runs the daemon event loop (polling mode)
func runEventLoop(ctx context.Context, cancel context.CancelFunc, ticker *time.Ticker, doSync func(), server *rpc.Server, serverErrChan chan error, log daemonLogger) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			doSync()
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
