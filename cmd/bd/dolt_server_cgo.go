//go:build cgo && !windows
package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// DoltServerHandle wraps a dolt.Server for CGO builds
type DoltServerHandle struct {
	server *dolt.Server
}

// DoltDefaultSQLPort is the default SQL port for dolt server
const DoltDefaultSQLPort = dolt.DefaultSQLPort

// DoltDefaultRemotesAPIPort is the default remotesapi port for dolt server
const DoltDefaultRemotesAPIPort = dolt.DefaultRemotesAPIPort

// StartDoltServer starts a dolt sql-server for federation mode
func StartDoltServer(ctx context.Context, dataDir, logFile string, sqlPort, remotePort int) (*DoltServerHandle, error) {
	server := dolt.NewServer(dolt.ServerConfig{
		DataDir:        dataDir,
		SQLPort:        sqlPort,
		RemotesAPIPort: remotePort,
		Host:           "127.0.0.1",
		LogFile:        logFile,
	})

	if err := server.Start(ctx); err != nil {
		return nil, err
	}

	return &DoltServerHandle{server: server}, nil
}

// Stop stops the dolt server
func (h *DoltServerHandle) Stop() error {
	if h.server != nil {
		return h.server.Stop()
	}
	return nil
}

// SQLPort returns the SQL port the server is listening on
func (h *DoltServerHandle) SQLPort() int {
	if h.server != nil {
		return h.server.SQLPort()
	}
	return 0
}

// RemotesAPIPort returns the remotesapi port the server is listening on
func (h *DoltServerHandle) RemotesAPIPort() int {
	if h.server != nil {
		return h.server.RemotesAPIPort()
	}
	return 0
}

// Host returns the host the server is listening on
func (h *DoltServerHandle) Host() string {
	if h.server != nil {
		return h.server.Host()
	}
	return ""
}

// DoltServerAvailable returns true when CGO is available
func DoltServerAvailable() bool {
	return true
}

