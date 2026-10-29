package main

// bd init has to reason about two database identities at once, and before this
// record existed each guard invented its own answer for both of them:
//
//   - the CONFIGURED database: the one the workspace being re-initialized
//     records — the data a reinit destroys, and the one ADR-0004's
//     missing-database claims are about. Its name comes from the workspace's
//     own metadata, read raw, so no BEADS_DOLT_SERVER_DATABASE value can
//     substitute a different database for the one the user is about to lose.
//   - the TARGET database: the one this invocation's flags select, with the
//     same name-derivation precedence init itself applies when it writes
//     metadata further down.
//
// runInit resolves one initIdentity at one chokepoint — the workspace half
// before any explicit --server-* flag is promoted into the environment
// (promoteExplicitServerConnFlags), so the configured endpoint is the
// workspace's own rather than the flagged one — and threads it by parameter to
// every guard, so each proves its facts about the same database the others do
// (GH#6172).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/utils"
)

// initDBRef names one database identity at one endpoint. Server says which
// storage the ref's database lives in; Host and Port name the server endpoint
// and are meaningful only when Server is true.
type initDBRef struct {
	Database string
	Server   bool
	Host     string
	Port     int
}

// initIdentity is the one record of both database identities bd init reasons
// about, resolved once in runInit and threaded by parameter.
type initIdentity struct {
	// BeadsDir is the workspace init targets, following a redirect the way
	// init's own writes do (beadsDirForInit).
	BeadsDir string
	// RedirectedFrom is the local .beads stub a redirect was followed from,
	// empty when no redirect was.
	RedirectedFrom string
	// Recorded is the workspace's own metadata, read the way a non-mutating
	// open reads it (configfile.LoadForDiscovery), nil when it has none.
	Recorded *configfile.Config
	// RecordedErr is the error that stopped Recorded being read; non-nil only
	// for a present-but-unreadable metadata.json, which is not the same
	// workspace state as no metadata at all.
	RecordedErr error
	// Configured is the database the workspace records (empty ref when the
	// workspace is fresh): the identity whose data a reinit destroys.
	Configured initDBRef
	// Target is the database this invocation selects, with init's own
	// name-derivation precedence applied.
	Target initDBRef
}

// resolveInitWorkspace resolves the workspace half of the identity: the
// .beads directory init targets (BEADS_DIR > worktree fallback > ./.beads
// following a redirect, the beadsDirForInit precedence) and the database that
// workspace records. It must run before promoteExplicitServerConnFlags: the
// configured endpoint is resolved from the workspace's own configuration and
// the ambient environment, never from flags this init is about to re-target
// with.
func resolveInitWorkspace() initIdentity {
	var dir, redirectedFrom string
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		dir = utils.CanonicalizePath(envBeadsDir)
	} else if fallback := beads.GetWorktreeFallbackBeadsDir(); fallback != "" {
		dir = fallback
	} else {
		local := filepath.Join(".", ".beads")
		dir = beads.FollowRedirect(local)
		if dir != local {
			redirectedFrom = local
		}
	}
	return initIdentityAtFill(initIdentity{BeadsDir: dir, RedirectedFrom: redirectedFrom})
}

// initIdentityAtFill fills the workspace half of an identity whose BeadsDir
// is already chosen: the recorded metadata and the CONFIGURED database ref.
func initIdentityAtFill(id initIdentity) initIdentity {
	id.Recorded, id.RecordedErr = configfile.LoadForDiscovery(id.BeadsDir)
	if id.Recorded != nil {
		id.Configured = initDBRef{
			// Raw DoltDatabase, deliberately not GetDoltDatabase(): the
			// getter is env-first, and a BEADS_DOLT_SERVER_DATABASE value
			// must not substitute the database whose destruction the reinit
			// confirm counts.
			Database: id.Recorded.DoltDatabase,
			Server:   id.Recorded.IsDoltServerMode(),
			Host:     id.Recorded.GetDoltServerHost(),
			Port:     doltserver.DefaultConfig(id.BeadsDir).Port,
		}
	}
	return id
}

// initIdentityAt builds the workspace half of an identity for an explicit
// .beads directory, the way the unit-test-callable guards do: no BEADS_DIR
// or worktree-fallback resolution, no redirect following — the caller named
// the workspace.
func initIdentityAt(beadsDir string) initIdentity {
	return initIdentityAtFill(initIdentity{BeadsDir: beadsDir})
}

// resolveInitTargetRef completes the identity with the target half, from the
// endpoint flags and the finalized prefix (normalized by the time init
// derives names from it). The database-name precedence is byte-identical to
// the dbName derivation init applies when it writes metadata: the recorded
// name, else the prefix-derived name, else "beads", overridden by --database.
func resolveInitTargetRef(id initIdentity, database string, prefix string, serverHost string, serverPort int, initServerMode bool) initIdentity {
	dbName := ""
	if id.Recorded != nil && id.Recorded.DoltDatabase != "" {
		dbName = id.Recorded.DoltDatabase
	} else if prefix != "" {
		dbName = dbNameFromPrefix(prefix)
	} else {
		dbName = configfile.DefaultDoltDatabase
	}
	if database != "" {
		dbName = database
	}
	host := initStoreServerHost(serverHost)
	port := serverPort
	if port == 0 {
		port, _, _, _ = initStoreServerPort(id.BeadsDir, host)
	}
	// Restating flags are not changes (GH#5177). A --server-host/--server-port
	// that names the endpoint the workspace itself records — its own host, or
	// the port its own port file names for that host — re-targets nothing,
	// even under a stale BEADS_DOLT_SERVER_* overlay: the flag proves the
	// overlay wrong, so both sides of the identity take the flag and the
	// destroy confirmation still counts at the workspace's own endpoint.
	// The host restatement also re-resolves the configured port through the
	// workspace's own chain, because DefaultConfig discards the port file
	// under whatever host a stale overlay names.
	if id.Recorded != nil && serverHost != "" && initHostsEqual(serverHost, initOwnServerHost(id.Recorded)) {
		id.Configured.Host = host
		if ownPort := initOwnServerPort(id.BeadsDir, id.Recorded); ownPort > 0 {
			id.Configured.Port = ownPort
		}
	}
	if id.Recorded != nil && serverPort != 0 && initOwnServerPort(id.BeadsDir, id.Recorded) == serverPort {
		id.Configured.Port = serverPort
	}
	id.Target = initDBRef{
		Database: dbName,
		Server:   initServerMode,
		Host:     host,
		Port:     port,
	}
	return id
}

// Repoints reports whether this init re-targets the workspace away from the
// database it records — a different database, a different storage mode, or a
// different endpoint. A fresh workspace (no recorded metadata) never
// repoints: it has no identity to leave.
func (id initIdentity) Repoints() bool {
	if id.Recorded == nil || id.RecordedErr != nil {
		return false
	}
	configured := id.Configured.Database
	if configured == "" {
		configured = configfile.DefaultDoltDatabase
	}
	// A --database differing from the recorded name only by case restates
	// the same database (GH#5177): the spelling differs, the database does
	// not, so this is not a repoint.
	return !strings.EqualFold(configured, id.Target.Database) ||
		id.Configured.Server != id.Target.Server ||
		!initHostsEqual(id.Configured.Host, id.Target.Host) ||
		id.Configured.Port != id.Target.Port
}

// refuseUnreadableConfiguredDatabase is the repoint refusal for a configured
// database that exists but cannot be read: an init that re-targets the
// workspace must first know what the old database holds, and when it cannot
// know, it must not run. It is a plain error (exit 1) on purpose: it is not
// one of the frozen init-safety exit codes because no opt-in lifts it — not
// --recreate-missing (which authorizes creating where nothing exists, not
// destroying what cannot be inspected) and not --destroy-token (whose
// confirmation this very failure made impossible to run). The text names the
// configured database, its endpoint, the read failure and the target, and no
// token value (ADR-0002 invariant 4).
func refuseUnreadableConfiguredDatabase(id initIdentity, cause error) error {
	configured := id.Configured.Database
	if configured == "" {
		configured = configfile.DefaultDoltDatabase
	}
	addr := doltutil.PrintedServerAddr(id.Configured.Host, id.Configured.Port)
	return fmt.Errorf("\n⚠ Refusing to repoint this workspace from database %q at %s to database %q:\n"+
		"the configured database is present but its issue count could not be read (%v),\n"+
		"so the destruction a re-initialization would cause cannot be confirmed.\n"+
		"Neither --recreate-missing nor --destroy-token lifts this refusal.\n"+
		"Start the Dolt server at %s or fix the connection so the count can run,\n"+
		"or re-run init without the flags that change the database.\n",
		configured, addr, id.Target.Database, cause, addr)
}

// initHostsEqual compares two server hosts for identity: bd's own server
// address is one host however it is spelled, and every other address compares
// by its unbracketed form, ignoring case.
func initHostsEqual(a, b string) bool {
	if configfile.ClassifyDoltHost(a) == configfile.DoltHostBdServer && configfile.ClassifyDoltHost(b) == configfile.DoltHostBdServer {
		return true
	}
	return strings.EqualFold(configfile.UnbracketHost(a), configfile.UnbracketHost(b))
}

// initOwnServerHost is the server host the workspace itself names, with the
// environment overlay removed: metadata.json's dolt_server_host, else
// config.yaml's dolt.host, else bd's own default. A --server-host restating
// this value re-targets nothing (GH#5177): the workspace already lives at
// that host, whatever a stale BEADS_DOLT_SERVER_HOST overlay says.
func initOwnServerHost(recorded *configfile.Config) string {
	if recorded != nil && recorded.DoltServerHost != "" {
		return recorded.DoltServerHost
	}
	if h := config.GetYamlConfig("dolt.host"); h != "" {
		return h
	}
	return configfile.DefaultDoltServerHost
}

// initOwnServerPort is the server port the workspace itself names for its own
// host, with the environment overlay removed: the port file (bd's bookkeeping
// for the server it runs at the workspace's own host), else config.yaml's
// dolt.port, else metadata.json's dolt_server_port. Zero when the workspace
// names none. A --server-port restating this value re-targets nothing
// (GH#5177).
func initOwnServerPort(beadsDir string, recorded *configfile.Config) int {
	if configfile.BdRunsServerAt(initOwnServerHost(recorded)) {
		if port := doltserver.ReadPortFile(beadsDir); port > 0 {
			return port
		}
	}
	if p := config.GetYamlConfig("dolt.port"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	if recorded != nil && recorded.DoltServerPort > 0 {
		return recorded.DoltServerPort
	}
	return 0
}

// countConfiguredIssues counts the issues in the CONFIGURED database — the
// one the workspace records, never one an env value names — so the reinit
// destroy confirmation names the data the user is actually about to lose. It
// never creates anything: the embedded arm opens read-only and the server arm
// pins the recorded database on a read-only open, which refuses a missing
// database rather than creating one. present says the configured database
// exists (on the server, or as local Dolt data); count is meaningful only
// then. A workspace with no recorded metadata has nothing configured to
// count.
func countConfiguredIssues(ctx context.Context, id initIdentity) (count int, present bool, err error) {
	if id.RecordedErr != nil {
		return 0, false, fmt.Errorf("reading the workspace's metadata: %w", id.RecordedErr)
	}
	if id.Recorded == nil {
		return 0, false, nil
	}
	subject := id.Recorded.DoltDatabase
	if subject == "" {
		subject = configfile.DefaultDoltDatabase
	}
	if id.Configured.Server {
		return countConfiguredServerIssues(ctx, id, subject)
	}
	return countConfiguredEmbeddedIssues(ctx, id, subject)
}

// countConfiguredServerIssues is the server-mode arm of
// countConfiguredIssues. present comes first — Tier-1 local Dolt data under
// the workspace's own root, else the server's own database list — so a
// database bd can see data for counts even when the server cannot currently
// be reached to confirm it.
func countConfiguredServerIssues(ctx context.Context, id initIdentity, subject string) (int, bool, error) {
	present := initLocalDoltDataPresent(id.BeadsDir, subject)
	if !present {
		check := checkDatabaseOnServer(id.Configured.Host, id.Configured.Port,
			id.Recorded.GetDoltServerUser(), id.Recorded.GetDoltServerPassword(),
			subject, id.Recorded.GetDoltServerTLS())
		present = check.Reachable && check.Exists
	}
	if !present {
		return 0, false, nil
	}
	// A read-only open that pins the recorded database and the configured
	// endpoint. The preflight runs after runInit promoted the explicit
	// --server-* flags into the environment, so letting the endpoint fill
	// from that environment would count at the FLAGGED endpoint, not the
	// configured one; pinning host and port (as a caller assertion — the
	// count deliberately dials the configured endpoint, whatever promoted
	// env values now say) keeps the count on the workspace's own server.
	// DisableAutoStart keeps the count from starting a server: confirming a
	// destruction must not have side effects of its own.
	countCfg := &dolt.Config{
		ReadOnly:              true,
		RequireReadableSchema: true,
		Database:              subject,
		ServerHost:            id.Configured.Host,
		DisableAutoStart:      true,
	}
	if id.Configured.Port > 0 {
		countCfg.ServerPort = id.Configured.Port
		countCfg.ServerPortSource = doltserver.PortSourceCallerExplicit
	}
	store, err := dolt.NewFromConfigWithOptions(ctx, id.BeadsDir, countCfg)
	if err != nil {
		return 0, present, err
	}
	defer func() { _ = store.Close() }()
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, present, err
	}
	if stats == nil {
		return 0, present, nil
	}
	return stats.TotalIssues, present, nil
}

// countConfiguredEmbeddedIssues is the embedded-mode arm of
// countConfiguredIssues.
func countConfiguredEmbeddedIssues(ctx context.Context, id initIdentity, subject string) (int, bool, error) {
	if sanitized := sanitizeDBName(subject); sanitized != subject {
		subject = sanitized
	}
	present := false
	for _, name := range configfile.EmbeddedDoltDatabases(id.BeadsDir) {
		if strings.EqualFold(name, subject) {
			present = true
			break
		}
	}
	if !present {
		present = initLocalDoltDataPresent(id.BeadsDir, subject)
	}
	if !present {
		return 0, false, nil
	}
	store, err := embeddeddolt.OpenReadOnly(ctx, id.BeadsDir, subject, "main")
	if err != nil {
		return 0, present, err
	}
	defer func() { _ = store.Close() }()
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, present, err
	}
	if stats == nil {
		return 0, present, nil
	}
	return stats.TotalIssues, present, nil
}

// initLocalDoltDataPresent reports Tier-1 evidence that database exists as
// local Dolt data under the workspace's own root: a .dolt repository
// directory for it. It stats; it never opens or creates anything.
func initLocalDoltDataPresent(beadsDir, database string) bool {
	if database == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(doltserver.ResolveDoltDir(beadsDir), database, ".dolt"))
	return err == nil && info.IsDir()
}
