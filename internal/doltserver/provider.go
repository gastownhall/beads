package doltserver

import "sync"

// ServerProvider abstracts the lifecycle of a dolt sql-server.
// The default CLIProvider spawns a local dolt process; tests can swap in
// a ContainerProvider backed by testcontainers.
type ServerProvider interface {
	// Start launches the server for the given server directory.
	// Returns the State of the started server, or an error.
	Start(serverDir string) (*State, error)

	// Stop shuts down the server for the given server directory.
	Stop(serverDir string) error

	// IsRunning checks if a server is healthy for the given server directory.
	IsRunning(serverDir string) (*State, error)
}

// CLIProvider is the default ServerProvider. It manages a local dolt
// sql-server process via the CLI, with PID/port file tracking.
type CLIProvider struct{}

func (p *CLIProvider) Start(serverDir string) (*State, error) {
	return cliStart(serverDir)
}

func (p *CLIProvider) Stop(serverDir string) error {
	return cliStop(serverDir)
}

func (p *CLIProvider) IsRunning(serverDir string) (*State, error) {
	return cliIsRunning(serverDir)
}

var (
	providerMu sync.RWMutex
	provider   ServerProvider = &CLIProvider{}
)

// SetProvider replaces the active ServerProvider. Pass nil to restore the
// default CLIProvider. Intended for test setup — not safe to call while
// other goroutines are calling Start/Stop/IsRunning.
func SetProvider(p ServerProvider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if p == nil {
		p = &CLIProvider{}
	}
	provider = p
}

func getProvider() ServerProvider {
	providerMu.RLock()
	defer providerMu.RUnlock()
	return provider
}
