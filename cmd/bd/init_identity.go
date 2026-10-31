package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/utils"
)

// bd init reasons about two databases (GH#6172). The workspace's
// metadata.json names the one its commands use today (Configured); the
// --database / --server-host / --server-port flags name the one this init
// opens and records (Target). Before this record existed each guard
// re-derived an identity of its own after the connection flags had been
// promoted into the environment, so the destroy count, the missing-database
// witness and the write path could each be about a different database, and
// a count of 0 from one of them waved through a repoint that orphaned the
// populated other. Every guard now reads this one record: the ADR-0004
// guards and the destroy-N count prove things about Configured at its own
// endpoint, whatever the flags name, and a Target that differs is an
// explicit repoint (initIdentity.Repoints).

// initDBRef names one Dolt database as bd init reasons about it.
type initDBRef struct {
	Database string // raw recorded or flagged name; "" = unset
	Server   bool
	Host     string
	Port     int // 0 = unknown
}

// initIdentity is the one record of which database bd init is leaving and
// which one it is about to open, resolved once per invocation
// (resolveInitIdentity) and passed by parameter to every guard.
type initIdentity struct {
	BeadsDir       string             // the .beads init writes (follows .beads/redirect)
	RedirectedFrom string             // the stub .beads when a redirect was followed
	Recorded       *configfile.Config // LoadForDiscovery(BeadsDir); nil = fresh
	Configured     initDBRef          // what this workspace's commands resolve today
	Target         initDBRef          // what this init will open and record
}

// initEndpoint is one resolution of a workspace's server endpoint.
type initEndpoint struct {
	Host string
	Port int
}

// initWorkspace is what resolveInitWorkspace reads from disk and the
// environment; resolveInitIdentity is pure over it.
type initWorkspace struct {
	BeadsDir       string
	RedirectedFrom string
	Recorded       *configfile.Config
	Server         bool         // the workspace's commands open a server store
	Effective      initEndpoint // with the ambient BEADS_DOLT_SERVER_* overlay
	Own            initEndpoint // the workspace's own settings, overlay removed
}

// initIdentityFlags are the explicit target selectors of this invocation;
// zero values mean the flag was not given.
type initIdentityFlags struct {
	Database string
	Host     string
	Port     int
}

// initTargetBeadsDir is the .beads directory bd init writes: BEADS_DIR, the
// worktree fallback, or ./.beads with its redirect followed. It is the one
// rule both the write path and resolveInitWorkspace use; redirectedFrom is
// the stub when a redirect was followed.
func initTargetBeadsDir() (beadsDir, redirectedFrom string) {
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		return utils.CanonicalizePath(envBeadsDir), ""
	}
	if fallback := beads.GetWorktreeFallbackBeadsDir(); fallback != "" {
		return fallback, ""
	}
	localBeadsDir := filepath.Join(".", ".beads")
	beadsDir = beads.FollowRedirect(localBeadsDir)
	if beadsDir != localBeadsDir {
		redirectedFrom = localBeadsDir
	}
	return beadsDir, redirectedFrom
}

// resolveInitWorkspace reads the workspace bd init targets. It must run
// before promoteExplicitServerConnFlags, so the endpoints it records are what
// this environment's commands dial, not what init's own flags name.
func resolveInitWorkspace() initWorkspace {
	beadsDir, redirectedFrom := initTargetBeadsDir()
	ws := initWorkspace{BeadsDir: absInitPath(beadsDir)}
	if redirectedFrom != "" {
		ws.RedirectedFrom = absInitPath(redirectedFrom)
	}
	cfg, err := configfile.LoadForDiscovery(ws.BeadsDir)
	if err != nil || cfg == nil {
		// Unreadable metadata is checkExistingBeadsDataAt's to refuse;
		// without it there is no recorded identity to protect here.
		return ws
	}
	ws.Recorded = cfg
	ws.Server = effectiveServerMode(ws.BeadsDir, cfg)
	ws.Effective = initEndpoint{Host: cfg.GetDoltServerHost(), Port: doltserver.DialConfig(ws.BeadsDir).Port}
	withoutServerEndpointEnv(func() {
		ws.Own = initEndpoint{Host: cfg.GetDoltServerHost(), Port: doltserver.DialConfig(ws.BeadsDir).Port}
	})
	return ws
}

func absInitPath(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// withoutServerEndpointEnv runs fn with the ambient endpoint overlay
// (BEADS_DOLT_SERVER_HOST/PORT and the legacy BEADS_DOLT_PORT) removed, and
// puts it back exactly as it was. It only reads: a flag that restates the
// workspace's own endpoint over a stale exported one is not a repoint
// (GH#5177).
func withoutServerEndpointEnv(fn func()) {
	keys := []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"}
	saved := map[string]string{}
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			saved[key] = value
			_ = os.Unsetenv(key)
		}
	}
	defer func() {
		for key, value := range saved {
			_ = os.Setenv(key, value)
		}
	}()
	fn()
}

// resolveInitIdentity overlays the explicit flags on the workspace's
// configured identity. A flag that restates the effective value or the
// workspace's own value is not a change: both sides take it, so the guards
// keep reading the endpoint init dials today.
func resolveInitIdentity(ws initWorkspace, flags initIdentityFlags, targetServer bool) initIdentity {
	id := initIdentity{BeadsDir: ws.BeadsDir, RedirectedFrom: ws.RedirectedFrom, Recorded: ws.Recorded}
	configured := initDBRef{Server: ws.Server, Host: ws.Effective.Host, Port: ws.Effective.Port}
	if ws.Recorded != nil {
		// Raw on purpose: init's open and its metadata write ignore
		// BEADS_DOLT_SERVER_DATABASE, so the count must too.
		configured.Database = ws.Recorded.DoltDatabase
	}
	target := configured
	target.Server = targetServer
	if flags.Database != "" {
		target.Database = flags.Database
	}
	if flags.Host != "" {
		target.Host = flags.Host
		if sameInitDoltHost(flags.Host, ws.Effective.Host) || (ws.Recorded != nil && sameInitDoltHost(flags.Host, ws.Own.Host)) {
			configured.Host = flags.Host
		}
	}
	if flags.Port != 0 {
		target.Port = flags.Port
		if configured.Port == 0 || flags.Port == ws.Effective.Port || (ws.Recorded != nil && flags.Port == ws.Own.Port) {
			configured.Port = flags.Port
		}
	}
	id.Configured, id.Target = configured, target
	return id
}

// sameInitDoltHost reports whether two host spellings reach the same server:
// any two spellings of bd's own address are one host, otherwise the
// unbracketed names compare case-insensitively.
func sameInitDoltHost(a, b string) bool {
	if configfile.BdRunsServerAt(a) && configfile.BdRunsServerAt(b) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(configfile.UnbracketHost(a)), strings.TrimSpace(configfile.UnbracketHost(b)))
}

// Repoints reports whether this init moves a recorded workspace to another
// database: a different name, or, when both sides are server mode, a
// different host or a port flag that changes a known configured port. A mode
// change (#3885) compares names only; a fresh workspace never repoints.
func (id initIdentity) Repoints() bool {
	if id.Recorded == nil {
		return false
	}
	c, t := id.Configured, id.Target
	if c.Database != "" && t.Database != "" && !strings.EqualFold(c.Database, t.Database) {
		return true
	}
	if !c.Server || !t.Server {
		return false
	}
	if !sameInitDoltHost(c.Host, t.Host) {
		return true
	}
	return c.Port > 0 && t.Port != c.Port
}

// subject is the database name ref resolves to when it is opened.
func (r initDBRef) subject() string {
	if r.Database != "" {
		return r.Database
	}
	return configfile.DefaultDoltDatabase
}

// describe renders ref for operator text: the name, and the endpoint in
// server mode.
func (r initDBRef) describe() string {
	name := remedy.PrintedValue(r.subject())
	if !r.Server {
		return name
	}
	if r.Port <= 0 {
		return fmt.Sprintf("%s (%s)", name, remedy.PrintedValue(r.Host))
	}
	return fmt.Sprintf("%s (%s)", name, doltutil.PrintedServerAddr(r.Host, r.Port))
}

// countConfiguredIssues counts the issues in the configured database at its
// configured endpoint. It never creates the database. present reports
// whether the database is known to exist (its Tier-1 directory, or the
// server listing it) even when the count fails.
func countConfiguredIssues(ctx context.Context, id initIdentity) (n int, present bool, err error) {
	ref := id.Configured
	db := ref.subject()
	if id.BeadsDir == "" {
		return 0, false, nil
	}
	if !ref.Server {
		present = initDirExists(filepath.Join(id.BeadsDir, "embeddeddolt", db, ".dolt"))
		member := false
		for _, name := range configfile.EmbeddedDoltDatabases(id.BeadsDir) {
			if name == db {
				member = true
				break
			}
		}
		if !member {
			if present {
				return 0, true, fmt.Errorf("embedded database %s has no readable Dolt repository", remedy.PrintedValue(filepath.Join(id.BeadsDir, "embeddeddolt", db)))
			}
			return 0, false, nil
		}
		n, err = countEmbeddedInitIssues(ctx, id.BeadsDir, db)
		return n, true, err
	}

	present = initDirExists(filepath.Join(doltserver.DoltDirPath(id.BeadsDir), db, ".dolt"))
	var store storage.DoltStorage
	if id.Recorded != nil && id.Recorded.GetBackend() != configfile.BackendDolt {
		store, err = newDoltStoreFromConfig(ctx, id.BeadsDir)
	} else {
		cfg := &dolt.Config{Database: db, ServerHost: ref.Host, ReadOnly: true}
		if ref.Port > 0 {
			cfg.ServerPort = ref.Port
			cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
		}
		var doltStore *dolt.DoltStore
		doltStore, err = dolt.NewFromConfigWithOptions(ctx, id.BeadsDir, cfg)
		if err == nil {
			store = doltStore
		}
	}
	if err != nil {
		if !present && id.Recorded != nil && ref.Port > 0 {
			check := checkDatabaseOnServer(ref.Host, ref.Port, id.Recorded.GetDoltServerUser(), id.Recorded.GetDoltServerPassword(), db, id.Recorded.GetDoltServerTLS())
			present = check.Reachable && check.Exists && check.Err == nil
		}
		return 0, present, err
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

func initDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// printInitRepointNotice is the one line every repoint that proceeds prints.
func printInitRepointNotice(id initIdentity, quiet bool) {
	if quiet || !id.Repoints() {
		return
	}
	fmt.Fprintf(os.Stderr, "Repointing this workspace from database %s to %s.\n", id.Configured.describe(), id.Target.describe())
}

// initRepointUnreadableError refuses a repoint away from a configured
// database that exists but cannot be read: its issue count is the only
// thing the destroy confirmation could promise about, so neither a destroy
// token nor --recreate-missing lifts it.
func initRepointUnreadableError(id initIdentity, readErr error) error {
	return fmt.Errorf("refusing to repoint this workspace from database %s to %s: %s exists but could not be read: %s\n"+
		"  Its issues cannot be counted, so init cannot confirm what would stop being reachable from this workspace.\n"+
		"  Repair or re-initialize %s in place (bd init --reinit-local without --database, --server-host or --server-port),\n"+
		"  or move it aside so it is genuinely missing, where --recreate-missing applies.\n"+
		"  See 'bd help init-safety' (init-repoint).",
		id.Configured.describe(), id.Target.describe(), remedy.PrintedValue(id.Configured.subject()), remedy.PrintedError(readErr),
		remedy.PrintedValue(id.Configured.subject()))
}

var errInitCountTimeout = errors.New("timed out counting the configured database's issues")

// countConfiguredIssuesWithTimeout is countConfiguredIssues under the 5s
// budget the reinit preflight has always given its count.
func countConfiguredIssuesWithTimeout(id initIdentity) (int, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, present, err := countConfiguredIssues(ctx, id)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w: %w", errInitCountTimeout, err)
	}
	return n, present, err
}
