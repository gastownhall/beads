package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
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

// initDBRef names one Dolt database as bd init reasons about it, and how its
// server is dialed.
type initDBRef struct {
	Database string // raw recorded or flagged name; "" = unset
	Server   bool
	Host     string
	Port     int // 0 = unknown
	// PortSource is the step of the port rule that resolved Port, and
	// PortShared whether it is the shared server's port
	// (doltserver.Config.PortSource, PortSharedServer). The count opens Port
	// with them, as the workspace's commands do, so auto-start replaces a
	// stale port-file port but never one a setting or a flag asserts.
	PortSource doltserver.PortSource
	PortShared bool
	User       string // the server user it is dialed as
	TLS        bool
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
	// configuredEnv is the environment the workspace's commands dial
	// Configured from, kept when the flags move init to another server:
	// init's own endpoint, user and TLS flags are then for that server, not
	// this one. Nil when init dials Configured's endpoint itself, where its
	// flags reach both.
	configuredEnv initConnEnv
}

// initEndpoint is one resolution of a workspace's server endpoint.
type initEndpoint struct {
	Host       string
	Port       int
	PortSource doltserver.PortSource
	PortShared bool
}

// dialInitEndpoint is beadsDir's endpoint at host: the port its commands dial
// (doltserver.DialConfig) and where that port came from.
func dialInitEndpoint(beadsDir, host string) initEndpoint {
	dial := doltserver.DialConfig(beadsDir)
	return initEndpoint{Host: host, Port: dial.Port, PortSource: dial.PortSource, PortShared: dial.PortSharedServer}
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
	User           string       // the server user its commands dial as
	TLS            bool         // whether its commands dial with TLS
	ConnEnv        initConnEnv  // the connection variables its commands dial with
}

// initConnEnvKeys are the variables promoteExplicitServerConnFlags exports
// init's connection flags into.
var initConnEnvKeys = []string{
	"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_USER",
	"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_SERVER_TLS",
}

// initConnEnv is a saved value of each of initConnEnvKeys; a key that was
// unset is absent.
type initConnEnv map[string]string

// captureInitConnEnv saves initConnEnvKeys as they are set now.
func captureInitConnEnv() initConnEnv {
	env := initConnEnv{}
	for _, key := range initConnEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

// apply sets initConnEnvKeys back to env's saved values.
func (env initConnEnv) apply() {
	for _, key := range initConnEnvKeys {
		if value, ok := env[key]; ok {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// withConfiguredEnv runs fn in the environment the workspace's commands dial
// Configured from (configuredEnv), and puts init's own back afterwards. When
// init dials Configured's endpoint itself, fn runs as it is.
func (id initIdentity) withConfiguredEnv(fn func()) {
	if id.configuredEnv == nil {
		fn()
		return
	}
	promoted := captureInitConnEnv()
	id.configuredEnv.apply()
	defer promoted.apply()
	fn()
}

// initIdentityFlags are the explicit target selectors of this invocation;
// zero values mean the flag was not given.
type initIdentityFlags struct {
	Database string
	Host     string
	Port     int
	User     string
	TLS      *bool
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
// before promoteExplicitServerConnFlags, so the endpoints, the user and TLS
// setting, and the connection variables it records are what this
// environment's commands dial, not what init's own flags name.
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
	ws.Effective = dialInitEndpoint(ws.BeadsDir, cfg.GetDoltServerHost())
	withoutServerEndpointEnv(func() {
		ws.Own = dialInitEndpoint(ws.BeadsDir, cfg.GetDoltServerHost())
	})
	ws.User, ws.TLS = cfg.GetDoltServerUser(), cfg.GetDoltServerTLS()
	ws.ConnEnv = captureInitConnEnv()
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
// keep reading the endpoint init dials today, and a restated port is the
// operator's assertion of it. An unknown configured port (the workspace's own
// server is stopped, so no port file names the port it will start on) is
// restated by nothing: a --server-port names another server. When the flags
// move init to another server, Configured keeps the environment its commands
// dial it from (initIdentity.configuredEnv).
func resolveInitIdentity(ws initWorkspace, flags initIdentityFlags, targetServer bool) initIdentity {
	id := initIdentity{BeadsDir: ws.BeadsDir, RedirectedFrom: ws.RedirectedFrom, Recorded: ws.Recorded}
	configured := initDBRef{Server: ws.Server, Host: ws.Effective.Host, Port: ws.Effective.Port,
		PortSource: ws.Effective.PortSource, PortShared: ws.Effective.PortShared, User: ws.User, TLS: ws.TLS}
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
		target.Port, target.PortSource = flags.Port, doltserver.PortSourceCallerExplicit
		if flags.Port == ws.Effective.Port || (ws.Recorded != nil && flags.Port == ws.Own.Port) {
			configured.Port, configured.PortSource = flags.Port, doltserver.PortSourceCallerExplicit
		}
	}
	id.Configured, id.Target = configured, target
	id.applyDialFlags(flags)
	if id.movesEndpoint() {
		id.configuredEnv = ws.ConnEnv
	}
	return id
}

// initServerTLSFlag is the --server-tls value when the flag was given.
func initServerTLSFlag(cmd *cobra.Command) *bool {
	if !cmd.Flags().Changed("server-tls") {
		return nil
	}
	tls, _ := cmd.Flags().GetBool("server-tls")
	return &tls
}

// applyDialFlags applies --server-user and --server-tls, which say how init
// dials a server target (only then are they promoted into the environment).
// The configured side takes them while the target is the server the
// workspace already uses; across a move to another server the guards keep
// dialing the configured one the way the workspace's commands do.
func (id *initIdentity) applyDialFlags(flags initIdentityFlags) {
	if !id.Target.Server {
		return
	}
	if flags.User != "" {
		id.Target.User = flags.User
	}
	if flags.TLS != nil {
		id.Target.TLS = *flags.TLS
	}
	if !id.movesEndpoint() {
		id.Configured.User, id.Configured.TLS = id.Target.User, id.Target.TLS
	}
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
// database: a different name, or, when both sides are server mode, another
// server (movesEndpoint). A mode change (#3885) compares names only; a fresh
// workspace never repoints.
func (id initIdentity) Repoints() bool {
	if id.Recorded == nil {
		return false
	}
	c, t := id.Configured, id.Target
	if c.Database != "" && t.Database != "" && !strings.EqualFold(c.Database, t.Database) {
		return true
	}
	return c.Server && t.Server && id.movesEndpoint()
}

// movesEndpoint reports whether the target names another server than the
// configured one: a different host, or a port flag naming another port than
// the configured one, which it always does when that port is unknown.
func (id initIdentity) movesEndpoint() bool {
	c, t := id.Configured, id.Target
	return !sameInitDoltHost(c.Host, t.Host) || t.Port != c.Port
}

// configuredRef is the configured database for a guard to reason about, or
// nil when the workspace records none: the guard then keeps the getters'
// answer, as it does for its other callers.
func (id initIdentity) configuredRef() *initDBRef {
	if id.Recorded == nil {
		return nil
	}
	ref := id.Configured
	return &ref
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

// initServerWitness is how a server-mode guard asks a server whether a
// database exists: the database, the endpoint, and the credentials it dials
// that endpoint with.
type initServerWitness struct {
	database string
	host     string
	port     int
	user     string
	password string
	tls      bool
}

func (w initServerWitness) check() initGuardDBCheck {
	return checkDatabaseOnServer(w.host, w.port, w.user, w.password, w.database, w.tls)
}

// serverWitness asks for r's database at r's endpoint with the password kept
// for that endpoint, never the one for a server init's flags name.
func (r initDBRef) serverWitness() initServerWitness {
	return initServerWitness{
		database: r.subject(),
		host:     r.Host,
		port:     r.Port,
		user:     r.User,
		password: configfile.ServerPasswordFor(r.Host, r.Port),
		tls:      r.TLS,
	}
}

// initGuardWitness is what the server-mode guards witness at beadsDir. runInit
// passes its configured database (initIdentity.configuredRef), which they ask
// about at its own endpoint with that endpoint's credentials, whatever the
// connection flags promoted into the environment name (GH#6172). A nil
// configured keeps the getters' answer for the guards' other callers.
func initGuardWitness(beadsDir string, cfg *configfile.Config, configured *initDBRef) initServerWitness {
	if configured == nil {
		return initServerWitness{
			database: cfg.GetDoltDatabase(),
			host:     cfg.GetDoltServerHost(),
			port:     doltserver.DefaultConfig(beadsDir).Port,
			user:     cfg.GetDoltServerUser(),
			password: cfg.GetDoltServerPassword(),
			tls:      cfg.GetDoltServerTLS(),
		}
	}
	ref := *configured
	if ref.Port <= 0 {
		ref.Port = doltserver.DefaultConfig(beadsDir).Port
	}
	return ref.serverWitness()
}

// countConfiguredIssues counts the issues in the configured database at its
// configured endpoint. It never creates the database. present reports
// whether the database is known to exist (its Tier-1 directory, or the
// server listing it) even when the count fails.
func countConfiguredIssues(ctx context.Context, id initIdentity) (n int, present bool, err error) {
	switch {
	case id.BeadsDir == "":
		return 0, false, nil
	case id.Recorded != nil && id.Recorded.GetBackend() != configfile.BackendDolt:
		// A registered backend is neither Dolt shape below:
		// effectiveServerMode is false for it unless a shared server is
		// configured, and the embedded probe would count 0 and wave the
		// reinit through. The backend's own read-only open counts it.
		return countRegisteredBackendIssues(ctx, id.BeadsDir)
	case !id.Configured.Server:
		return countEmbeddedConfiguredIssues(ctx, id.BeadsDir, id.Configured.subject())
	}
	return countServerConfiguredIssues(ctx, id)
}

// countRegisteredBackendIssues counts a registered backend's issues through
// the non-mutating factory, which hands the open to the backend's
// OpenReadOnly. Only the backend knows where its data lives, so a failed
// open is an unknown count, not a present database.
func countRegisteredBackendIssues(ctx context.Context, beadsDir string) (int, bool, error) {
	store, err := newReadOnlyStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return 0, false, err
	}
	n, err := storeIssueCount(ctx, store)
	return n, true, err
}

// countEmbeddedConfiguredIssues counts the embedded database db. present is
// its Tier-1 directory.
func countEmbeddedConfiguredIssues(ctx context.Context, beadsDir, db string) (int, bool, error) {
	present := initDirExists(filepath.Join(beadsDir, "embeddeddolt", db, ".dolt"))
	if !slices.Contains(configfile.EmbeddedDoltDatabases(beadsDir), db) {
		if present {
			return 0, true, fmt.Errorf("embedded database %s has no readable Dolt repository", remedy.PrintedValue(filepath.Join(beadsDir, "embeddeddolt", db)))
		}
		return 0, false, nil
	}
	n, err := countEmbeddedInitIssues(ctx, beadsDir, db)
	return n, true, err
}

// countServerConfiguredIssues counts the configured server database at its
// configured endpoint, opened as the workspace's commands open it: in the
// environment they dial it from (initIdentity.withConfiguredEnv), so across a
// move the open presents their user and TLS setting, and auto-start starts
// the workspace's own server on its own port; and with the port's source, so
// an owned server that died uncleanly, leaving a port file that names a port
// where nothing listens, is restarted on a new port rather than refused.
// present is its Tier-1 directory or, when the count fails, the configured
// server listing it.
func countServerConfiguredIssues(ctx context.Context, id initIdentity) (int, bool, error) {
	ref := id.Configured
	db := ref.subject()
	present := initDirExists(filepath.Join(doltserver.DoltDirPath(id.BeadsDir), db, ".dolt"))
	// The open resolves the user itself: a caller-set user would switch off
	// a configured credential command (dolt.ApplyGatewayCredential).
	cfg := &dolt.Config{Database: db, ServerHost: ref.Host, ServerTLS: ref.TLS, ReadOnly: true}
	if ref.Port > 0 {
		cfg.ServerPort, cfg.ServerPortSource, cfg.ServerPortSharedServer = ref.Port, ref.PortSource, ref.PortShared
	}
	var store *dolt.DoltStore
	var err error
	id.withConfiguredEnv(func() { store, err = dolt.NewFromConfigWithOptions(ctx, id.BeadsDir, cfg) })
	if err != nil {
		if !present && id.Recorded != nil && ref.Port > 0 {
			check := ref.serverWitness().check()
			present = check.Reachable && check.Exists && check.Err == nil
		}
		return 0, present, err
	}
	n, err := storeIssueCount(ctx, store)
	return n, true, err
}

// storeIssueCount is the issue total of an open store, which it closes.
func storeIssueCount(ctx context.Context, store storage.DoltStorage) (int, error) {
	defer func() { _ = store.Close() }()
	stats, err := store.GetStatistics(ctx)
	if err != nil || stats == nil {
		return 0, err
	}
	return stats.TotalIssues, nil
}

func initDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// leavesOwnServer reports whether this init moves the workspace from the
// server bd runs for it to another server: one whose port is bd's port-file
// bookkeeping, or unknown because that server is stopped. The shared server
// serves other workspaces too, and a port a setting asserts keeps the
// workspace's commands there whatever init records, so neither is left.
func (id initIdentity) leavesOwnServer() bool {
	c := id.Configured
	movesServer := id.Recorded != nil && c.Server && id.Target.Server && id.movesEndpoint()
	return movesServer && !c.PortShared && (c.PortSource == doltserver.PortSourcePortFile || c.Port <= 0)
}

// stopLeftOwnServer stops the server bd runs for the workspace when this init
// moves the workspace off it (leavesOwnServer), as bd dolt stop does. Its
// port file outranks the port init records in metadata.json, so while it runs
// the workspace's commands keep reading and writing the database init leaves,
// and with the server stopped the count starts it. It returns the port the
// server listened on, or 0 when none ran.
func stopLeftOwnServer(id initIdentity) (int, error) {
	if !id.leavesOwnServer() {
		return 0, nil
	}
	port := 0
	if state, err := doltserver.IsRunning(id.BeadsDir); err == nil && state.Running {
		port = state.Port
	}
	if err := doltserver.IgnoreNotRunning(doltserver.Stop(id.BeadsDir)); err != nil {
		return 0, err
	}
	return port, nil
}

// initLeaveOwnServerError refuses a repoint whose workspace's own server
// could not be stopped: its port file would keep the workspace's commands on
// the configured database, whatever init records.
func initLeaveOwnServerError(id initIdentity, stopErr error) error {
	return fmt.Errorf("refusing to repoint this workspace from database %s to %s: its own Dolt server could not be stopped: %s\n"+
		"  While that server runs, this workspace's commands keep using %s, whatever init records.\n"+
		"  Stop it (bd dolt stop), then re-run bd init. See 'bd help init-safety' (init-repoint).",
		id.Configured.describe(), id.Target.describe(), remedy.PrintedError(stopErr), remedy.PrintedValue(id.Configured.subject()))
}

// printInitRepointNotice is the one line every repoint that proceeds prints,
// and a second when init stopped the workspace's own server to leave it
// (stopLeftOwnServer), listening on stoppedPort.
func printInitRepointNotice(id initIdentity, stoppedPort int, quiet bool) {
	if quiet || !id.Repoints() {
		return
	}
	fmt.Fprintf(os.Stderr, "Repointing this workspace from database %s to %s.\n", id.Configured.describe(), id.Target.describe())
	if stoppedPort > 0 {
		fmt.Fprintf(os.Stderr, "Stopped this workspace's own Dolt server for %s (%s).\n",
			remedy.PrintedValue(id.Configured.subject()), doltutil.PrintedServerAddr(id.Configured.Host, stoppedPort))
	}
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

// initRecordChanged reports whether the workspace's metadata no longer
// records the backend, database, mode or endpoint id was resolved from. init
// resolves its identity before it waits for the init gate, but its write path
// re-reads the metadata once it holds the gate, which is why the destroy
// count runs under the gate (bd-dswzy). An init that held the gate first may
// have repointed the workspace meanwhile, and the count must not be of one
// database while the write path re-initializes another.
func initRecordChanged(id initIdentity) bool {
	if id.Recorded == nil {
		return false
	}
	now, err := configfile.LoadForDiscovery(id.BeadsDir)
	if err != nil || now == nil {
		return true
	}
	was := id.Recorded
	return now.GetBackend() != was.GetBackend() || now.DoltDatabase != was.DoltDatabase || now.DoltMode != was.DoltMode ||
		now.DoltServerHost != was.DoltServerHost || now.DoltServerPort != was.DoltServerPort
}

// initRecordChangedError refuses a reinit whose workspace changed while it
// waited for the init gate.
func initRecordChangedError(id initIdentity) error {
	return fmt.Errorf("refusing to re-initialize: %s changed while this init waited for the workspace's init gate; re-run bd init",
		remedy.PrintedValue(configfile.MetadataFilePath(id.BeadsDir)))
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
