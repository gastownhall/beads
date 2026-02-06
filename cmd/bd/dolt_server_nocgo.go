//go:build !cgo
package main

import (
	"context"
	"errors"
)

// DoltServerHandle is a stub for non-CGO builds
type DoltServerHandle struct{}

// DoltDefaultSQLPort is the default SQL port for dolt server
const DoltDefaultSQLPort = 3306

// DoltDefaultRemotesAPIPort is the default remotesapi port for dolt server
const DoltDefaultRemotesAPIPort = 50051

// ErrDoltRequiresCGO is returned when dolt features are requested without CGO
var ErrDoltRequiresCGO = errors.New("dolt backend requires CGO; use pre-built binaries from GitHub releases or enable CGO")

// StartDoltServer returns an error in non-CGO builds
func StartDoltServer(ctx context.Context, dataDir, logFile string, sqlPort, remotePort int) (*DoltServerHandle, error) {
	return nil, ErrDoltRequiresCGO
}

// Stop is a no-op stub
func (h *DoltServerHandle) Stop() error {
	return nil
}

// SQLPort returns 0 in non-CGO builds
func (h *DoltServerHandle) SQLPort() int {
	return 0
}

// RemotesAPIPort returns 0 in non-CGO builds
func (h *DoltServerHandle) RemotesAPIPort() int {
	return 0
}

// Host returns empty string in non-CGO builds
func (h *DoltServerHandle) Host() string {
	return ""
}

// DoltServerAvailable returns false when CGO is not available
func DoltServerAvailable() bool {
	return false
}


