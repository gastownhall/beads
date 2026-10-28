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
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
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
	return configured != id.Target.Database ||
		id.Configured.Server != id.Target.Server ||
		!initHostsEqual(id.Configured.Host, id.Target.Host) ||
		id.Configured.Port != id.Target.Port
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
