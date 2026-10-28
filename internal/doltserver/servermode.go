package doltserver

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// ServerMode describes who owns and manages the dolt sql-server lifecycle.
type ServerMode int

const (
	// ServerModeOwned means beads auto-starts and manages the server.
	// This is the default for standalone users with no explicit port config.
	ServerModeOwned ServerMode = iota

	// ServerModeExternal means the user manages the server lifecycle
	// (e.g., systemd, Docker, Hosted Dolt, VPS). Beads never starts or
	// stops the server. Determined when metadata.json has an explicit
	// dolt_server_port or BEADS_DOLT_SHARED_SERVER is set.
	ServerModeExternal

	// ServerModeEmbedded is the legacy in-process embedded dolt path.
	// Determined when metadata.json dolt_mode is "embedded".
	ServerModeEmbedded
)

// String returns a human-readable label for the server mode.
func (m ServerMode) String() string {
	switch m {
	case ServerModeOwned:
		return "owned"
	case ServerModeExternal:
		return "external"
	case ServerModeEmbedded:
		return "embedded"
	default:
		return fmt.Sprintf("ServerMode(%d)", int(m))
	}
}

// ResolveServerMode determines the server mode from the given beadsDir.
// This is the single source of truth for how the server lifecycle is managed.
//
// Decision logic (checked in order):
//  1. BEADS_DOLT_SERVER_MODE=1 env var             -> ServerModeExternal
//  2. BEADS_DOLT_SHARED_SERVER env var is set       -> ServerModeExternal
//     2b. host-based inference (HostImpliesServerMode) -> ServerModeExternal
//     2c. BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT env var -> ServerModeExternal
//  3. metadata.json dolt_mode == "embedded"         -> ServerModeEmbedded
//  4. metadata.json has explicit dolt_server_port   -> ServerModeExternal
//  5. default                                       -> ServerModeOwned
//
// Runtime env vars (1, 2, 2c) take precedence over persisted metadata.json
// to prevent stale dolt_mode=embedded from silently overriding an active
// shared-server, server-mode, or env-supplied-port configuration (GH#2949).
//
// The function loads metadata.json only if the file exists, to avoid
// triggering the legacy config.json -> metadata.json migration side effect.
func ResolveServerMode(beadsDir string) ServerMode {
	return ResolveServerModeForMode(beadsDir, IsSharedServerMode())
}

// ResolveServerModeForMode resolves lifecycle ownership for an
// already-classified target. BEADS_DOLT_SERVER_MODE remains authoritative;
// shared-server state comes from the caller's target classification rather
// than the active workspace.
func ResolveServerModeForMode(beadsDir string, sharedMode bool) ServerMode {
	return resolveServerMode(beadsDir, sharedMode, true)
}

// ResolveServerModeIgnoringPortEnv is ResolveServerMode with check 2c
// (BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT) skipped.
//
// Those env vars are also set ambiently on multi-agent rigs purely to route
// bd's own client connections to a shared coordination server; setting them
// does not disclaim beads' ownership of beadsDir's own local dolt server
// process the way checks 1/2/2b (and metadata.json check 4) genuinely do.
// Callers asking "should I auto-start a server" or "which server should I
// talk to" want the full ResolveServerMode — an ambient port var correctly
// means "route there instead of spawning a redundant one". Callers asking
// "am I allowed to manage/kill OS processes for this directory's server
// lifecycle" — killStaleServersForDir's orphan-cleanup guard (GH#2430) —
// must use this instead, or the ambient routing var falsely disables
// cleanup for a directory beads still owns.
//
// Exported for internal/storage/dolt's sharedServerDatabase (GH#6169):
// once ManagesLiveServerOnPort has PROVEN bd started and tracks the live
// server on cfg.ServerPort for cfg.BeadsDir, an ambient
// BEADS_DOLT_SERVER_PORT/BEADS_DOLT_PORT set purely for multi-agent
// connection routing must not override that proof and reclassify bd's own
// server as shared -- the same reasoning as the kill-guard above, applied
// to the migrate-on-open decision instead of the process-reaping one.
func ResolveServerModeIgnoringPortEnv(beadsDir string) ServerMode {
	return resolveServerMode(beadsDir, IsSharedServerMode(), false)
}

// resolveServerMode is the shared implementation behind ResolveServerMode,
// ResolveServerModeForMode and ResolveServerModeIgnoringPortEnv. sharedMode
// is the target's shared-server classification (check 2). honorPortEnv
// controls whether check 2c (BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT)
// participates; see ResolveServerModeIgnoringPortEnv for why a caller would
// want it excluded.
//
// ExternalPortSetting's Sole repeats checks 1, 2b, 2c and 4b to decide whether
// removing dolt_server_port lets bd manage the server; a new check here that
// returns ServerModeExternal must be added there too.
func resolveServerMode(beadsDir string, sharedMode, honorPortEnv bool) ServerMode {
	// 1. BEADS_DOLT_SERVER_MODE=1 env var -> external (explicit server mode)
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
		return ServerModeExternal
	}

	if sharedMode {
		return ServerModeExternal
	}

	var fileCfg *configfile.Config

	// Only load config if metadata.json exists (avoids legacy migration side effect)
	metadataPath := configfile.ConfigPath(beadsDir)
	if _, err := os.Stat(metadataPath); err == nil {
		if cfg, loadErr := configfile.Load(beadsDir); loadErr == nil && cfg != nil {
			fileCfg = cfg
		}
	}

	// 2b. Host-based inference (GH#3545) -> external. Uses the same
	// centralized rule as IsDoltServerMode (explicit-mode gates, env
	// suppression, effective-host precedence, proxied exemption) so the
	// storage-mode and lifecycle resolvers cannot disagree: a server
	// that lives on another machine cannot have a bd-owned lifecycle.
	// Runs before the embedded-metadata check because a runtime env
	// host beats stale metadata (GH#2949).
	hostCfg := fileCfg
	if hostCfg == nil {
		hostCfg = &configfile.Config{}
	}
	if hostCfg.HostImpliesServerMode() {
		return ServerModeExternal
	}

	// 2c. Explicit server port via env var (BEADS_DOLT_SERVER_PORT, then
	// the legacy BEADS_DOLT_PORT) -> external. Mirrors the deprecated
	// GetDoltServerPort()'s own env precedence: the orchestrator sets
	// these to signal a server address it manages, port included, so
	// ResolveServerMode must agree instead of defaulting a
	// port-only deployment to owned (ensureDoltInit would never run for
	// it). Runs before the metadata.json checks for the same GH#2949
	// reason as 1/2/2b: a runtime env var beats stale persisted metadata.
	//
	// Skipped when honorPortEnv is false — see resolveServerModeIgnoringPortEnv.
	//
	// Delegates to the same centralized rule as 2b rather than keeping a
	// private copy: PortImpliesServerMode is shaped like HostImpliesServerMode
	// (proxied exemption, then the env vars), so the storage-mode and
	// lifecycle resolvers read one definition of the port rule.
	//
	// That a bare port env var outranks an explicit dolt_mode is deliberate
	// and settled (be-yb2ai), not an asymmetry with 2b: HostImpliesServerMode
	// checks BEADS_DOLT_SERVER_HOST *before* its DoltMode suppression and can
	// return on it, so the real precedence there is env > explicit dolt_mode >
	// persisted host. 2c mirrors every tier that has a port-side counterpart.
	// TestResolveServerMode_EnvPortOverridesStaleEmbedded pins it.
	//
	// configfile.IsDoltServerMode deliberately does NOT consult this predicate
	// — see the note there and GH#6181.
	if honorPortEnv && hostCfg.PortImpliesServerMode() {
		return ServerModeExternal
	}

	// 3. Explicit embedded mode in metadata.json
	if fileCfg != nil && strings.ToLower(fileCfg.DoltMode) == configfile.DoltModeEmbedded &&
		fileCfg.DoltMode != "" { // empty defaults to embedded in GetDoltMode, but we treat empty as "unset"
		return ServerModeEmbedded
	}

	// 4. Explicit server port in metadata.json -> external
	if fileCfg != nil && fileCfg.DoltServerPort > 0 {
		return ServerModeExternal
	}

	// 4b. The central server config names this server-mode workspace's
	// endpoint -> external (GH#3895). See centralConfigNamesServer.
	if centralConfigNamesServer(beadsDir, fileCfg) {
		return ServerModeExternal
	}

	// 5. Default: beads owns the server
	return ServerModeOwned
}

// centralConfigNamesServer is resolveServerMode's check 4b: fileCfg is in
// server storage mode (IsDoltServerMode, which excludes proxied) and the
// central server config (server.json) supplies its effective non-local host,
// or its effective port (no source above the central step names one; bd's
// port file is skipped, since it records only the port an earlier open
// reached). One file every workspace on the machine shares cannot name a
// server one workspace owns.
//
// Only a workspace already in server mode qualifies: server.json never
// selects the storage mode (configfile.Config.HostImpliesServerMode), so an
// embedded or mode-less workspace never resolves external because of it, and
// the storage-mode and lifecycle resolvers keep agreeing.
//
// A metadata.json dolt_server_port is the step just above the central one,
// and check 4 returns before this check sees it, so here it stands for "the
// central port is next": ExternalPortSetting's Sole asks this question as if
// that port were removed.
func centralConfigNamesServer(beadsDir string, fileCfg *configfile.Config) bool {
	if fileCfg == nil || !fileCfg.IsDoltServerMode() {
		return false
	}
	if fileCfg.CentralConfigSuppliesHost() && !configfile.IsLocalHostString(fileCfg.GetDoltServerHost()) {
		return true
	}
	if configfile.CentralServerPort() == 0 {
		return false
	}
	_, source := resolveServerPortForMode(beadsDir, beadsDir, false, true)
	return source == PortSourceCentralConfig || source == PortSourceMetadataJSON
}

// ErrExternallyManaged matches EnsureRunning's refusal to start a server for a
// workspace whose server is externally managed (ServerModeExternal). The
// refusal is not a failed start: bd never tried, so a caller that reports it
// as one ("auto-start failed") misdescribes it.
var ErrExternallyManaged = errors.New("dolt server is externally managed; bd does not start it")

// externallyManagedError is EnsureRunning's refusal: its own text, matching
// ErrExternallyManaged.
type externallyManagedError struct{ msg string }

func (e *externallyManagedError) Error() string        { return e.msg }
func (e *externallyManagedError) Is(target error) bool { return target == ErrExternallyManaged }

// ExternalPortDeclaration is the setting that declares a workspace's Dolt
// server externally managed (ServerModeExternal) by naming a port for it, as
// found by ExternalPortSetting. The zero value means no such setting does.
type ExternalPortDeclaration struct {
	// Setting names the declaring setting for the operator:
	// "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
	// "dolt_server_port in <metadata.json path>", or
	// "dolt_server_port in <server.json path>".
	Setting string
	// NamesEndpoint reports whether Setting's value is the port bd dialed. It
	// is false when a higher-ranked source (config.yaml dolt.port, the dolt
	// data directory's listener.port, a port file with no pid file beside it)
	// chose a different port than the metadata.json dolt_server_port that
	// makes the workspace external.
	NamesEndpoint bool
	// PortFile is bd's port file when it chose the dialed port, Setting
	// names another one, DeclaredPort, and removing the file moves the dial
	// off that port (no config.yaml dolt.port or listener.port names it too).
	// The file records the port an earlier open reached and outranks
	// dolt_server_port, and `bd dolt set port` leaves it alone, so it is what
	// still points bd at the old port.
	PortFile     string
	DeclaredPort int
	// Sole reports, when neither NamesEndpoint nor PortFile is set, whether
	// nothing else declares the server externally managed (ResolveServerMode
	// checks 1, 2b and 2c: BEADS_DOLT_SERVER_MODE=1, a remote host, or a port
	// env var naming another port), so removing Setting lets bd manage the
	// server.
	Sole bool
}

// ExternalPortSetting finds the setting that declares beadsDir's Dolt server
// externally managed by naming a port, for a dial of port resolved from
// source; the zero ExternalPortDeclaration when no such setting does. The
// settings are BEADS_DOLT_SERVER_PORT and the legacy BEADS_DOLT_PORT
// (ResolveServerMode check 2c, #5934), each only when its value is the dialed
// port, whichever source chose that port: a config.yaml dolt.port or
// listener.port outranks the legacy BEADS_DOLT_PORT for the dial, and an env
// var naming the same port still declares the server external. Then
// metadata.json's dolt_server_port (check 4, GH#2554), which declares the
// server external whichever source chose the port, so it is reported beside a
// config.yaml or listener.port port too (`bd dolt set port N --update-config`
// writes both). Excluded: shared-server mode, since `bd dolt start` is how bd
// starts that server; a port from bd's port file with bd's pid file beside it,
// which records a server bd started; and the shared-server and remote-host
// defaults. A port file with no pid file beside it records only the port an
// earlier open reached (the store writes one after a successful local open),
// and it outranks listener.port, config.yaml and dolt_server_port for the
// dial, so its port is matched against the settings like any other; when
// dolt_server_port names another port, the declaration carries the file and
// that port, since the file is what still points bd at the old one, unless a
// config.yaml dolt.port or listener.port names the file's port too, so that
// removing the file would not move the dial. A port of 0 matches any named
// value. The env vars are parsed as the store and check 2c parse them,
// untrimmed, so a value neither of them reads is never named.
func ExternalPortSetting(beadsDir string, source PortSource, port int) ExternalPortDeclaration {
	switch source {
	case PortSourceUnset, PortSourceSharedServerDefault, PortSourceExternalHostDefault:
		return ExternalPortDeclaration{}
	case PortSourcePortFile:
		if _, err := os.Stat(pidPath(beadsDir)); err == nil {
			return ExternalPortDeclaration{}
		}
	}
	if IsSharedServerMode() || ResolveServerMode(beadsDir) != ServerModeExternal {
		return ExternalPortDeclaration{}
	}
	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if p, err := strconv.Atoi(os.Getenv(name)); err == nil && p > 0 && (port == 0 || p == port) {
			return ExternalPortDeclaration{Setting: name, NamesEndpoint: true}
		}
	}
	// The central server config's port (check 4b, GH#3895), when it is the
	// one dialed.
	if source == PortSourceCentralConfig {
		if p := configfile.CentralServerPort(); p > 0 && (port == 0 || p == port) {
			return ExternalPortDeclaration{
				Setting:       "dolt_server_port in " + remedy.PrintedValue(configfile.CentralConfigPath()),
				NamesEndpoint: true,
			}
		}
	}
	metadataPath := configfile.ConfigPath(beadsDir)
	if _, err := os.Stat(metadataPath); err != nil {
		return ExternalPortDeclaration{}
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil || cfg.DoltServerPort <= 0 {
		return ExternalPortDeclaration{}
	}
	decl := ExternalPortDeclaration{
		Setting:       "dolt_server_port in " + remedy.PrintedValue(metadataPath),
		NamesEndpoint: port == 0 || cfg.DoltServerPort == port,
	}
	switch {
	case decl.NamesEndpoint:
	case source == PortSourcePortFile && portBehindPortFile(beadsDir, cfg.DoltServerPort) != port:
		decl.PortFile, decl.DeclaredPort = portPath(beadsDir), cfg.DoltServerPort
	default:
		// Checks 1, 2b, 2c and 4b of resolveServerMode; keep in step with it.
		decl.Sole = os.Getenv("BEADS_DOLT_SERVER_MODE") != "1" &&
			!cfg.HostImpliesServerMode() && !cfg.PortImpliesServerMode() &&
			!centralConfigNamesServer(beadsDir, cfg)
	}
	return decl
}

// portBehindPortFile is the port the dial falls back to once beadsDir's port
// file is gone: the first source after the file in portSources that resolves
// one. metadataPort stands in for metadata.json's dolt_server_port, whose
// source prints a deprecation warning when it resolves.
func portBehindPortFile(beadsDir string, metadataPort int) int {
	for _, src := range portSources {
		switch src.source {
		case PortSourceEnv, PortSourcePortFile:
			continue
		case PortSourceMetadataJSON:
			return metadataPort
		}
		if p, ok := src.resolve(beadsDir); ok {
			return p
		}
	}
	return metadataPort
}

// Advice is the advice for an unreachable Dolt server at endpoint that d
// declares externally managed, or "" for the zero declaration. It says bd did
// not start a server, in the words `bd init --external` uses (#7674), and
// does not suggest `bd dolt start`: that command would start a server of bd's
// own over .beads/dolt on the same port, a different database that hides the
// real one and takes the port the real server needs. When d carries bd's port
// file it names that file, and it says removing dolt_server_port lets bd
// manage the server only when d is Sole.
func (d ExternalPortDeclaration) Advice(endpoint string) string {
	switch {
	case d.Setting == "":
		return ""
	case d.NamesEndpoint:
		return ExternallyManagedAdvice(d.Setting, endpoint)
	}
	advice := fmt.Sprintf("%s declares this workspace's Dolt server externally managed, so bd did not start one at %s.\n",
		d.Setting, remedy.PrintedValue(endpoint))
	if d.PortFile != "" {
		return advice + fmt.Sprintf("bd took that port from %s, which records the port an earlier command reached "+
			"and outranks dolt_server_port (%d). Start that server, or remove that file.", remedy.PrintedValue(d.PortFile), d.DeclaredPort)
	}
	advice += "Start that server, or point bd at the right endpoint."
	if d.Sole {
		advice += " Removing dolt_server_port lets bd manage the server."
	}
	return advice
}

// ExternallyManagedAdvice is the advice for a Dolt server at endpoint that bd
// could not reach and that setting, by naming endpoint's port, declares
// externally managed. See ExternalPortDeclaration.Advice.
func ExternallyManagedAdvice(setting, endpoint string) string {
	return fmt.Sprintf("%s declares the Dolt server at %s externally managed, so bd did not start one.\n"+
		"Start that server, or point bd at the right endpoint.", setting, remedy.PrintedValue(endpoint))
}

// UnreachableEndpointAdvice is the advice for a TCP dial of host:port, the
// port resolved from source, that found no server, or "" when the caller's own
// advice stands. A host bd does not run its server at
// (configfile.BdRunsServerAt) gets the remote-host advice (GH#3518): on
// another machine, at the IPv6 loopback or at another loopback address bd
// starts no server, and `bd dolt start` refuses the host. With no port
// resolved (port 0), it gets UnresolvedPortAdvice instead, never a check of
// port 0. The host bd runs its server at gets the externally managed advice
// when a setting declares the workspace's server so (see
// ExternalPortSetting).
func UnreachableEndpointAdvice(beadsDir, host string, port int, source PortSource) string {
	if !configfile.BdRunsServerAt(host) {
		if port == 0 {
			return UnresolvedPortAdvice(host)
		}
		return remoteServerAdvice(host, port)
	}
	return ExternalPortSetting(beadsDir, source, port).Advice(doltutil.ServerAddr(host, port))
}

// UnresolvedPortAdvice is the advice for a Dolt server at host for which no
// port source resolved a port, or "" when bd runs the server there
// (configfile.BdRunsServerAt), whose start resolves one. bd runs no server
// anywhere else, so no bd command publishes a port for one there, and the
// store refuses to open until the port is pinned, with this advice.
func UnresolvedPortAdvice(host string) string {
	if configfile.BdRunsServerAt(host) {
		return ""
	}
	return fmt.Sprintf("bd does not manage the Dolt server at %s; pin its port with "+
		"BEADS_DOLT_SERVER_PORT or dolt.port in .beads/config.yaml.", remedy.PrintedValue(host))
}

// remoteServerAdvice is the advice for a Dolt server at host:port that bd
// does not run (configfile.BdRunsServerAt) and could not reach, in the words
// the store uses for one.
func remoteServerAdvice(host string, port int) string {
	return "Verify the external server is running and reachable from this host:\n  " +
		ReachabilityCheck(host, port)
}

// ReachabilityCheck is the shell check that advice for an unreachable Dolt
// server at host:port suggests. nc takes an IPv6 host bare, and curl takes it
// bracketed with the port, in double quotes: a shell reads an unquoted
// [::1]:3307 as a glob, which zsh refuses ("no matches found"). An IPv4
// address or a hostname needs no quotes and gets none. The host is the
// workspace's dolt.host, which a cloned repository's committed config.yaml
// can set, so one that is not a plain shell word (remedy.IsPlainWord) is not
// pasted into the commands: they take a <host> placeholder, and the host is
// named beside them escaped (remedy.EscapedValue).
func ReachabilityCheck(host string, port int) string {
	bare := configfile.UnbracketHost(host)
	if !remedy.IsPlainWord(bare) {
		return fmt.Sprintf("nc -zv <host> %d  # or curl <host>:%d for an HTTP-style check, with the dolt.host %s for <host>",
			port, port, remedy.EscapedValue(bare))
	}
	// A plain word, and the address built from it, print as themselves, so
	// PrintedValue spells each unchanged.
	target := doltutil.PrintedServerAddr(host, port)
	if strings.HasPrefix(target, "[") {
		target = `"` + target + `"`
	}
	return fmt.Sprintf("nc -zv %s %d  # or curl %s for an HTTP-style check",
		remedy.PrintedValue(bare), port, target)
}
