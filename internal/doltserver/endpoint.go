package doltserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Endpoint is the Dolt SQL server a workspace's store dials, and the
// credentials it presents there. ResolveEndpoint resolves it by the store's
// own rules, so a caller that dials, probes, reports or clones into a
// workspace's server outside the store's open (and the open itself, through
// dolt.FillServerEndpoint) sees the host, port and password the open uses,
// and never a server other than the one later commands dial.
//
// The invariant it carries:
//
//   - The port is DialConfig's, the store's own rule (the legacy
//     BEADS_DOLT_PORT included), and PortSource records where it came from.
//   - The password is BEADS_DOLT_PASSWORD, else the credentials-file entry
//     for exactly [Host:Port]. It is looked up only when a caller asks for
//     it (Password, DialDSN), so always for the final host and port, and a
//     caller that only names the endpoint never reads the credentials file.
//   - The identity is the store's: a configured
//     BEADS_DOLT_CREDENTIAL_COMMAND's token (a gateway) over the static
//     user. It is resolved only for a dial (DialDSN, WithIdentity), since it
//     runs a command.
//   - The transport is the store's: a live unix socket first, else TCP
//     (SocketTransport).
//   - An endpoint with neither a port nor a socket is never dialed: there is
//     no server at 127.0.0.1:0, and no default port stands in for it.
//     UnresolvedAdvice says what to do instead.
type Endpoint struct {
	// BeadsDir is the workspace the endpoint was resolved for.
	BeadsDir string
	// Host is the configured host, as configured: an IPv6 literal may be
	// bracketed. doltutil.ServerAddr and the credentials lookup take either
	// spelling.
	Host string
	// Port is the port the store dials, or 0 when no source resolved one.
	Port             int
	PortSource       PortSource
	PortSharedServer bool
	// Mode is the server's ownership mode (ResolveServerMode).
	Mode   ServerMode
	Socket string
	// User is the static user (BEADS_DOLT_SERVER_USER, metadata.json, root).
	// When CredentialCommand is set the store presents that command's token
	// instead (WithIdentity), unless the caller named a user (WithUser).
	User string
	// password is a password the caller named (WithPassword); passwordSet
	// says it was. Otherwise Password looks it up for Host and Port.
	password    string
	passwordSet bool
	TLS         bool
	// AllowCleartextPassword is dolt_server_allow_cleartext_password
	// (BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD) as configured, with no TLS
	// gate; CleartextAuth pairs it with TLS and refuses it without.
	AllowCleartextPassword bool
	Database               string
	// CredentialCommand is BEADS_DOLT_CREDENTIAL_COMMAND, the gateway
	// credential the store runs for its user.
	CredentialCommand string
	// Gateway reports that User is a CredentialCommand token (WithIdentity).
	Gateway bool
	// SharedServer and AutoStartDisabled are IsSharedServerMode and
	// IsAutoStartDisabled under the configuration ResolveEndpoint ran with,
	// so a caller that resolved another workspace's endpoint under
	// config.WithBeadsDir classifies it by that workspace's settings.
	SharedServer      bool
	AutoStartDisabled bool
}

// ResolveEndpoint resolves the endpoint beadsDir's store dials, with fileCfg
// as its metadata.json (loaded from beadsDir when nil; an empty config, whose
// getters still read the environment, when there is none). The port and its
// source are DialConfig's, the host, socket, user, TLS and database fileCfg's
// getters', and the password the one for that host and port.
//
// Like DialConfig it reads config.yaml through the configuration loaded now;
// a caller resolving a workspace other than the active one wraps the call in
// config.WithBeadsDir.
func ResolveEndpoint(beadsDir string, fileCfg *configfile.Config) Endpoint {
	if fileCfg == nil {
		fileCfg = loadEndpointConfig(beadsDir)
	}
	return endpointFor(beadsDir, fileCfg, DialConfig(beadsDir))
}

// endpointFor is the endpoint fileCfg names with dial's port and mode.
func endpointFor(beadsDir string, fileCfg *configfile.Config, dial *Config) Endpoint {
	ep := Endpoint{
		BeadsDir:               beadsDir,
		Host:                   fileCfg.GetDoltServerHost(),
		Port:                   dial.Port,
		PortSource:             dial.PortSource,
		PortSharedServer:       dial.PortSharedServer,
		Mode:                   dial.Mode,
		Socket:                 fileCfg.GetDoltServerSocket(),
		User:                   fileCfg.GetDoltServerUser(),
		TLS:                    fileCfg.GetDoltServerTLS(),
		Database:               fileCfg.GetDoltDatabase(),
		AllowCleartextPassword: fileCfg.GetDoltServerAllowCleartextPassword(),
		CredentialCommand:      fileCfg.GetDoltCredentialCommand(),
		SharedServer:           IsSharedServerMode(),
		AutoStartDisabled:      IsAutoStartDisabled(),
	}
	return ep
}

// loadEndpointConfig is beadsDir's metadata.json, or an empty config when
// there is none or it does not load. It stats first so a legacy config.json
// is not migrated as a side effect of resolving an endpoint.
func loadEndpointConfig(beadsDir string) *configfile.Config {
	if beadsDir != "" {
		if _, err := os.Stat(configfile.ConfigPath(beadsDir)); err == nil {
			if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
				return cfg
			}
		}
	}
	return &configfile.Config{}
}

// endpointPassword is the password for the server at host and port. An
// endpoint with no port has no [host:port] credentials-file entry, so only
// BEADS_DOLT_PASSWORD applies to it.
func endpointPassword(host string, port int) string {
	if port <= 0 {
		return os.Getenv("BEADS_DOLT_PASSWORD")
	}
	return configfile.ServerPasswordFor(host, port)
}

// Password is the password the store presents at e: one the caller named
// (WithPassword), else BEADS_DOLT_PASSWORD, else the credentials-file entry
// for exactly [Host:Port]. It is looked up now, for e's final host and port,
// so it is never the entry for a port a later rule replaces.
func (e Endpoint) Password() string {
	if e.passwordSet {
		return e.password
	}
	return endpointPassword(e.Host, e.Port)
}

// WithPassword is e presenting password, one the caller named, wherever it
// dials.
func (e Endpoint) WithPassword(password string) Endpoint {
	e.password, e.passwordSet = password, true
	return e
}

// WithHost is e at host, for a caller that dials a host of its own (a store
// config with ServerHost already set). Its password is then the one for host.
func (e Endpoint) WithHost(host string) Endpoint {
	e.Host = host
	return e
}

// AtPort is e at port, resolved from source, for a caller whose own port
// order decides the port (bd init's store). Its password is then the one for
// that port.
func (e Endpoint) AtPort(port int, source PortSource) Endpoint {
	e.Port, e.PortSource = port, source
	if source != PortSourceSharedServerDefault {
		e.PortSharedServer = false
	}
	return e
}

// WithUser is e as user, a user the caller named (bd init --server-user),
// which the store presents over the credential command's token, as
// ApplyGatewayCredential leaves a preset user alone.
func (e Endpoint) WithUser(user string) Endpoint {
	e.User, e.CredentialCommand, e.Gateway = user, "", false
	return e
}

// Resolved reports whether a source resolved a port. A socket-only endpoint
// has none and is still Dialable.
func (e Endpoint) Resolved() bool { return e.Port > 0 }

// Dialable reports whether e has an address to dial: a resolved port or a
// unix socket, as the store's open requires (unresolvedServerPortError).
func (e Endpoint) Dialable() bool { return e.Port > 0 || e.Socket != "" }

// Addr is host:port, bracketing an IPv6 host.
func (e Endpoint) Addr() string { return doltutil.ServerAddr(e.Host, e.Port) }

// Location names e's server for a message: host:port, else its socket for a
// socket-only endpoint, else the host and the fact that no port resolved,
// never host:0.
func (e Endpoint) Location() string {
	switch {
	case e.Port > 0:
		return e.Addr()
	case e.Socket != "":
		return "unix:" + e.Socket
	}
	return e.Host + " (no port resolved)"
}

// WithIdentity is e with the user the store presents: the credential
// command's token when one is configured and no user was named (WithUser),
// marking e a gateway; else e unchanged. It fails closed, as the store does:
// a configured command that fails never falls back to the static user.
func (e Endpoint) WithIdentity(ctx context.Context) (Endpoint, error) {
	if e.CredentialCommand == "" || e.Gateway {
		return e, nil
	}
	token, ok, err := GatewayIdentity(ctx, e.CredentialCommand)
	if err != nil {
		return e, err
	}
	if ok {
		e.User, e.Gateway = token, true
	}
	return e, nil
}

// CleartextAuth is whether a dial to e may answer a mysql_clear_password
// auth request: AllowCleartextPassword, refused without TLS
// (configfile.ValidateServerAuthConfig owns the refusal text), so no caller
// that dials through the endpoint sends a password in the clear.
func (e Endpoint) CleartextAuth() (bool, error) {
	if err := configfile.ValidateServerAuthConfig(e.AllowCleartextPassword, e.TLS); err != nil {
		return false, err
	}
	return e.AllowCleartextPassword, nil
}

// DialDSN is the connection to e the store would make, on database ("" for
// none): its transport (SocketTransport), the identity it presents
// (WithIdentity) and the password keyed to its port. It refuses an endpoint
// with no address (UnresolvedError) rather than dial host:0. Callers set a
// Timeout on it as they need.
func (e Endpoint) DialDSN(ctx context.Context, database string) (doltutil.ServerDSN, error) {
	if err := e.UnresolvedError(); err != nil {
		return doltutil.ServerDSN{}, err
	}
	allowCleartext, err := e.CleartextAuth()
	if err != nil {
		return doltutil.ServerDSN{}, err
	}
	id, err := e.WithIdentity(ctx)
	if err != nil {
		return doltutil.ServerDSN{}, fmt.Errorf("resolving dolt credential command: %w", err)
	}
	return doltutil.ServerDSN{
		Socket:                  e.Transport(),
		Host:                    e.Host,
		Port:                    e.Port,
		User:                    id.User,
		Password:                e.Password(),
		Database:                database,
		TLS:                     e.TLS,
		AllowCleartextPasswords: allowCleartext,
	}, nil
}

// Transport is the unix socket the store dials e over, or "" for TCP: its
// socket-first / TCP-fallback policy (SocketTransport) with its own probe
// budget. It probes a configured socket, so resolve it only for a dial. A
// caller that builds its own DSN, keeping an identity of its own, takes the
// store's transport from it; DialDSN does.
func (e Endpoint) Transport() string {
	return SocketTransport(e.Socket, e.Host, e.Port, socketProbeTimeout)
}

// Probe reports whether e's server answers the MySQL handshake within
// timeout, over the transport the store would dial (SocketTransport): its
// live socket, else TCP. An endpoint with no address is UnresolvedError.
func (e Endpoint) Probe(timeout time.Duration) error {
	if err := e.UnresolvedError(); err != nil {
		return err
	}
	if socket := SocketTransport(e.Socket, e.Host, e.Port, timeout); socket != "" {
		return socketDialProbe("unix", socket, timeout)
	}
	return socketDialProbe("tcp", e.Addr(), timeout)
}

// socketProbeTimeout is how long SocketTransport waits on each probe, the
// store's own budget.
const socketProbeTimeout = 500 * time.Millisecond

// socketDialProbe reports whether network/addr accepts a MySQL connection.
// A var so tests can stub connectivity.
var socketDialProbe = func(network, addr string, timeout time.Duration) error {
	_, err := ProbeSQLServer(network, addr, timeout)
	return err
}

// SocketTransport is the store's socket-first / TCP-fallback policy: the
// unix socket to dial, or "" for TCP. A configured socket is used when it
// is live; when it is not and TCP at host:port is, TCP; when neither is,
// the socket, so the error names the socket the workspace configured.
func SocketTransport(socket, host string, port int, timeout time.Duration) string {
	return SocketTransportWith(socketDialProbe, socket, host, port, timeout)
}

// SocketTransportWith is SocketTransport with probe as its dial probe.
func SocketTransportWith(probe func(network, addr string, timeout time.Duration) error, socket, host string, port int, timeout time.Duration) string {
	if socket == "" {
		return ""
	}
	if probe("unix", socket, timeout) == nil {
		return socket // socket is live — keep using it
	}
	if port > 0 && probe("tcp", doltutil.ServerAddr(host, port), timeout) == nil {
		debug.Logf("dolt: socket %s unreachable, falling back to TCP %s\n", socket, doltutil.ServerAddr(host, port))
		return "" // socket down but TCP up — transparently fall back to TCP
	}
	return socket // both down (or no TCP port) — keep socket for the error path
}

// GatewayIdentity runs the server credential command (command, from
// BEADS_DOLT_CREDENTIAL_COMMAND) for the token an authenticating gateway
// server takes as the connection username. ok is false when no command is
// configured. It fails closed on a command that fails, a credential that is
// not an identity, and a token the DSN user field cannot carry. It is the
// one place the token is resolved: the store (dolt.ApplyGatewayCredential)
// and a dial through the endpoint (Endpoint.WithIdentity) present the same
// identity.
func GatewayIdentity(ctx context.Context, command string) (token string, ok bool, err error) {
	cred, ok, err := creds.ResolveLadder(ctx, creds.CommandSource{
		Command: command,
		Kind:    creds.KindIdentity,
		Label:   "BEADS_DOLT_CREDENTIAL_COMMAND",
	})
	if err != nil || !ok {
		return "", false, err
	}
	// Defense in depth: the token is presented AS the username, so a non-identity
	// credential must never reach this slot.
	if cred.Kind != creds.KindIdentity {
		return "", false, fmt.Errorf("dolt: credential from %s is not an identity; refusing to present it as the connection username", cred.Source)
	}
	// The token becomes the DSN username; the go-sql-driver grammar has no escaping for
	// the user field, so a ':' '@' or '/' would silently mis-split it into user/password.
	// Reject rather than connect with a mangled identity. (JWTs are base64url + '.', safe.)
	if strings.ContainsAny(cred.Value, ":@/") {
		return "", false, fmt.Errorf("dolt: credential from %s contains a character (:, @, or /) that cannot be placed in the connection username", cred.Source)
	}
	return cred.Value, true, nil
}

// hostRunsBDServer reports whether a server bd runs could be at e's host
// (HostRunsBDServer).
func (e Endpoint) hostRunsBDServer() bool {
	return HostRunsBDServer(e.Host)
}

// HostRunsBDServer reports whether a server bd runs could be at host:
// configfile.BdRunsServerAt, 127.0.0.1, the one address bd's own server
// listens on, in any spelling a dial reaches it by. A server at any other host
// (another machine, the IPv6 loopback in any spelling, or another loopback
// address) is one some other process runs, whatever bd's mode settings say.
// It is the store's own classification (dolt.isExternalServerHost is its
// negation).
func HostRunsBDServer(host string) bool {
	return configfile.BdRunsServerAt(host)
}

// ManagedElsewhere reports a server that something other than bd runs,
// whatever its host and mode: a gateway, whose credential command turns the
// store's auto-start off (dolt.ApplyGatewayCredential), or a unix socket,
// which bd's own server never listens on (AutoStartReaches).
func (e Endpoint) ManagedElsewhere() bool {
	return e.CredentialCommand != "" || e.Gateway || e.Socket != ""
}

// ServedByBD reports whether the server at e is one bd runs: the shared
// server, or bd's own server for this workspace (a local host and a mode
// that is not externally managed). A server some other process manages, on
// this machine or another, behind a gateway or on a unix socket, is not.
func (e Endpoint) ServedByBD() bool {
	if e.ManagedElsewhere() {
		return false
	}
	if e.PortSharedServer || e.SharedServer {
		return true
	}
	return e.hostRunsBDServer() && e.Mode != ServerModeExternal
}

// BDStartsServer reports whether a bd command that opens the store starts
// e's server when none is running. It is the store's own rule
// (dolt.serverOpenCanAutoStart under dolt.ApplyCLIAutoStart): auto-start is
// on, no credential command is configured, the mode is not externally
// managed (shared-server mode is), and the dial is TCP to a host bd's server
// listens on (AutoStartReaches). Only then is "run any bd command to
// auto-start" true, and only then is a workspace with no resolved port not a
// misconfiguration.
func (e Endpoint) BDStartsServer() bool {
	return !e.AutoStartDisabled && !e.ManagedElsewhere() &&
		e.Mode != ServerModeExternal && AutoStartReaches(e.Host, e.Socket)
}

// AutoStartReaches reports whether a server the store's auto-start starts
// is one a dial to host over socket reaches: TCP only, since that server
// listens on TCP and never on a unix socket, and a host bd runs its server
// at (HostRunsBDServer), since it listens on 127.0.0.1 only. The store's
// open (dolt.serverOpenCanAutoStart) and Endpoint.BDStartsServer both ask
// it, so a caller that acts on "a bd command starts this server" (bd
// bootstrap with no port resolved) never counts on an auto-start the open
// will not make.
func AutoStartReaches(host, socket string) bool {
	return socket == "" && HostRunsBDServer(host)
}

// UnresolvedAdvice is what to do about e when it has no address (no port
// resolved and no socket), or "" when it has one. It is the store's own
// remedy for an open with no port (see UnresolvedPortRemedy), so a
// caller that refuses to dial port 0 says what the open would: the
// store calls a server it will not start, because its mode is external or a
// credential command turned auto-start off, externally managed.
func (e Endpoint) UnresolvedAdvice() string {
	if e.Dialable() {
		return ""
	}
	return UnresolvedPortRemedy(e.Host, e.portFile(), e.AutoStartDisabled,
		e.Mode == ServerModeExternal || e.ManagedElsewhere())
}

// portFile is where the server at e publishes its port: the shared server's
// port file in shared-server mode, else the workspace's.
func (e Endpoint) portFile() string {
	dir := e.BeadsDir
	if e.SharedServer {
		if shared, err := SharedServerPath(); err == nil {
			dir = shared
		}
	}
	return filepath.Join(dir, PortFileName)
}

// ErrUnresolvedPort is wrapped by the error for an endpoint no port source
// resolved a port for (Endpoint.UnresolvedError, and the store's refusal to
// open, dolt.ErrUnresolvedServerPort): there is nothing to dial.
var ErrUnresolvedPort = errors.New("no Dolt server port resolved")

// UnresolvedError is the error for an endpoint with no address (no resolved
// port and no socket), or nil when it has one: ErrUnresolvedPort, the host, and UnresolvedAdvice. Callers
// return it instead of dialing host:0.
func (e Endpoint) UnresolvedError() error {
	if e.Dialable() {
		return nil
	}
	return fmt.Errorf("%w (host %s).\n%s", ErrUnresolvedPort, remedy.PrintedValue(e.Host), e.UnresolvedAdvice())
}

// UnresolvedPortRemedy is the remedy paragraph for a Dolt server at host with
// no resolved port, shared by the store's refusal to open
// (unresolvedServerPortError) and Endpoint.UnresolvedAdvice. portFile is
// the port file whatever runs the server publishes its port in.
//
// A server bd does not run (HostRunsBDServer), on another machine or at the
// IPv6 loopback, gets UnresolvedPortAdvice: pin the port. One on this
// machine that bd will not start, because auto-start is off or the server is
// externally managed, must publish its port again or have it pinned.
// Otherwise bd starts the server:
// a command that opens the store does, as does `bd dolt start`. It never
// offers a default port, since on a shared machine whatever listens there may
// belong to another project (GH#2098, GH#2372).
func UnresolvedPortRemedy(host, portFile string, autoStartDisabled, externallyManaged bool) string {
	const pinPort = "BEADS_DOLT_SERVER_PORT or dolt.port in .beads/config.yaml"
	var b strings.Builder
	switch {
	case !HostRunsBDServer(host):
		b.WriteString(UnresolvedPortAdvice(host))
	case autoStartDisabled || externallyManaged:
		if autoStartDisabled {
			b.WriteString("dolt.auto-start is off, so bd does not start this workspace's Dolt server.\n")
		} else {
			b.WriteString("This workspace's Dolt server is externally managed, so bd does not start it.\n")
		}
		fmt.Fprintf(&b, "Whatever manages the server must publish its port again (normally by writing %s), "+
			"or pin the port with %s.", portFile, pinPort)
	default:
		b.WriteString("This command does not start a Dolt server. Run a bd command that can start one, " +
			"or start it with:\n  bd dolt start")
	}
	return b.String()
}
