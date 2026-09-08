package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func newProxiedServerUOWProvider(ctx context.Context, beadsDir string) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}

	persisted, _ := configfile.Load(beadsDir)
	database := configfile.DefaultDoltDatabase
	if persisted != nil {
		database = persisted.GetDoltDatabase()
	}

	info, _ := configfile.LoadProxiedServerClientInfo(beadsDir)
	if info != nil && info.External != nil {
		return newExternalProxiedServerUOWProvider(ctx, beadsDir, database, info.External)
	}

	return newManagedProxiedServerUOWProvider(ctx, beadsDir, database)
}

func newExternalProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	external *configfile.ExternalDoltConfig,
) (uow.UnitOfWorkProvider, error) {
	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	if err := os.MkdirAll(rootPath, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: mkdir %s: %w", rootPath, err)
	}

	return uow.NewExternalDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		*external,
		external.ResolvedUser(),
		os.Getenv(configfile.ExternalDoltPasswordEnvVar),
	)
}

func newManagedProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
) (uow.UnitOfWorkProvider, error) {
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: dolt is not installed (not found in PATH); install from https://docs.dolthub.com/introduction/installation: %w", err)
	}

	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	configPath, err := ensureProxiedServerConfig(beadsDir)
	if err != nil {
		return nil, err
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	return uow.NewDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		configPath,
		proxy.BackendLocalServer,
		"root",
		"", // proxy is loopback-only, no auth
		doltBin,
	)
}
