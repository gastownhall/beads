//go:build windows

package testutil

import (
	"fmt"

	"github.com/steveyegge/beads/internal/doltserver"
)

// ContainerProvider is not supported on Windows.
type ContainerProvider struct {
	port int
}

// NewContainerProvider is not supported on Windows.
func NewContainerProvider() (*ContainerProvider, error) {
	return nil, fmt.Errorf("ContainerProvider not available on Windows")
}

// Port returns 0 on Windows.
func (p *ContainerProvider) Port() int { return 0 }

// Start is not supported on Windows.
func (p *ContainerProvider) Start(serverDir string) (*doltserver.State, error) {
	return nil, fmt.Errorf("ContainerProvider not available on Windows")
}

// Stop is a no-op on Windows.
func (p *ContainerProvider) Stop(serverDir string) error { return nil }

// IsRunning always returns not running on Windows.
func (p *ContainerProvider) IsRunning(serverDir string) (*doltserver.State, error) {
	return &doltserver.State{Running: false}, nil
}
