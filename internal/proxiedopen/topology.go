package proxiedopen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
)

// Topology is everything the two unit-of-work providers need that differs
// between workspaces: which database, whose schema, where the proxy listens,
// and — when the Dolt SQL server is not one Beads starts itself — how to
// reach it.
//
// It exists so the two ways a workspace can describe its SQL server (the
// proxied-server sidecar, and server mode's Dolt connection settings) meet in
// one shape before anything is constructed. The construction (OpenTopology)
// is then identical for both, which is the point: a proxied workspace and a
// server-mode workspace differ in where the answer is written down, not in
// what is built.
type Topology struct {
	Database   string
	TeamServer bool
	// ExpectedProjectID is the workspace identity the open asserts against
	// the selected database: always for a team-server workspace, and for a
	// plain proxied workspace only on a --database/--db excursion to a
	// database other than the configured one (see ResolveProxiedTopology).
	// Empty means nothing to assert: the caller adopts
	// (AdoptWorkspaceIdentity), or a plain workspace opens its configured
	// database. Server mode never sets it.
	ExpectedProjectID string
	ProxyPort         int
	ProxyIdle         time.Duration
	External          *configfile.ExternalDoltConfig
	RootPassword      string
}

// ResolveProxiedTopology reads a proxied-server workspace's topology out of
// metadata.json and the proxied-server sidecar. An ABSENT file is not fatal —
// both loads return (nil, nil) then, the defaults apply, and the provider
// construction is where a workspace that cannot be reached actually fails (a
// managed-local default whose root holds no database is refused just before
// that, by FreshStoreRefusal, so the construction never mints one). A file
// that EXISTS but cannot be read or parsed IS fatal (restores f880a985b,
// reverted in #4418; bd-aj3g5): swallowing it silently falls back to a fresh
// managed local database — reads return zero issues and writes land in the
// wrong database (split-brain) — and the identity assertion silently degrades
// to no assertion at all.
func ResolveProxiedTopology(beadsDir, databaseOverride string, posture IdentityPosture) (Topology, error) {
	persisted, err := configfile.Load(beadsDir)
	if err != nil {
		return Topology{}, fmt.Errorf(
			"corrupt workspace config %s: %w — refusing to fall back to a fresh database; repair or remove the file to proceed",
			configfile.ConfigPath(beadsDir), err)
	}
	topology := Topology{Database: configfile.DefaultDoltDatabase}
	if persisted != nil {
		topology.Database = persisted.GetDoltDatabase()
		topology.TeamServer = persisted.IsTeamServerManaged()
		if posture == AssertWorkspaceIdentity {
			topology.ExpectedProjectID = persisted.ProjectID
			// An empty local identity would reach checkTeamServerIdentity as
			// "nothing to assert" — indistinguishable from the adoption path,
			// which would silently disable the guard. A team-server workspace
			// always has an adopted ProjectID after a successful init, so an
			// empty one here is a broken workspace, not a legacy one.
			if topology.TeamServer && topology.ExpectedProjectID == "" {
				return Topology{}, fmt.Errorf(
					"newProxiedServerUOWProvider: this team-server workspace has no project identity in %s; re-run 'bd init --team-server' to adopt the identity provisioned in the shared database (it never writes to that database)",
					configfile.ConfigFileName)
			}
		}
	}
	// A plain (non-team-server) workspace asserts its identity only on an
	// explicit excursion: a --database/--db override naming a database other
	// than the configured one (gastownhall/beads#5572). Opening the configured
	// database compares nothing, because plain init does not persist an
	// identity it adopted from an existing database, so a co-tenant
	// workspace's local ID can be stale; asserting it there would refuse
	// workspaces that work today. Schema names compare case-insensitively,
	// matching MySQL. BEADS_SKIP_IDENTITY_CHECK=1 is the deliberate
	// cross-project hatch, as on the pre-open check in cmd/bd/main.go; it
	// never reaches the team-server guard, which stays strict.
	if !topology.TeamServer && (databaseOverride == "" ||
		strings.EqualFold(databaseOverride, topology.Database) ||
		os.Getenv("BEADS_SKIP_IDENTITY_CHECK") == "1") {
		topology.ExpectedProjectID = ""
	}
	if databaseOverride != "" {
		topology.Database = databaseOverride
	}

	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return Topology{}, fmt.Errorf(
			"corrupt proxied-server sidecar %s: %w — refusing to fall back to a fresh database; repair or remove the file to proceed",
			configfile.ProxiedServerClientInfoPath(beadsDir), err)
	}
	if info != nil {
		topology.ProxyPort = info.Port
		topology.ProxyIdle = info.IdleTimeout
		if info.External != nil {
			topology.External = info.External
			topology.RootPassword = os.Getenv(configfile.ExternalDoltPasswordEnvVar)
		}
	}
	return topology, nil
}

// FreshStoreRefusal is the proxied-mode twin of
// externallyManagedFreshStartRefusal (cmd/bd/dolt.go): a managed-local open
// whose resolved root holds no Dolt database is refused, instead of letting
// the provider run `dolt init` and CREATE DATABASE over an empty directory.
//
// The case it closes: a repository that tracks .beads/metadata.json with
// dolt_mode=proxied-server and leaves the sidecar untracked, because the
// sidecar names a machine-local server. A fresh clone of it carries the mode
// but no sidecar and no store. Before this check every bd command there
// silently minted a private, empty database and then failed on it one table
// at a time ("table not found: leases", "run bd migrate schema"), never
// saying the clone had not joined anything. The same shape covers a
// managed-local workspace whose root was deleted: the data is gone, and a
// fresh database in its place would hide that behind zero rows.
//
// An absent sidecar is NOT the signal. A default `bd init --proxied-server`
// writes no sidecar at all (buildProxiedServerClientInfo returns nil when
// every field is default), so the ordinary managed-local workspace has none.
// The signal is the resolved root — env, sidecar root_path or the default —
// holding no database, read with the same HasDoltDatabase that dolt.go uses.
// External topologies never create a store and are exempt; init is the one
// caller allowed to create one and bypasses this with AllowFreshStore.
func FreshStoreRefusal(beadsDir string, topology Topology) error {
	if topology.External != nil {
		return nil
	}
	root, err := ResolveRootPath(beadsDir)
	if err != nil {
		return fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if doltserver.HasDoltDatabase(root) {
		return nil
	}
	consequence := "Starting a managed server there would run 'dolt init' and create a new, empty database; " +
		"every later bd command in this workspace would then read and write that empty database instead of the one it was meant to share."
	initRemedies := "\nTo restore it from a bd backup: bd init --proxied-server, then bd backup restore --force <dir>" +
		"\nTo create a new, empty database here: bd init --proxied-server"
	if topology.TeamServer {
		// beads-team-server provisions a team-server database and bd never
		// creates one (init only adopts it), so neither init remedy can
		// bring this workspace back: the sidecar or the served root can.
		consequence = "Its database belongs to beads-team-server (dolt_team_server in " + configfile.ConfigFileName +
			"), which provisions it; bd never creates a team-server database, so no bd command can make one here."
		initRemedies = ""
	}
	// The root can come from metadata.json, which every clone carries, so it
	// is spelled by remedy.PrintedValue: a line break in it would otherwise
	// print as remedy lines of its own.
	return fmt.Errorf("refusing to start a proxied Dolt server: %[1]s holds no Dolt database\n\n"+
		"This workspace is in proxied-server mode and %[2]s. %[4]s\n\n"+
		"If this is a clone of a workspace that shares a Dolt server, restore its %[3]s sidecar (usually untracked, since it names a machine-local server) "+
		"or set BEADS_PROXIED_SERVER_ROOT_PATH to the served root. If the database was moved or deleted, restore it to %[1]s.%[5]s",
		remedy.PrintedValue(root), rootProvenance(beadsDir), configfile.ProxiedServerClientInfoFileName, consequence, initRemedies)
}

// rootProvenance says where the managed-local root came from, for the
// refusal text: the operator repairs a different thing for each source. It
// mirrors ResolveProxiedServerRootPath's precedence (env, sidecar, then
// DoltDirPath's own sources, dataDirProvenance); a sidecar read error is not
// reported here because ResolveRootPath has already failed the open on it.
func rootProvenance(beadsDir string) string {
	if os.Getenv("BEADS_PROXIED_SERVER_ROOT_PATH") != "" {
		return "that root is named by BEADS_PROXIED_SERVER_ROOT_PATH"
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	switch {
	case err != nil:
		return dataDirProvenance(beadsDir)
	case info != nil && info.RootPath != "":
		return "that root is named by root_path in " + configfile.ProxiedServerClientInfoFileName
	case info != nil:
		return dataDirProvenance(beadsDir) + "; " + configfile.ProxiedServerClientInfoFileName + " names neither another root nor an external server"
	default:
		return dataDirProvenance(beadsDir) + "; there is no " + configfile.ProxiedServerClientInfoFileName + " sidecar naming another root or an external server"
	}
}

// dataDirProvenance names the source of a root neither the env nor the
// sidecar chose, in DoltDirPath's precedence: shared-server mode,
// BEADS_DOLT_DATA_DIR, then metadata.json's dolt_data_dir or an absolute
// database, and only then the default <beadsDir>/dolt. Calling every one of
// them "the default" sent the operator to the sidecar instead of the setting
// that chose the root.
func dataDirProvenance(beadsDir string) string {
	if _, err := doltserver.SharedDoltPath(); err == nil && doltserver.IsSharedServerMode() {
		return "that root is the shared server's data directory (shared-server mode)"
	}
	if os.Getenv("BEADS_DOLT_DATA_DIR") != "" {
		return "that root is named by BEADS_DOLT_DATA_DIR"
	}
	// DoltDirPath reads metadata.json only when it exists (a legacy
	// config.json alone leaves the default), and so does this, in place.
	if _, err := os.Stat(configfile.ConfigPath(beadsDir)); err != nil {
		return "that root is the default"
	}
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	switch {
	case err != nil || cfg == nil:
		return "that root is the default"
	case cfg.DoltDataDir != "":
		return "that root is named by dolt_data_dir in " + configfile.ConfigFileName
	case filepath.IsAbs(cfg.Database):
		return "that root is named by database in " + configfile.ConfigFileName
	default:
		return "that root is the default"
	}
}
