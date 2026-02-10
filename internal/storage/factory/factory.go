// Package factory provides functions for creating storage backends based on configuration.
package factory

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// BackendFactory is a function that creates a storage backend
type BackendFactory func(ctx context.Context, path string, opts Options) (storage.Storage, error)

// backendRegistry holds registered backend factories
var backendRegistry = make(map[string]BackendFactory)

// RegisterBackend registers a storage backend factory
func RegisterBackend(name string, factory BackendFactory) {
	backendRegistry[name] = factory
}

// Options configures how the storage backend is opened
type Options struct {
	ReadOnly    bool
	LockTimeout time.Duration

	// Dolt server mode options (federation)
	ServerMode  bool          // Connect to dolt sql-server instead of embedded
	ServerHost  string        // Server host (default: 127.0.0.1)
	ServerPort  int           // Server port (default: 3307)
	ServerUser  string        // MySQL user (default: root)
	Database    string        // Database name for Dolt server mode (default: beads)
	OpenTimeout time.Duration // Advisory lock timeout for embedded dolt (0 = no lock)
}

// New creates a storage backend based on the backend type.
// For Dolt, path should be the directory containing the Dolt database.
func New(ctx context.Context, backend, path string) (storage.Storage, error) {
	return NewWithOptions(ctx, backend, path, Options{})
}

// NewWithOptions creates a storage backend with the specified options.
func NewWithOptions(ctx context.Context, backend, path string, opts Options) (storage.Storage, error) {
	// Default to dolt backend
	if backend == "" || backend == "sqlite" {
		backend = configfile.BackendDolt
	}
	if factory, ok := backendRegistry[backend]; ok {
		return factory(ctx, path, opts)
	}
	if backend == configfile.BackendDolt {
		return nil, fmt.Errorf("dolt backend requires CGO (not available on this build); install from pre-built binaries")
	}
	return nil, fmt.Errorf("unknown storage backend: %s (supported: dolt)", backend)
}

// NewFromConfig creates a storage backend based on the metadata.json configuration.
// beadsDir is the path to the .beads directory.
func NewFromConfig(ctx context.Context, beadsDir string) (storage.Storage, error) {
	return NewFromConfigWithOptions(ctx, beadsDir, Options{})
}

// NewFromConfigWithOptions creates a storage backend with options from metadata.json.
func NewFromConfigWithOptions(ctx context.Context, beadsDir string, opts Options) (storage.Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	backend := cfg.GetBackend()
	// Treat sqlite configs as dolt (migration complete)
	if backend == configfile.BackendSQLite {
		backend = configfile.BackendDolt
	}

	switch backend {
	case configfile.BackendDolt:
		// Merge Dolt server mode config into options (config provides defaults, opts can override)
		if cfg.IsDoltServerMode() {
			opts.ServerMode = true
			if opts.ServerHost == "" {
				opts.ServerHost = cfg.GetDoltServerHost()
			}
			if opts.ServerPort == 0 {
				opts.ServerPort = cfg.GetDoltServerPort()
			}
			if opts.ServerUser == "" {
				opts.ServerUser = cfg.GetDoltServerUser()
			}
			if opts.Database == "" {
				opts.Database = cfg.GetDoltDatabase()
			}
		}
		return NewWithOptions(ctx, backend, cfg.DatabasePath(beadsDir), opts)
	default:
		return nil, fmt.Errorf("unknown storage backend in config: %s", backend)
	}
}

// GetBackendFromConfig returns the backend type from metadata.json
func GetBackendFromConfig(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return configfile.BackendDolt
	}
	backend := cfg.GetBackend()
	// Treat sqlite as dolt (migration complete)
	if backend == "" || backend == configfile.BackendSQLite {
		return configfile.BackendDolt
	}
	return backend
}
