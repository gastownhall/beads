package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

var (
	dbProxyChildRoot                string
	dbProxyChildPort                int
	dbProxyChildIdleTimeout         time.Duration
	dbProxyChildBackend             string
	dbProxyChildConfig              string
	dbProxyChildLogPath             string
	dbProxyChildDoltBin             string
	dbProxyChildExternalHost        string
	dbProxyChildExternalPort        int
	dbProxyChildExternalSocketPath  string
	dbProxyChildExternalTLS         bool
	dbProxyChildExternalTLSCertPath string
	dbProxyChildExternalTLSKeyPath  string
	dbProxyChildExternalKeepAlive   time.Duration
	dbProxyChildPoolSize            int
	dbProxyChildBackendUser         string
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
			TLSRequired:     dbProxyChildExternalTLS,
			TLSCert:         dbProxyChildExternalTLSCertPath,
			TLSKey:          dbProxyChildExternalTLSKeyPath,
			KeepAlivePeriod: dbProxyChildExternalKeepAlive,
		}

		srv, err := newDatabaseServer(backend, dbProxyChildRoot, dbProxyChildConfig, dbProxyChildLogPath, dbProxyChildDoltBin, external)
		if err != nil {
			return err
		}

		// Backend credentials for pooled connections. The user arrives via
		// flag (default root); the password is read from the environment so it
		// never appears on the command line. Only the external backend has a
		// non-empty password; the managed loopback server uses root with no
		// password.
		backendPassword := ""
		if backend == proxy.BackendExternal || backend == proxy.BackendLocalSharedServer {
			backendPassword = os.Getenv(configfile.ExternalDoltPasswordEnvVar)
		}

		p := proxy.NewProxyServer(proxy.ProxyOpts{
			RootDir:         dbProxyChildRoot,
			Port:            dbProxyChildPort,
			IdleTimeout:     dbProxyChildIdleTimeout,
			Server:          srv,
			PoolSize:        dbProxyChildPoolSize,
			BackendUser:     dbProxyChildBackendUser,
			BackendPassword: backendPassword,
		})
		if err := p.ListenAndServe(cmd.Context()); err != nil {
			if errors.Is(err, proxy.ErrLockHeld) {
				os.Exit(proxy.LockHeldExitCode)
			}
			return err
		}
		return nil
	},
}

func newDatabaseServer(backend proxy.Backend, rootDir, configPath, logPath, doltBin string, external configfile.ExternalDoltConfig) (server.DatabaseServer, error) {
	switch backend {
	case proxy.BackendLocalServer:
		return server.NewDoltServer(doltBin, rootDir, configPath, logPath, 0)
	case proxy.BackendExternal, proxy.BackendLocalSharedServer:
		// The shared backend fronts the managed dolt through the same external
		// server. The N+1 → 1 collapse comes from pointing every scope at one
		// shared rootDir (so the parent's spawn-or-reuse yields a single child),
		// not from a distinct server type.
		return server.NewExternalDoltServer(external)
	}
	return nil, fmt.Errorf("unknown backend %q", backend)
}

func init() {
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildRoot, "root", "", "root directory holding proxy.lock, proxy.pid, proxy.log")
	dbProxyChildCmd.Flags().IntVar(&dbProxyChildPort, "port", 0, "port to listen on")
	dbProxyChildCmd.Flags().DurationVar(&dbProxyChildIdleTimeout, "idle-timeout", 30*time.Second, "idle timeout before shutdown (0 disables)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildBackend, "backend", "",
		"backend kind: "+strings.Join(proxy.KnownBackendNames(), " | "))
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildConfig, "config", "", "path to backend server config (e.g. dolt sql-server YAML)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildLogPath, "logpath", "", "path the backend server should write its stdout/stderr to")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildDoltBin, "dolt-bin", "", "path to the dolt executable")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalHost, "external-host", "", "external backend: hostname or IP of the dolt sql-server")
	dbProxyChildCmd.Flags().IntVar(&dbProxyChildExternalPort, "external-port", 0, "external backend: TCP port of the dolt sql-server")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalSocketPath, "external-socket-path", "", "external backend: absolute path to a unix domain socket (overrides host/port)")
	dbProxyChildCmd.Flags().BoolVar(&dbProxyChildExternalTLS, "external-tls", false, "external backend: require TLS in the MySQL handshake")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalTLSCertPath, "external-tls-cert-path", "", "external backend: absolute path to client TLS certificate (mTLS)")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildExternalTLSKeyPath, "external-tls-key-path", "", "external backend: absolute path to client TLS private key (mTLS)")
	dbProxyChildCmd.Flags().DurationVar(&dbProxyChildExternalKeepAlive, "external-keep-alive", 0, "external backend: TCP keepalive period (default 30s)")
	dbProxyChildCmd.Flags().IntVar(&dbProxyChildPoolSize, "pool-size", 0, "if >0, pool up to N warm authenticated backend connections instead of dialing one per client")
	dbProxyChildCmd.Flags().StringVar(&dbProxyChildBackendUser, "backend-user", "root", "user the proxy authenticates pooled backend connections as")
	_ = dbProxyChildCmd.MarkFlagRequired("root")
	_ = dbProxyChildCmd.MarkFlagRequired("port")
	_ = dbProxyChildCmd.MarkFlagRequired("backend")
	rootCmd.AddCommand(dbProxyChildCmd)
}
