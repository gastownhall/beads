package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// serveCmdName is the command name, shared with the root command's post-run
// policy (runsPostCommandMaintenance) so the exclusion cannot drift from the
// command it names.
const serveCmdName = "serve"

// providerCloseTimeout bounds the shutdown close of a provider serve built
// itself. It is not the drain budget — the server has already drained by then.
const providerCloseTimeout = 10 * time.Second

var (
	serveAddr             string
	serveAllowNonLoopback bool
)

var serveCmd = &cobra.Command{
	Use:   serveCmdName,
	Short: "Serve the beads HTTP API over loopback",
	Long: `Serve the beads HTTP API — the same work surface the CLI answers, for
automation clients that would otherwise fork a bd subprocess per call.

The wire contract is described by an OpenAPI document (/v0); GET
/v0/beads/context reports which operations this build actually implements.

DEPLOYMENT

  Pass an explicit port. The default 127.0.0.1:0 takes an ephemeral one, which
  is right for ad-hoc and test use — where the bound address printed on stdout
  is read immediately — but carries no mutual exclusion: two serves against one
  workspace then run side by side on different ports with no way to enumerate
  them. On a fixed port the second one fails to bind, which is the intended
  behavior. Concurrent serves are data-safe either way; claims are arbitrated
  in the SQL server.

  Run it under a supervisor. bd shuts down gracefully on SIGHUP as well as
  SIGINT and SIGTERM, so closing the terminal of a foreground bd serve stops it.

PROBES

  GET /healthz is LIVENESS only: it answers from the process and never touches
  the database, so it stays green while the database is unreachable. For
  readiness use GET /v0/beads/ready?limit=1 — a real query, where 200 means
  ready and 503 means live but not ready.

WHAT THIS DOES NOT DO

  No authentication and no TLS. The trust model is the loopback boundary, which
  is the same one the database behind it already relies on. --allow-non-loopback
  extends the surface to every network peer that can reach the address; nothing
  else about the server changes.

  Hooks do not fire. A hook is a user-controlled subprocess per mutation: in a
  concurrent server that is an unbounded latency multiplier and an orphaned
  child at shutdown, and its working-directory-derived hook lookup is
  meaningless in a server process. A CLI claim runs on_update; an HTTP claim
  does not.

  The per-command auto-commit machinery does not run. Durability is per request:
  a successful claim commits inside its own transaction, exactly as a proxied
  CLI claim does today.

  An actor on an HTTP request is caller-asserted provenance for the audit trail,
  not authenticated identity — the same thing it has always been on the CLI,
  where any local process can pass any --actor.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServe()
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:0",
		"Address to bind as IP:PORT; the host must be a numeric IP literal, and port 0 takes an ephemeral port")
	serveCmd.Flags().BoolVar(&serveAllowNonLoopback, "allow-non-loopback", false,
		"Permit a bind beyond loopback. bd serve has no authentication: every peer that can reach the address gets full read and claim access")
	rootCmd.AddCommand(serveCmd)
}

func runServe() error {
	// Flag validation first: it depends on nothing about the workspace, so the
	// refusal for a bad --addr is the same in every mode.
	if _, err := httpapi.ValidateBindAddr(serveAddr, serveAllowNonLoopback); err != nil {
		return HandleError("%v", err)
	}
	if err := serveModeGate(); err != nil {
		return HandleError("%v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return HandleError("cannot resolve working directory: %v", err)
	}
	info, err := contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(rootCtx)
	if err != nil {
		return HandleError("cannot resolve workspace context: %v", err)
	}

	provider := uowProvider
	if provider == nil {
		// Server, external-server and shared-server workspaces: PersistentPreRunE
		// builds a DoltStore for those and no unit-of-work provider, so serve
		// builds its own from the same connection settings the store used.
		//
		// That store stays open for the life of the process and serve never
		// touches it. Opening and closing it is the root command's business, not
		// a single command's, but it is not free either: its pool holds an idle
		// connection or two against the very server this process is about to
		// pool twenty more on. Worth knowing when sizing a shared Dolt server's
		// max_connections.
		topology, err := resolveServerModeUOWTopology(rootCtx, info.BeadsDir)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		// GET /v0/beads/context is the one endpoint automation is told to trust
		// for this server's identity, and its Database comes from metadata.json,
		// which knows nothing about --global. Report the database the provider
		// actually opened, or the handshake names one database while every
		// operation answers from another.
		info.Database = topology.database
		p, err := newSQLServerUOWProvider(rootCtx, info.BeadsDir, topology)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		defer func() {
			// By the time this runs the signal context is already canceled, so a
			// close that inherited it could not do any of its work.
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(rootCtx), providerCloseTimeout)
			defer cancel()
			if err := p.Close(closeCtx); err != nil {
				fmt.Fprintf(os.Stderr, "bd serve: closing the unit-of-work provider: %v\n", err)
			}
		}()
		provider = p
	}

	if serveAllowNonLoopback {
		fmt.Fprintf(os.Stderr,
			"bd serve: WARNING: --allow-non-loopback binds %s beyond loopback. "+
				"This API has no authentication and no TLS: any peer that can reach it can read every issue and claim work as any actor.\n",
			serveAddr)
	}

	srv, err := httpapi.Listen(httpapi.Config{
		Addr:             serveAddr,
		AllowNonLoopback: serveAllowNonLoopback,
		Provider:         provider,
		Workspace:        info,
		SchemaVersion:    JSONSchemaVersion,
		Mode:             serveResolvedMode(info),
	})
	if err != nil {
		return HandleError("%v", err)
	}

	// Graceful shutdown rides the signal context the root command already sets
	// up (SIGINT/SIGTERM/SIGHUP). A proxied provider is closed where every
	// proxied command closes it, in PersistentPostRunE — which in proxied mode
	// does nothing else; the provider serve built for a server-mode workspace is
	// closed by the defer above. Neither path runs the auto-commit, export or
	// push maintenance: proxied mode never had it, and server mode is excluded
	// from it by name (runsPostCommandMaintenance, cmd/bd/main.go).
	return srv.Serve(rootCtx)
}

// serveModeGate refuses the one workspace mode bd serve cannot answer for.
//
// Embedded is permanent, and the message is written to promise nothing: there
// is no unit-of-work provider for that backend, and there will not be one. Its
// commit protocol runs outside the SQL transaction on a separate connection, so
// the per-request atomicity this server's contract states would be a lie there
// even if a provider were written.
//
// Every mode that does have a SQL server behind it is served: proxied (managed
// or external), and — since bd-emv — server, external-server and shared-server,
// which build their provider in runServe.
//
// The mode belongs in the message, not in ErrUnsupported.Backend: that field is
// documented as a BACKEND name and is the embryo of the pluggable-backend error
// taxonomy, so putting a topology string in it would hand every downstream
// errors.As a mixed backend/mode vocabulary.
func serveModeGate() error {
	if isEmbeddedMode() {
		return errServeEmbedded()
	}
	return nil
}

// errServeEmbedded is the PERMANENT refusal. There is no unit-of-work provider
// for the embedded backend and there will not be one: the message says what the
// workspace is and what serve needs, and promises nothing further.
func errServeEmbedded() error {
	return fmt.Errorf("%w: bd serve requires a Dolt SQL server; this workspace uses embedded Dolt",
		&storage.ErrUnsupported{Op: "serve", Backend: "embedded-dolt"})
}

// serveResolvedMode labels the topology for the startup log line. Cosmetic —
// nothing dispatches on it — but the managed/external distinction is worth
// naming: an external dolt sql-server shares its max_connections budget with
// every other bd process pointed at it, and this server's pool is a claim on
// that budget.
func serveResolvedMode(info domain.ContextInfo) string {
	if !usesProxiedServer() {
		// Server, external-server and shared-server: serve fronts the running
		// dolt sql-server rather than starting one, so from this process the
		// server is external even when Beads is what started it.
		return info.DoltMode + " (external dolt)"
	}
	client, err := configfile.LoadProxiedServerClientInfo(info.BeadsDir)
	if err == nil && client != nil && client.External != nil {
		return info.DoltMode + " (external dolt)"
	}
	return info.DoltMode + " (managed dolt)"
}
