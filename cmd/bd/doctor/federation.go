package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/pushstate"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// doltDatabaseName returns the configured Dolt database name for the given beads directory.
// Falls back to the default ("beads") if config cannot be read.
func doltDatabaseName(beadsDir string) string {
	dbName := configfile.DefaultDoltDatabase
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		dbName = cfg.GetDoltDatabase()
	}
	return dbName
}

// doltServerConfig returns a read-only dolt.Config populated with server
// connection settings from beads configuration. This ensures federation checks
// use the configured host/port rather than falling back to defaults.
func doltServerConfig(beadsDir, doltPath string) *dolt.Config {
	cfg := &dolt.Config{
		Path:     doltPath,
		ReadOnly: true,
		Database: doltDatabaseName(beadsDir),
	}
	if bcfg, err := configfile.Load(beadsDir); err == nil && bcfg != nil {
		cfg.ServerHost = bcfg.GetDoltServerHost()
		// Carries PortSource with the port: this cfg reaches applyConfigDefaults,
		// which reads a sourceless port as caller-explicit (see
		// dolt.ApplyResolvedServerPort).
		dolt.ApplyResolvedServerPort(beadsDir, cfg)
		cfg.ServerUser = bcfg.GetDoltServerUser()
		cfg.ServerTLS = bcfg.GetDoltServerTLS()
		cfg.ServerPassword = bcfg.GetDoltServerPasswordForPort(cfg.ServerPort)
	}
	dolt.ApplyCLIAutoStart(beadsDir, cfg)
	return cfg
}

// CheckLegacyCLIRemotes warns when legacy filesystem CLI remotes are not
// represented in SQL, because bd now treats SQL remotes as the source of truth.
func CheckLegacyCLIRemotes(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "Skipped (database unavailable)",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	sqlRemotes, err := store.ListRemotes(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "Skipped (SQL remotes unavailable)",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	sqlByName := make(map[string]string, len(sqlRemotes))
	for _, remote := range sqlRemotes {
		sqlByName[remote.Name] = remote.URL
	}

	type cliLocation struct {
		label string
		dir   string
	}
	locations := []cliLocation{
		{label: "database CLI directory", dir: store.CLIDir()},
		{label: "Dolt server root", dir: store.Path()},
	}

	var missing []string
	var inspected []string
	var inspectErrors []string
	seenDirs := make(map[string]bool, len(locations))
	for _, loc := range locations {
		if loc.dir == "" {
			continue
		}
		dir := filepath.Clean(loc.dir)
		if seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true

		if _, err := os.Stat(filepath.Join(dir, ".dolt")); err != nil {
			if !os.IsNotExist(err) {
				inspectErrors = append(inspectErrors, fmt.Sprintf("%s (%s): %v", loc.label, dir, err))
			}
			continue
		}

		cliRemotes, err := doltutil.ListCLIRemotes(dir)
		if err != nil {
			inspectErrors = append(inspectErrors, fmt.Sprintf("%s (%s): %v", loc.label, dir, err))
			continue
		}
		inspected = append(inspected, fmt.Sprintf("%s: %s", loc.label, dir))
		for _, remote := range cliRemotes {
			sqlURL := sqlByName[remote.Name]
			if !doltutil.RemoteURLsMatch(sqlURL, remote.URL) {
				missing = append(missing, fmt.Sprintf("%s %s=%s", loc.label, remote.Name, remote.URL))
			}
		}
	}

	if len(inspected) == 0 && len(inspectErrors) > 0 {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "No legacy CLI remote check available",
			Detail:   strings.Join(inspectErrors, "\n"),
			Category: CategoryFederation,
		}
	}

	if len(missing) == 0 {
		return DoctorCheck{
			Name:     "Dolt Remote Migration",
			Status:   StatusOK,
			Message:  "No legacy CLI-only remotes detected",
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Remote Migration",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d legacy CLI remote(s) not visible through SQL", len(missing)),
		Detail:   fmt.Sprintf("Inspected CLI directories:\n%s\nRemotes: %s\nbd dolt remote list, push, and pull use SQL remotes as the source of truth.", strings.Join(inspected, "\n"), strings.Join(missing, ", ")),
		Fix:      "Re-register each remote with 'bd dolt remote add <name> <url>' so it is stored in SQL.",
		Category: CategoryFederation,
	}
}

// serverRunningOrProbeable reports whether the workspace's Dolt server is up:
// true when the PID file names a running server (state from
// doltserver.IsRunning), without calling probe, and otherwise probe's answer.
// The PID file alone misses every server bd did not start (launchd, systemd,
// a container) and bd's shared server, whose PID file lives in the shared
// server directory, while the store still opens the endpoint probe asks about.
func serverRunningOrProbeable(state *doltserver.State, probe func() bool) bool {
	if state != nil && state.Running {
		return true
	}
	return probe()
}

// CheckFederationRemotesAPI checks if the remotesapi port is accessible for federation.
// This is the port used for peer-to-peer sync operations.
func CheckFederationRemotesAPI(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt server is running using doltserver.IsRunning which
	// correctly resolves PID file paths (in beadsDir, not doltPath)
	// and handles orchestrator daemon PID files. When the PID file names no
	// running server, probe the endpoint the store opens below, as the Dolt
	// Mode check does (doltModeServerReachable), so a server bd did not start
	// is not reported as down while the remotes are read from it.
	serverState, _ := doltserver.IsRunning(beadsDir)
	cfg, _ := configfile.Load(beadsDir)
	serverRunning := serverRunningOrProbeable(serverState, func() bool {
		return doltModeServerReachable(beadsDir, cfg)
	})

	if !serverRunning {
		// No server running - check if we have remotes configured
		ctx := context.Background()
		store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
		if err != nil {
			return DoctorCheck{
				Name:     "Federation remotesapi",
				Status:   StatusOK,
				Message:  "N/A (server not running, no remotes check needed)",
				Category: CategoryFederation,
			}
		}
		defer func() { _ = store.Close() }()

		remotes, err := store.ListRemotes(ctx)
		if err != nil || len(remotes) == 0 {
			return DoctorCheck{
				Name:     "Federation remotesapi",
				Status:   StatusOK,
				Message:  "N/A (no peers configured)",
				Category: CategoryFederation,
			}
		}

		// Has remotes but no server running - suggest starting in federation mode
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Server not running (%d peers configured)", len(remotes)),
			Detail:   "Federation requires dolt sql-server for peer sync",
			Fix:      "Start dolt sql-server in server mode to enable peer-to-peer sync",
			Category: CategoryFederation,
		}
	}

	// Server is running - check if any federation peers are configured before
	// probing the remotesapi port. Without peers, remotesapi is irrelevant.
	// A server only the probe found has no PID (and an unreadable PID file
	// leaves serverState nil). That probe counts a TCP accept, which proves
	// neither a Dolt server, nor one that takes the workspace's user, nor its
	// database, so when its peers cannot be read the check reports why rather
	// than dial a remotesapi listener no peer was shown to need. A PID-tracked
	// server's listener is checked whether or not its peers could be read.
	pid := 0
	if serverState != nil && serverState.Running {
		pid = serverState.PID
	}
	peers, skipped, err := federationPeers(beadsDir, doltPath)
	switch {
	case err != nil && pid <= 0:
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  skipped,
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	case err == nil && !peers:
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (no federation peers configured)",
			Category: CategoryFederation,
		}
	}

	// Server is running and peers are configured - check if remotesapi port is
	// accessible.
	return checkRemotesAPIListener(beadsDir, pid)
}

// federationPeers reports whether the store the federation checks open
// (federationStoreConfig) lists a federation peer, a remote other than
// origin. When it cannot tell, err is the store open's or ListRemotes's error
// and skipped the message that names the step that failed, as
// CheckLegacyCLIRemotes reports it.
func federationPeers(beadsDir, doltPath string) (peers bool, skipped string, err error) {
	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return false, "Skipped (database unavailable)", err
	}
	defer func() { _ = store.Close() }()

	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		return false, "Skipped (SQL remotes unavailable)", err
	}
	for _, r := range remotes {
		if r.Name != "origin" {
			return true, "", nil
		}
	}
	return false, "", nil
}

// checkRemotesAPIListener probes the remotesapi listener of a running server.
// It resolves the port through the same chain the launcher uses
// (BEADS_DOLT_REMOTESAPI_PORT -> user-global dolt.remotesapi-port for a shared
// server -> the per-project configfile), not configfile alone: reading
// configfile alone diagnoses port 8080 while a shared server launched from the
// user-global key is listening somewhere else.
//
// pid is the server's PID from the PID file, or 0 for a server only the
// endpoint probe found. A PID-tracked server is bd's own, which binds
// 127.0.0.1, so its listener is dialed there; for pid 0 the listener is
// dialed where the probe reached the server (probedServer), and the error
// names that endpoint.
//
// The mode is classified once because it also decides the remedy. bd opens a
// remotesapi listener only for the shared server it launches, so restarting is
// the fix there; restarting a per-project server reproduces the same error.
func checkRemotesAPIListener(beadsDir string, pid int) DoctorCheck {
	sharedMode := doltserver.IsSharedServerModeForDir(beadsDir)
	remotesAPIPort := doltserver.ResolveRemotesAPIPortForMode(beadsDir, sharedMode)
	if remotesAPIPort <= 0 {
		// Resolved zero means no listener was requested. Dialing port 0 would
		// report a spurious federation error for a correct configuration.
		detail := "The remotesapi port resolves to 0, so no remotesapi listener is expected."
		if sharedMode {
			detail = "No remotesapi port is configured, so the shared dolt sql-server does not open a remotesapi listener."
		}
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusOK,
			Message:  "N/A (remotesapi listener disabled)",
			Detail:   detail,
			Category: CategoryFederation,
		}
	}
	// bd's own server binds 127.0.0.1, so a PID-tracked server is dialed
	// there. A server only the endpoint probe found is dialed where the probe
	// reached it.
	addr := doltutil.ServerAddr("127.0.0.1", remotesAPIPort)
	var location string
	if pid <= 0 {
		var host string
		location, host = probedServer(doltserver.ResolveEndpoint(beadsDir, nil))
		addr = doltutil.ServerAddr(host, remotesAPIPort)
	}
	// Left as a bare dial+close (no doltserver.ProbeSQLServer): remotesapi
	// speaks gRPC/HTTP, not the MySQL protocol, so there is no handshake
	// greeting to drain here.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fix := fmt.Sprintf("bd opens a remotesapi listener only for the shared server: run dolt sql-server with --remotesapi-port %d, or enable dolt.shared-server and set dolt.remotesapi-port in the user-global config", remotesAPIPort)
		if sharedMode {
			fix = "Restart the shared server so it picks up the resolved remotesapi port: bd dolt stop && bd dolt start"
		}
		detail := fmt.Sprintf("Server running (PID %d) but remotesapi port unreachable: %v", pid, err)
		if pid <= 0 {
			// Only the endpoint probe found this server, so name the endpoint
			// it reached rather than "PID 0". The dial error repeats the host
			// it dialed.
			detail = fmt.Sprintf("Server reachable at %s (no PID file in this workspace names it) but remotesapi port unreachable: %v", remedy.PrintedValue(location), remedy.PrintedError(err))
		}
		return DoctorCheck{
			Name:     "Federation remotesapi",
			Status:   StatusError,
			Message:  fmt.Sprintf("remotesapi port %d not accessible", remotesAPIPort),
			Detail:   detail,
			Fix:      fix,
			Category: CategoryFederation,
		}
	}
	_ = conn.Close() // Best effort cleanup

	return DoctorCheck{
		Name:     "Federation remotesapi",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Port %d accessible", remotesAPIPort),
		Category: CategoryFederation,
	}
}

// probedServer names the server ep's probe reaches (Endpoint.Probe) and the
// host to dial its remotesapi listener at. The probe and the store's open both
// take the store's transport (Endpoint.Transport), a live unix socket before
// TCP. A server reached over its socket runs on this machine, so it is named
// by the socket and its listener dialed at 127.0.0.1; over TCP it is named
// host:port and its listener dialed at the configured host. Endpoint.Location
// cannot tell the two apart: it names host:port whenever a port resolved, even
// when the socket beside it answered.
func probedServer(ep doltserver.Endpoint) (location, remotesAPIHost string) {
	if socket := ep.Transport(); socket != "" {
		return "unix:" + socket, "127.0.0.1"
	}
	return ep.Location(), ep.Host
}

// CheckFederationPeerConnectivity checks if configured peer remotes are reachable.
func CheckFederationPeerConnectivity(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  "Unable to list remotes",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	if len(remotes) == 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "No peers configured",
			Category: CategoryFederation,
		}
	}

	// Try to get sync status for each peer (this doesn't require network for cached data)
	var reachable, unreachable []string
	var statusDetails []string

	for _, remote := range remotes {
		// Skip origin - it's typically the DoltHub remote, not a peer
		if remote.Name == "origin" {
			continue
		}

		status, err := store.SyncStatus(ctx, remote.Name)
		if err != nil {
			unreachable = append(unreachable, remote.Name)
			statusDetails = append(statusDetails, fmt.Sprintf("%s: %v", remote.Name, err))
		} else {
			reachable = append(reachable, remote.Name)
			if status.LocalAhead > 0 || status.LocalBehind > 0 {
				statusDetails = append(statusDetails, fmt.Sprintf("%s: %d ahead, %d behind",
					remote.Name, status.LocalAhead, status.LocalBehind))
			}
		}
	}

	// If no peers (only origin), report as OK
	if len(reachable) == 0 && len(unreachable) == 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusOK,
			Message:  "No federation peers configured (only origin remote)",
			Category: CategoryFederation,
		}
	}

	if len(unreachable) > 0 {
		return DoctorCheck{
			Name:     "Peer Connectivity",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d/%d peers unreachable", len(unreachable), len(reachable)+len(unreachable)),
			Detail:   strings.Join(statusDetails, "\n"),
			Fix:      "Check peer URLs and network connectivity",
			Category: CategoryFederation,
		}
	}

	msg := fmt.Sprintf("%d peers reachable", len(reachable))
	detail := ""
	if len(statusDetails) > 0 {
		detail = strings.Join(statusDetails, "\n")
	}

	return DoctorCheck{
		Name:     "Peer Connectivity",
		Status:   StatusOK,
		Message:  msg,
		Detail:   detail,
		Category: CategoryFederation,
	}
}

// CheckFederationSyncStaleness checks for stale sync status with origin and
// peers, from local state only: no network call is made.
func CheckFederationSyncStaleness(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	skipped := originSkipReason(beadsDir)
	remotes, err := store.ListRemotes(ctx)
	if (err != nil || len(remotes) == 0) && skipped == "" {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "No peers configured",
			Category: CategoryFederation,
		}
	}

	// Origin's leg reads only local state; the peer leg is unchanged.
	origin := originSyncFacts{skipped: skipped}
	var peers peerStaleness
	for _, remote := range remotes {
		if remote.Name == originRemote {
			if skipped != "" {
				continue
			}
			origin = readOriginSyncFacts(beadsDir)
			status, err := store.SyncStatus(ctx, originRemote)
			origin.statusErr = err
			if status != nil {
				origin.ahead, origin.behind = status.LocalAhead, status.LocalBehind
			}
			continue
		}

		status, err := store.SyncStatus(ctx, remote.Name)
		if err != nil {
			continue // Already handled in peer connectivity check
		}

		// Warn if significantly behind
		if status.LocalBehind > 0 {
			peers.totalBehind += status.LocalBehind
			peers.lines = append(peers.lines, fmt.Sprintf("%s: %d commits behind",
				remote.Name, status.LocalBehind))
		}

		// Note: LastSync time tracking is not yet implemented in SyncStatus
		// When it is, we can add time-based staleness warnings here
	}

	return syncStalenessResult(origin, peers)
}

// originRemote is the remote bd dolt push, bd sync and auto-push publish to
// by default, and the one .beads/push-state.json describes.
const originRemote = "origin"

// originSyncFacts is what Sync Staleness knows about origin without a network
// call: the local push history in push-state.json and a comparison of local
// history with the cached origin ref, which is only as fresh as the last
// pull, push or sync (GH#6289).
type originSyncFacts struct {
	checked       bool             // origin is configured, so its leg ran
	state         *pushstate.State // push-state.json; nil when there is none
	stateErr      error            // push-state.json exists but cannot be read or parsed
	ahead, behind int              // commits only local / only in the cached ref; -1 when there is no ref
	statusErr     error            // the comparison itself failed
	skipped       string           // the setting that disables push to origin, e.g. "no-push=true"
}

// originSkipReason names the setting in the inspected workspace's
// .beads/config.yaml that disables push to origin, or returns "" when
// neither no-push nor dolt.local-only is set.
func originSkipReason(beadsDir string) string {
	for _, key := range []string{"no-push", "dolt.local-only"} {
		if on, err := strconv.ParseBool(strings.TrimSpace(config.GetStringFromDir(beadsDir, key))); err == nil && on {
			return key + "=true"
		}
	}
	return ""
}

// readOriginSyncFacts loads origin's local push history from beadsDir.
func readOriginSyncFacts(beadsDir string) originSyncFacts {
	state, err := pushstate.Load(beadsDir)
	return originSyncFacts{checked: true, state: state, stateErr: err}
}

// peerStaleness is the peer leg's finding: one line per peer this clone is
// behind, and the total commits behind.
type peerStaleness struct {
	lines       []string
	totalBehind int
}

// originFindings turns origin's facts into a warning message (the first
// finding's), detail lines and fixes; an empty message means no finding.
func originFindings(o originSyncFacts) (message string, details, fixes []string) {
	if !o.checked {
		return "", nil, nil
	}
	add := func(msg, detail, fix string) {
		if message == "" {
			message = msg
		}
		details = append(details, detail)
		fixes = append(fixes, fix)
	}
	switch {
	case o.stateErr != nil:
		add("Push history unreadable",
			fmt.Sprintf("origin: cannot read the local push history in .beads/%s: %v", pushstate.FileName, o.stateErr),
			fmt.Sprintf("Repair or delete .beads/%s; the next successful push to origin rewrites it", pushstate.FileName))
	case o.state != nil && o.state.LastError != "":
		// A recorded failure warns even when the cached origin ref or the
		// timestamps look current: the last attempt to publish did not land.
		when := o.state.LastAttempt
		if when == "" {
			when = "an unrecorded time"
		}
		add("Last push to origin failed",
			fmt.Sprintf("origin: the last push attempt (%s) failed: %s", when, o.state.LastError),
			"Retry with 'bd dolt push' or 'bd sync'; a successful push to origin clears this warning")
	}
	const refreshFix = "Run 'bd dolt pull' to refresh the cached origin ref, then re-run 'bd doctor'"
	switch {
	case o.statusErr != nil:
		add("Comparison with cached origin refs unavailable",
			fmt.Sprintf("origin: cannot compare local history with the cached origin ref: %v", o.statusErr),
			refreshFix)
	case o.ahead < 0 || o.behind < 0:
		add("Comparison with cached origin refs unavailable",
			"origin: no cached origin ref to compare local history with (this clone has not pulled from or pushed to origin)",
			refreshFix)
	case o.ahead > 0 && o.behind > 0:
		add("Local history diverged from cached origin refs",
			fmt.Sprintf("origin: %d local commits are not in the cached origin ref, which has %d commits not in local history", o.ahead, o.behind),
			"Run 'bd dolt pull', then 'bd dolt push'")
	case o.ahead > 0:
		add("Local history ahead of cached origin refs",
			fmt.Sprintf("origin: %d local commits are not in the cached origin ref", o.ahead),
			"Run 'bd dolt push'; if a push already ran, check its output and retry")
	case o.behind > 0:
		add("Local history behind cached origin refs",
			fmt.Sprintf("origin: the cached origin ref has %d commits not in local history", o.behind),
			"Run 'bd dolt pull'")
	}
	return message, details, fixes
}

// syncStalenessResult aggregates the origin and peer legs into the Sync
// Staleness check. A peer-only finding reads as it always has.
func syncStalenessResult(origin originSyncFacts, peers peerStaleness) DoctorCheck {
	message, details, fixes := originFindings(origin)
	if len(peers.lines) > 0 {
		peerMsg := fmt.Sprintf("%d total commits behind peers", peers.totalBehind)
		if message == "" {
			message = peerMsg
		} else {
			message += "; " + peerMsg
		}
		details = append(details, peers.lines...)
		fixes = append(fixes, "Run 'bd federation sync' to synchronize with peers")
	}

	if origin.skipped != "" {
		if message == "" {
			return DoctorCheck{
				Name:    "Sync Staleness",
				Status:  StatusOK,
				Message: fmt.Sprintf("Origin check skipped (%s)", origin.skipped),
				Detail: fmt.Sprintf("Push to origin is disabled (%s in .beads/config.yaml), so local history was not "+
					"compared with origin and .beads/%s was not read. Federation peers are still checked.",
					origin.skipped, pushstate.FileName),
				Category: CategoryFederation,
			}
		}
		message += fmt.Sprintf("; origin check skipped (%s)", origin.skipped)
		details = append(details, fmt.Sprintf("origin: check skipped: push to origin is disabled (%s in .beads/config.yaml)", origin.skipped))
	}

	if message == "" && origin.checked {
		return DoctorCheck{
			Name:    "Sync Staleness",
			Status:  StatusOK,
			Message: "No local sync staleness detected",
			Detail: fmt.Sprintf("Local history matches the cached origin ref and .beads/%s records no failed push. "+
				"No network call was made: the cached ref is as fresh as the last pull, push or sync, "+
				"so this does not prove what origin holds now.", pushstate.FileName),
			Category: CategoryFederation,
		}
	}
	if message == "" {
		return DoctorCheck{
			Name:     "Sync Staleness",
			Status:   StatusOK,
			Message:  "Sync is up to date",
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Sync Staleness",
		Status:   StatusWarning,
		Message:  message,
		Detail:   strings.Join(details, "\n"),
		Fix:      strings.Join(fixes, "; "),
		Category: CategoryFederation,
	}
}

// CheckFederationConflicts checks for unresolved merge conflicts.
func CheckFederationConflicts(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	conflicts, err := store.GetConflicts(ctx)
	if err != nil {
		// Some errors are expected (e.g., no conflicts table)
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "doesn't exist") {
			return DoctorCheck{
				Name:     "Federation Conflicts",
				Status:   StatusOK,
				Message:  "No conflicts",
				Category: CategoryFederation,
			}
		}
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusWarning,
			Message:  "Unable to check conflicts",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	if len(conflicts) == 0 {
		return DoctorCheck{
			Name:     "Federation Conflicts",
			Status:   StatusOK,
			Message:  "No conflicts",
			Category: CategoryFederation,
		}
	}

	// Group conflicts by issue ID
	issueConflicts := make(map[string][]string)
	for _, c := range conflicts {
		issueConflicts[c.IssueID] = append(issueConflicts[c.IssueID], c.Field)
	}

	var details []string
	for issueID, fields := range issueConflicts {
		details = append(details, fmt.Sprintf("%s: %s", issueID, strings.Join(fields, ", ")))
	}

	return DoctorCheck{
		Name:     "Federation Conflicts",
		Status:   StatusError,
		Message:  fmt.Sprintf("%d unresolved conflicts in %d issues", len(conflicts), len(issueConflicts)),
		Detail:   strings.Join(details, "\n"),
		Fix:      "Run 'bd federation sync --strategy ours|theirs' to resolve conflicts",
		Category: CategoryFederation,
	}
}

// CheckDoltServerModeMismatch checks for mismatch between Dolt init and server mode.
// This detects cases where:
// - Server mode is expected but no server is running
// - Embedded mode is being used when server mode should be used (federation with peers)
func CheckDoltServerModeMismatch(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryFederation,
		}
	}

	// Check if dolt directory exists
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "N/A (no dolt database)",
			Category: CategoryFederation,
		}
	}

	// Check if server is reachable by trying to connect
	cfg, _ := configfile.Load(beadsDir)
	serverReachable := doltModeServerReachable(beadsDir, cfg)

	// Open storage to check for remotes, on the server the probe reached
	// (federationStoreConfig)
	ctx := context.Background()
	store, err := dolt.New(ctx, federationStoreConfig(beadsDir, doltPath))
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}
	defer func() { _ = store.Close() }()

	// Check for configured remotes
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  "Unable to list remotes",
			Detail:   err.Error(),
			Category: CategoryFederation,
		}
	}

	// Count federation peers (exclude origin)
	peerCount := 0
	for _, r := range remotes {
		if r.Name != "origin" {
			peerCount++
		}
	}

	// Determine expected vs actual mode
	if peerCount > 0 && !serverReachable {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Server not reachable with %d peers configured", peerCount),
			Detail:   "Federation with peers requires a running dolt sql-server",
			Fix:      "Start dolt sql-server manually",
			Category: CategoryFederation,
		}
	}

	if serverReachable {
		return DoctorCheck{
			Name:     "Dolt Mode",
			Status:   StatusOK,
			Message:  "Server mode (connected)",
			Detail:   fmt.Sprintf("%d peers configured", peerCount),
			Category: CategoryFederation,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Mode",
		Status:   StatusOK,
		Message:  "Embedded mode",
		Detail:   "No federation peers configured",
		Category: CategoryFederation,
	}
}

// doltModeServerReachable reports whether the server beadsDir's store dials
// accepts a connection: the endpoint doltserver.ResolveEndpoint resolves (its
// port, the legacy BEADS_DOLT_PORT included, which DefaultConfig leaves out),
// over the store's transport (Endpoint.Probe). It is false without a
// metadata.json, and for an endpoint with neither a port nor a socket.
func doltModeServerReachable(beadsDir string, cfg *configfile.Config) bool {
	if cfg == nil {
		return false
	}
	return doltserver.ResolveEndpoint(beadsDir, cfg).Probe(2*time.Second) == nil
}

// federationStoreConfig is the store config every federation check opens:
// doltServerConfig with the unix socket the CLI's store dials,
// BEADS_DOLT_SERVER_SOCKET else metadata.json's dolt_server_socket
// (resolveDoltServerConnection in cmd/bd/main.go), so the checks read the
// server bd list reads, the one the Dolt Mode check's probe
// (doltModeServerReachable) reaches by the same socket-first rule. dolt.New
// takes its socket from BEADS_DOLT_SERVER_SOCKET alone, so beside a resolved
// port and a live metadata.json socket the Dolt Mode check reported the
// socket's server connected while it counted the federation peers of the
// server at the port, and once it listed the socket's, the other federation
// checks still read the port's in the same run (#7874 review). None of them
// has a bd doctor --fix repair whose server they must read instead, as the
// SQL scans do (see fix.OpenScanDB).
func federationStoreConfig(beadsDir, doltPath string) *dolt.Config {
	storeCfg := doltServerConfig(beadsDir, doltPath)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		storeCfg.ServerSocket = cfg.GetDoltServerSocket()
	}
	return storeCfg
}
