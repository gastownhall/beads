package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/utils"
)

// initDBRef names one database and where it lives.
type initDBRef struct {
	Database string // raw recorded or flagged name; "" = unset
	Server   bool
	Host     string
	Port     int // 0 = unknown

	portSource doltserver.PortSource
}

// databaseOrDefault is the database a store open of this ref selects.
func (r initDBRef) databaseOrDefault() string {
	if r.Database == "" {
		return configfile.DefaultDoltDatabase
	}
	return r.Database
}

// initIdentity is what bd init knows about the database it is about to
// replace (Configured: the one the workspace's commands open) and the one it
// is about to write (Target) (#6172). Init's guards reason about Configured;
// the metadata write records Target. Before this existed the guards read the
// env-first getters, so an exported BEADS_DOLT_SERVER_DATABASE could point the
// destroy count at a database that does not exist, and a failed count skips
// the confirmation.
type initIdentity struct {
	BeadsDir       string             // the .beads init writes (follows .beads/redirect)
	RedirectedFrom string             // the stub .beads when a redirect was followed
	Recorded       *configfile.Config // metadata.json at BeadsDir; nil = fresh
	Configured     initDBRef
	Target         initDBRef

	// ownPort is the workspace's own server port, read without the
	// BEADS_DOLT_SERVER_PORT overlay; 0 = unknown.
	ownPort int
}

// initIdentityFlags are the invocation's own database selectors.
type initIdentityFlags struct {
	Database string // --database; "" when not given
	Host     string // --server-host; "" when not given
	Port     int    // --server-port; 0 when not given
}

// resolveInitWorkspace resolves the .beads init writes and the database its
// commands open. It runs once, before any init flag is promoted into the
// environment, so the endpoint is the one the workspace resolves on its own.
func resolveInitWorkspace() initIdentity {
	var id initIdentity
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		id.BeadsDir = utils.CanonicalizePath(envBeadsDir)
	} else if fallback := beads.GetWorktreeFallbackBeadsDir(); fallback != "" {
		id.BeadsDir = fallback
	} else {
		localBeadsDir := filepath.Join(".", ".beads")
		id.BeadsDir = beads.FollowRedirect(localBeadsDir)
		if id.BeadsDir != localBeadsDir {
			id.RedirectedFrom = localBeadsDir
		}
	}
	if cfg, err := configfile.LoadForDiscovery(id.BeadsDir); err == nil {
		id.Recorded = cfg
	}
	if id.Recorded != nil {
		serverCfg := doltserver.DefaultConfig(id.BeadsDir)
		id.Configured = initDBRef{
			Database:   id.Recorded.DoltDatabase,
			Server:     id.Recorded.IsDoltServerMode(),
			Host:       id.Recorded.GetDoltServerHost(),
			Port:       serverCfg.Port,
			portSource: serverCfg.PortSource,
		}
		id.ownPort = id.Recorded.DoltServerPort
		if id.ownPort <= 0 {
			id.ownPort, _ = strconv.Atoi(strings.TrimSpace(config.GetYamlConfig("dolt.port")))
		}
	}
	return id
}

// resolveInitIdentity derives Target from the workspace's Configured
// database overlaid by the invocation's flags. It is pure.
func resolveInitIdentity(ws initIdentity, flags initIdentityFlags, serverMode bool) initIdentity {
	id := ws
	id.Target = id.Configured
	id.Target.Server = serverMode
	if flags.Database != "" {
		id.Target.Database = flags.Database
	}
	if flags.Host != "" {
		id.Target.Host = flags.Host
	}
	if flags.Port != 0 {
		// A port flag naming the effective or the workspace's own port
		// restates it, even over a stale exported BEADS_DOLT_SERVER_PORT
		// (GH#5177): both sides take the flag.
		if flags.Port == id.Configured.Port || (id.ownPort > 0 && flags.Port == id.ownPort) {
			id.Configured.Port = flags.Port
			id.Configured.portSource = doltserver.PortSourceCallerExplicit
		}
		id.Target.Port = flags.Port
		id.Target.portSource = doltserver.PortSourceCallerExplicit
	}
	return id
}

// sameDoltHost reports whether two host spellings name one server: any two
// spellings of bd's own server, or otherwise the same host ignoring case and
// brackets.
func sameDoltHost(a, b string) bool {
	if configfile.ClassifyDoltHost(a) == configfile.DoltHostBdServer && configfile.ClassifyDoltHost(b) == configfile.DoltHostBdServer {
		return true
	}
	return strings.EqualFold(configfile.UnbracketHost(a), configfile.UnbracketHost(b))
}

// location names where the database lives, for messages.
func (r initDBRef) location() string {
	if !r.Server {
		return "embedded"
	}
	return doltutil.PrintedServerAddr(r.Host, r.Port)
}

// Repoints reports whether this init moves an initialized workspace off the
// database its commands open today. A mode change (#3885) compares names only.
func (id initIdentity) Repoints() bool {
	if id.Recorded == nil {
		return false
	}
	c, t := id.Configured, id.Target
	if c.Database != "" && t.Database != "" && !strings.EqualFold(c.Database, t.Database) {
		return true
	}
	if c.Server && t.Server {
		if !sameDoltHost(c.Host, t.Host) {
			return true
		}
		if c.Port > 0 && t.Port != c.Port {
			return true
		}
	}
	return false
}

// printRepointNotice tells the operator which database this init moves the
// workspace off and onto. --quiet suppresses it, like #3885's mode notice.
func (id initIdentity) printRepointNotice(quiet bool) {
	if quiet || !id.Repoints() {
		return
	}
	fmt.Fprintf(os.Stderr, "Repointing this workspace from database %s (%s) to %s (%s).\n",
		remedy.PrintedValue(id.Configured.databaseOrDefault()), id.Configured.location(),
		remedy.PrintedValue(id.Target.databaseOrDefault()), id.Target.location())
}

// configuredRef is the database the guards check for a guard run at the
// init target. Through a redirect the guards still read the stub .beads, so
// they get no override there.
func (id initIdentity) configuredRef() *initDBRef {
	if id.Recorded == nil || id.RedirectedFrom != "" {
		return nil
	}
	ref := id.Configured
	return &ref
}

// countConfiguredIssues counts the issues in the configured database, the one
// the workspace's commands open, never the one an exported variable or an
// init flag names. Returns 0 for a fresh workspace. When the count fails,
// present reports whether the database is there all the same: its Tier-1
// directory exists (ADR-0004) or its server says it does.
func countConfiguredIssues(id initIdentity) (n int, present bool, err error) {
	if id.Recorded == nil {
		return 0, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := openConfiguredStoreForCount(ctx, id)
	if err != nil {
		return 0, configuredDatabasePresent(id), err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, true, err
	}
	if stats == nil {
		return 0, true, nil
	}
	return stats.TotalIssues, true, nil
}

// configuredDatabasePresent is ADR-0004's evidence that the configured
// database exists, gathered without opening it.
func configuredDatabasePresent(id initIdentity) bool {
	db := id.Configured.databaseOrDefault()
	roots := []string{filepath.Join(id.BeadsDir, "embeddeddolt")}
	if id.Configured.Server {
		roots = append(roots, doltserver.ResolveDoltDir(id.BeadsDir))
	}
	for _, root := range roots {
		if info, err := os.Stat(filepath.Join(root, db, ".dolt")); err == nil && info.IsDir() {
			return true
		}
	}
	if !id.Configured.Server {
		return false
	}
	check := checkDatabaseOnServer(id.Configured.Host, id.Configured.Port, id.Recorded.GetDoltServerUser(),
		id.Recorded.GetDoltServerPassword(), db, id.Recorded.GetDoltServerTLS())
	return check.Reachable && check.Exists && check.Err == nil
}

// initRepointUnreadableError refuses a repoint away from a database that is
// there but could not be read: bd cannot say what moving off it would leave
// behind. It is a plain refusal, not one of ADR-0002's exit codes, and no
// flag lifts it.
func initRepointUnreadableError(id initIdentity, readErr error) error {
	a := remedy.PrintedValue(id.Configured.databaseOrDefault())
	return fmt.Errorf("refusing to repoint this workspace from database %s (%s) to %s (%s): %s exists but could not be read: %w\n\n"+
		"Neither --recreate-missing nor --destroy-token lifts this. Repair %s and re-run bd init in place,\n"+
		"or move it aside so it is missing and --recreate-missing applies.\n"+
		"See docs/recovery/init-safety.md#init-repoint.",
		a, id.Configured.location(), remedy.PrintedValue(id.Target.databaseOrDefault()), id.Target.location(),
		a, readErr, a)
}

// openConfiguredStoreForCount opens the configured database read-only. A
// server-mode workspace pins the recorded database name and the endpoint
// resolved before init promoted its flags, so the open follows neither
// BEADS_DOLT_SERVER_DATABASE nor --server-host/--server-port, and creates
// nothing. Every other mode opens the workspace as its commands do.
func openConfiguredStoreForCount(ctx context.Context, id initIdentity) (storage.DoltStorage, error) {
	if id.Recorded.GetBackend() == configfile.BackendDolt && id.Configured.Server {
		cfg := &dolt.Config{
			Database:         id.Configured.databaseOrDefault(),
			ServerHost:       id.Configured.Host,
			ServerPort:       id.Configured.Port,
			ServerPortSource: id.Configured.portSource,
			ReadOnly:         true,
		}
		// The port was resolved before init promoted --server-port into
		// BEADS_DOLT_SERVER_PORT; a non-authoritative source would let the
		// promoted variable override it again.
		if cfg.ServerPort > 0 && !cfg.ServerPortSource.IsAuthoritative() {
			cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
		}
		return dolt.NewFromConfigWithOptions(ctx, id.BeadsDir, cfg)
	}
	return newDoltStoreFromConfig(ctx, id.BeadsDir)
}
