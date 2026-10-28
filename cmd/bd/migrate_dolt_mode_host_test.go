package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
)

// migrationHostDirection is one dolt-mode migration whose target or source
// is the workspace's local Dolt root (.beads/dolt, or the shared root).
type migrationHostDirection struct {
	name string
	// setup builds a workspace ready for the migration and returns its
	// .beads directory.
	setup func(t *testing.T) string
	run   func(dryRun bool) error
	// sourceMode is the dolt_mode the workspace has until the migration
	// switches it.
	isSourceMode func(*configfile.Config) bool
}

const validRepoState = `{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`

func migrationHostDirections() []migrationHostDirection {
	sharedRoot := func(t *testing.T) string {
		t.Helper()
		sharedDir := t.TempDir()
		t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
		root := filepath.Join(sharedDir, "dolt")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(validRepoState), 0o600))
		return root
	}
	isServer := func(c *configfile.Config) bool { return c.IsDoltServerMode() && !c.IsDoltProxiedServerMode() }
	isProxied := func(c *configfile.Config) bool { return c.IsDoltProxiedServerMode() }
	return []migrationHostDirection{
		{
			name: "from-server-to-proxied-server",
			setup: func(t *testing.T) string {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
				return migrateModeWorkspace(t, configfile.DoltModeServer)
			},
			run:          func(dryRun bool) error { return runMigrateToProxiedServer(dryRun, 0, false) },
			isSourceMode: isServer,
		},
		{
			name: "from-shared-server-to-proxied-server",
			setup: func(t *testing.T) string {
				sharedRoot(t)
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
				beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
				require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
				return beadsDir
			},
			run:          func(dryRun bool) error { return runMigrateToProxiedServer(dryRun, 0, true) },
			isSourceMode: isServer,
		},
		{
			name: "from-proxied-server-to-server",
			setup: func(t *testing.T) string {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
				beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
				require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: filepath.Join(beadsDir, "dolt")}))
				return beadsDir
			},
			run:          func(dryRun bool) error { return runMigrateFromProxiedServer(dryRun, false) },
			isSourceMode: isProxied,
		},
		{
			name: "from-proxied-server-to-shared-server",
			setup: func(t *testing.T) string {
				root := sharedRoot(t)
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
				beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
				require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root}))
				require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: false\n"), 0o600))
				return beadsDir
			},
			run:          func(dryRun bool) error { return runMigrateFromProxiedServer(dryRun, true) },
			isSourceMode: isProxied,
		},
	}
}

// migrationRefusedHosts are hosts bd does not run its server at, each with
// the words its refusal names it by.
var migrationRefusedHosts = []struct {
	host  string
	inEnv bool
	want  string
}{
	{"db.example.com", false, "is remote (db.example.com)"},
	{"::1", false, "is the IPv6 loopback (::1)"},
	{"[::1]", true, "is the IPv6 loopback ([::1])"},
	{"0:0:0:0:0:0:0:1", false, "is the IPv6 loopback (0:0:0:0:0:0:0:1)"},
	{"127.0.0.2", false, "is 127.0.0.2, a loopback address bd's own server does not listen on"},
}

func setMigrationHost(t *testing.T, beadsDir, host string, inEnv bool) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	if inEnv {
		t.Setenv("BEADS_DOLT_SERVER_HOST", host)
		return
	}
	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	cfg.DoltServerHost = host
	require.NoError(t, cfg.Save(beadsDir))
}

// TestMigrateDoltModeRefusesServerBdDoesNotRun pins the migration side of the
// host-ownership invariant (configfile.BdRunsServerAt) at every dolt-mode
// migration that re-points a workspace at, or away from, its local Dolt root.
// A forward migration would serve that root in place of the data of a server
// bd does not run; a reverse one would leave the root behind and dial that
// server. Each is refused before anything is written, on a fresh run, a dry
// run, and a run resumed from a journal prepared while the host was bd's
// (the journal records no host, so the refusal must not depend on it), and
// dolt_mode is left as it was.
func TestMigrateDoltModeRefusesServerBdDoesNotRun(t *testing.T) {
	for _, dir := range migrationHostDirections() {
		for _, h := range migrationRefusedHosts {
			for _, variant := range []string{"fresh", "dry-run", "resumed"} {
				t.Run(dir.name+"/"+h.host+"/"+variant, func(t *testing.T) {
					t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
					t.Setenv("BEADS_DOLT_SERVER_HOST", "")
					beadsDir := dir.setup(t)
					if variant == "resumed" {
						t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(migratePrepared))
						require.Error(t, dir.run(false))
						t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
						j, err := loadMigrateJournal(beadsDir)
						require.NoError(t, err)
						require.NotNil(t, j)
						require.Equal(t, migratePrepared, j.Phase)
					}
					setMigrationHost(t, beadsDir, h.host, h.inEnv)
					before := snapshotMigrationTree(t, beadsDir)

					var err error
					stderr := captureStderr(t, func() { err = dir.run(variant == "dry-run") })
					require.Error(t, err)
					assert.Contains(t, stderr, "the configured Dolt server host "+h.want)
					// A resumed run names the journal the earlier run left,
					// and how to resume it; a run without one names none.
					// bd finds the workspace from the canonical working
					// directory, so the path has its symlinks resolved (on
					// macOS the temp dir's /var is /private/var).
					resolvedBeadsDir, evalErr := filepath.EvalSymlinks(beadsDir)
					require.NoError(t, evalErr)
					journalNote := "left its journal at " + migrateJournalPath(resolvedBeadsDir)
					if variant == "resumed" {
						assert.Contains(t, stderr, journalNote)
						assert.Contains(t, stderr, "clearing the host resumes that migration")
					} else {
						assert.NotContains(t, stderr, "left its journal at")
					}
					assert.Equal(t, before, snapshotMigrationTree(t, beadsDir), "a refused migration must write nothing")
					cfg, loadErr := configfile.Load(beadsDir)
					require.NoError(t, loadErr)
					assert.True(t, dir.isSourceMode(cfg), "a refused migration must leave dolt_mode as it was (%q)", cfg.DoltMode)
				})
			}
		}
	}
}

// TestMigrateDoltModeAllowsEveryBdServerSpelling is the control: a host bd
// runs its server at, in any spelling, migrates, fresh or resumed.
func TestMigrateDoltModeAllowsEveryBdServerSpelling(t *testing.T) {
	for _, dir := range migrationHostDirections() {
		for _, host := range []string{"127.0.0.1", "LOCALHOST", "0.0.0.0", "::ffff:127.0.0.1"} {
			for _, resumed := range []bool{false, true} {
				name := dir.name + "/" + host + "/fresh"
				if resumed {
					name = dir.name + "/" + host + "/resumed"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
					t.Setenv("BEADS_DOLT_SERVER_HOST", "")
					beadsDir := dir.setup(t)
					setMigrationHost(t, beadsDir, host, false)
					if resumed {
						t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(migratePrepared))
						require.Error(t, dir.run(false))
						t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
					}
					require.NoError(t, dir.run(false))
					cfg, err := configfile.Load(beadsDir)
					require.NoError(t, err)
					assert.False(t, dir.isSourceMode(cfg), "the migration must switch dolt_mode (%q)", cfg.DoltMode)
				})
			}
		}
	}
}
