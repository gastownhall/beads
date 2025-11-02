package daemonrunner

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
)

// startRPCServer initializes and starts the RPC server
func (d *Daemon) startRPCServer(ctx context.Context) (*rpc.Server, chan error, error) {
	// Sync daemon version with CLI version
	rpc.ServerVersion = d.Version
	
	server := rpc.NewServer(d.cfg.SocketPath, d.store, d.cfg.WorkspacePath, d.cfg.DBPath)
	serverErrChan := make(chan error, 1)

	go func() {
		d.log.log("Starting RPC server: %s", d.cfg.SocketPath)
		if err := server.Start(ctx); err != nil {
			d.log.log("RPC server error: %v", err)
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		d.log.log("RPC server failed to start: %v", err)
		return nil, nil, err
	case <-server.WaitReady():
		d.log.log("RPC server ready (socket listening)")
	case <-time.After(5 * time.Second):
		d.log.log("WARNING: Server didn't signal ready after 5 seconds (may still be starting)")
	}

	return server, serverErrChan, nil
}
