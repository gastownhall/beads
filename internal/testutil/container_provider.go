//go:build !windows

package testutil

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"

	"github.com/steveyegge/beads/internal/doltserver"
)

// ContainerProvider is a doltserver.ServerProvider backed by a testcontainers
// Dolt SQL server. It implements Start/Stop/IsRunning so that integration
// tests can swap it in via doltserver.SetProvider.
type ContainerProvider struct {
	container *dolt.DoltContainer
	port      int
}

// NewContainerProvider starts a Dolt container and returns a provider that
// satisfies doltserver.ServerProvider. The container is terminated when the
// provider's Stop method is called.
func NewContainerProvider() (*ContainerProvider, error) {
	if state := checkDolt(); state != doltReady {
		return nil, fmt.Errorf("cannot create container provider: %s", state)
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
	defer cancel()

	ctr, err := dolt.Run(ctx, DoltDockerImage,
		dolt.WithDatabase("beads_test"),
		testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
	)
	if err != nil {
		return nil, fmt.Errorf("starting Dolt container: %w", err)
	}

	p, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return nil, fmt.Errorf("getting mapped port: %w", err)
	}

	port, err := strconv.Atoi(p.Port())
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return nil, fmt.Errorf("parsing port %q: %w", p.Port(), err)
	}

	return &ContainerProvider{
		container: ctr,
		port:      port,
	}, nil
}

// Port returns the host-mapped port the container is listening on.
func (p *ContainerProvider) Port() int {
	return p.port
}

// Start writes the port file into serverDir so that DefaultConfig and other
// code that reads state files can discover the container's port. The
// container itself is already running (started in NewContainerProvider).
func (p *ContainerProvider) Start(serverDir string) (*doltserver.State, error) {
	// Write port file so DefaultConfig / readPortFile can find the port.
	portPath := serverDir + "/dolt-server.port"
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(p.port)), 0600); err != nil {
		return nil, fmt.Errorf("writing port file: %w", err)
	}
	return &doltserver.State{
		Running: true,
		Port:    p.port,
	}, nil
}

// Stop terminates the container and cleans up state files.
func (p *ContainerProvider) Stop(serverDir string) error {
	// Clean up state files.
	_ = os.Remove(serverDir + "/dolt-server.port")
	_ = os.Remove(serverDir + "/dolt-server.pid")

	if p.container == nil {
		return nil
	}
	err := testcontainers.TerminateContainer(p.container)
	p.container = nil
	return err
}

// IsRunning checks if the container is reachable via TCP dial.
func (p *ContainerProvider) IsRunning(serverDir string) (*doltserver.State, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", p.port)
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return &doltserver.State{Running: false}, nil
	}
	_ = conn.Close()
	return &doltserver.State{
		Running: true,
		Port:    p.port,
	}, nil
}
