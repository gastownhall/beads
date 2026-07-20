package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

var (
	dbProxyChildRoot               string
	dbProxyChildPort               int
	dbProxyChildIdleTimeout        time.Duration
	dbProxyChildBackend            string
	dbProxyChildConfig             string
	dbProxyChildLogPath            string
	dbProxyChildDoltBin            string
	dbProxyChildDatabase           string
	dbProxyChildExternalHost       string
	dbProxyChildExternalPort       int
	dbProxyChildExternalSocketPath string
	dbProxyChildExternalKeepAlive  time.Duration
)

var dbProxyChildCmd = &cobra.Command{
	Use:    "db-proxy-child",
	Hidden: true,
	Short:  "Internal: run as the database proxy child process",
	Long: `db-proxy-child runs the long-lived per-rootDir TCP proxy that fronts a
DatabaseServer. It is spawned by the parent bd process via fork+exec and is
not intended to be invoked directly by users.`,

	PersistentPreRun:  func(cmd *cobra.Command, args []string) {},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {},

	RunE: func(cmd *cobra.Command, _ []string) error {
		backend := proxy.Backend(dbProxyChildBackend)
		if err := backend.Validate(); err != nil {
			return err
		}

		external := configfile.ExternalDoltConfig{
			Host:            dbProxyChildExternalHost,
			Port:            dbProxyChildExternalPort,
			Socket:          dbProxyChildExternalSocketPath,
			KeepAlivePeriod: dbProxyChildExternalKeepAlive,
		}

		srv, err := newDatabaseServer(backend, dbProxyChildRoot, dbProxyChildConfig, dbProxyChildLogPath, dbProxyChildDoltBin, dbProxyChildDatabase, external)
		if err != nil {
			return err
		}
		defer func() { _ = srv.Stop(context.Background()) }()

		p := proxy.NewProxyServer(proxy.ProxyOpts{
			RootDir:     dbProxyChildRoot,
			Port:        dbProxyChildPort,
			IdleTimeout: dbProxyChildIdleTimeout,
			Server:      srv,
		})
		if err := p.ListenAndServe(cmd.Context()); err != nil {
			if errors.Is(err, proxy.ErrLockHeld) {
				return &exitError{Code: proxy.LockHeldExitCode}
			}
			return err
		}
		return nil
	},
}

func newDatabaseServer(backend proxy.Backend, rootDir, configPath, logPath, doltBin, database string, external configfile.ExternalDoltConfig) (server.DatabaseServer, error) {
	switch backend {
	case proxy.BackendLocalServer:
		return server.NewDoltServer(doltBin, rootDir, configPath, logPath, 0, database)
	case proxy.BackendExternal:
		return server.NewExternalDoltServer(external)
	case proxy.BackendLocalSharedServer:
		return nil, fmt.Errorf("backend %q: not yet implemented", backend)
	}
	return nil, fmt.Errorf("unknown backend %q", backend)
}

func init() {
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildRoot, "root", "", "root directory holding proxy.lock, proxy.pid, proxy.log")
	dbProxyChildCmd.Flags().IntVar(&dbProxyChildPort, "port", 0, "port to listen on")
	dbProxyChildCmd.Flags().DurationVar(&dbProxyChildIdleTimeout, "idle-timeout", 0, "idle timeout before shutdown (0 or negative = never shut down)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildBackend, "backend", "",
		"backend kind: "+strings.Join(proxy.KnownBackendNames(), " | "))
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildConfig, "config", "", "path to backend server config (e.g. dolt sql-server YAML)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildLogPath, "logpath", "", "path the backend server should write its stdout/stderr to")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildDoltBin, "dolt-bin", "", "path to the dolt executable")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildDatabase, "database", "", "database to select when running shutdown maintenance (local-server backend)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalHost, "external-host", "", "external backend: hostname or IP of the dolt sql-server")
	dbProxyChildCmd.Flags().IntVar(&dbProxyChildExternalPort, "external-port", 0, "external backend: TCP port of the dolt sql-server")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalSocketPath, "external-socket-path", "", "external backend: absolute path to a unix domain socket (overrides host/port)")
	dbProxyChildCmd.Flags().DurationVar(&dbProxyChildExternalKeepAlive, "external-keep-alive", 0, "external backend: TCP keepalive period (default 30s)")
	_ = dbProxyChildCmd.MarkFlagRequired("root")
	_ = dbProxyChildCmd.MarkFlagRequired("port")
	_ = dbProxyChildCmd.MarkFlagRequired("backend")
	rootCmd.AddCommand(dbProxyChildCmd)
}
