//go:build !windows

package testutil

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
)

// ContainerProvider manages a testcontainers Dolt SQL server for integration
// tests. Use NewContainerProvider to start the container, Port() to get the
// mapped host port, and Stop() to tear it down.
type ContainerProvider struct {
	container *dolt.DoltContainer
	port      int
}

// NewContainerProvider starts a Dolt container and returns a provider.
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

// WritePortFile writes the container port to the given shared server directory
// so that bd subprocesses can discover it via DefaultConfig / readPortFile.
func (p *ContainerProvider) WritePortFile(serverDir string) error {
	portPath := serverDir + "/dolt-server.port"
	return os.WriteFile(portPath, []byte(strconv.Itoa(p.port)), 0600)
}

// Stop terminates the container.
func (p *ContainerProvider) Stop() error {
	if p.container == nil {
		return nil
	}
	err := testcontainers.TerminateContainer(p.container)
	p.container = nil
	return err
}
