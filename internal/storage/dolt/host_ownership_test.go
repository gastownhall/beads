package dolt

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// storeHostSpellings covers each host kind in the spellings that used to get
// different answers from the store's private host list and from configfile.
var storeHostSpellings = []string{
	"", "127.0.0.1", "localhost", "LOCALHOST", " localhost ", "0.0.0.0", "[127.0.0.1]", "::ffff:127.0.0.1",
	"::1", "[::1]", "0:0:0:0:0:0:0:1", "::",
	"127.0.0.2",
	"db.example.com", "10.0.0.5", "2001:db8::1",
}

// leftoverDatabaseStore is a server-mode store whose .beads/dolt/<db> holds a
// Dolt database that records an origin remote, as a clone bd init left there
// does. beadsDir is the workspace whose server state files the store reads.
func leftoverDatabaseStore(t *testing.T, beadsDir string, cfg *Config) *DoltStore {
	t.Helper()
	store := &DoltStore{serverMode: true, beadsDir: beadsDir, dbPath: filepath.Join(beadsDir, "dolt"), database: "active", cfg: cfg}
	if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt"), 0o755); err != nil {
		t.Fatalf("create a Dolt database in %s: %v", store.CLIDir(), err)
	}
	state := `{"remotes":{"origin":{"name":"origin","url":"git+ssh://git@example.com/org/repo.git"}}}`
	if err := os.WriteFile(filepath.Join(store.CLIDir(), ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("record a remote in %s: %v", store.CLIDir(), err)
	}
	return store
}

// testServedPort is the port the stand-in Dolt server of the tests below
// listens on.
const testServedPort = 3307

// serveDoltDir records in dir/.dolt the server info file `dolt sql-server`
// keeps in the data directory it serves while it runs (pid:port:secret), so
// that a dolt CLI command run in a database under dir that the server holds
// locked is handed to that server. Alone it is what a copy of a served
// directory carries; serveDoltDatabase adds the server's lock.
func serveDoltDir(t *testing.T, dir string, pid, port int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := strconv.Itoa(pid) + ":" + strconv.Itoa(port) + ":secret"
	if err := os.WriteFile(filepath.Join(dir, ".dolt", doltserver.ServerInfoFileName), []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
}

// serveDoltDatabase makes store's CLIDir the database a live server on port
// serves, as doltserver.ServesDatabaseDirOnPort proves it: dir/.dolt holds
// the server's info file (serveDoltDir), the tests' own process standing in
// for the server, and the database is locked as the server holds it. A dolt
// CLI command run in CLIDir from then on goes to that server, which the tests
// do not run, so a test whose route runs one there uses
// standInForServedDatabase instead.
func serveDoltDatabase(t *testing.T, store *DoltStore, dir string, port int) {
	t.Helper()
	serveDoltDir(t, dir, os.Getpid(), port)
	testutil.HoldDoltDatabaseLock(t, store.CLIDir())
}

// standInForServedDatabase makes cliDirServesServer count dir as the
// database a live server on port serves, for a test whose route then runs
// the dolt CLI there: under a real server's lock that CLI would hand its
// commands to the server. TestStoreHostOwnershipDecisionsAgree and package
// doltserver pin the proof itself.
func standInForServedDatabase(t *testing.T, dir string, port int) {
	t.Helper()
	orig := servesDatabaseDirOnPort
	servesDatabaseDirOnPort = func(d string, p int) bool { return d == dir && p == port }
	t.Cleanup(func() { servesDatabaseDirOnPort = orig })
}

// checkLeftoverDatabaseDecisions asserts that the CLI routes take the
// leftover database as the server's (hasCLIDatabase), and that its recorded
// remote counts as the server's (PersistedRemoteInfos, HasPersistedRemote),
// exactly when want says the database can be the server's; otherwise the
// remote is the leftover database's (LeftoverRemotes), which the push/pull
// no-remote decision names instead of reporting none. The #4259
// remote-migrate gate reads that remote for every server.
func checkLeftoverDatabaseDecisions(t *testing.T, store *DoltStore, want bool, what string) {
	t.Helper()
	if got := store.hasCLIDatabase(); got != want {
		t.Errorf("hasCLIDatabase(%s) = %v, want %v: the dolt CLI may move %s as the server's data only where it can be", what, got, want, store.CLIDir())
	}
	if got := len(store.PersistedRemoteInfos()) > 0; got != want {
		t.Errorf("PersistedRemoteInfos(%s) lists the leftover database's origin = %v, want %v", what, got, want)
	}
	if got := store.HasPersistedRemote(); got != want {
		t.Errorf("HasPersistedRemote(%s) = %v, want %v", what, got, want)
	}
	dir, leftover := store.LeftoverRemotes()
	if listed := len(leftover) == 1 && leftover[0].Name == "origin" && dir == store.CLIDir(); listed == want {
		t.Errorf("LeftoverRemotes(%s) = (%q, %+v), want the origin %s records = %v", what, dir, leftover, store.CLIDir(), !want)
	}
	if !store.hasPersistedCLIRemote() {
		t.Errorf("hasPersistedCLIRemote(%s) = false: the remote-migrate gate must see the recorded remote whatever the server", what)
	}
}

// TestStoreHostOwnershipDecisionsAgree pins every store decision about
// whether bd runs the Dolt server at a host to configfile.BdRunsServerAt:
// auto-start, the local data directory, the directory push, pull and fetch
// run the dolt CLI in, the remotes recorded there counting as the server's,
// the shared-database consent gate, the port file, and the advice that names
// `bd dolt start`. They must agree for every spelling; before one predicate
// decided them all, LOCALHOST and 0.0.0.0 got bd-start advice but no
// auto-start, and ::1 the reverse in places.
func TestStoreHostOwnershipDecisionsAgree(t *testing.T) {
	originalEnsure := ensureResolvedPortFile
	t.Cleanup(func() { ensureResolvedPortFile = originalEnsure })

	for _, host := range storeHostSpellings {
		t.Run("host="+host, func(t *testing.T) {
			clearPredicateEnv(t)
			clearSizingModeEnv(t)
			bd := configfile.BdRunsServerAt(host)

			if got := isExternalServerHost(host); got == bd {
				t.Errorf("isExternalServerHost(%q) = %v, want %v", host, got, !bd)
			}
			if got := doltserver.UnresolvedPortAdvice(host) == ""; got != bd {
				t.Errorf("UnresolvedPortAdvice(%q) empty = %v, want %v", host, got, bd)
			}
			if got := serverOpenCanAutoStart(&Config{AutoStart: true, Path: "/unused", ServerHost: host}); got != bd {
				t.Errorf("serverOpenCanAutoStart(host %q) = %v, want %v", host, got, bd)
			}

			beadsDir := t.TempDir()
			root := filepath.Join(beadsDir, "dolt")
			dataDir := resolveLocalActiveDatabaseDir(&Config{
				Path: root, BeadsDir: beadsDir, Database: "active", ServerHost: host, AutoStart: true,
			})
			if got := dataDir != ""; got != bd {
				t.Errorf("resolveLocalActiveDatabaseDir(host %q) = %q; a local data dir must be counted only for a server bd runs (want %v)", host, dataDir, bd)
			}

			// A Dolt database left in .beads/dolt/<db> is the server's only
			// where bd runs the server and a live server on the dialed port
			// serves it; elsewhere the CLI routes must not push or pull it in
			// the server's place, nor its remotes stand in for the server's.
			// A copy no server holds counts for nothing even at bd's host:
			// the dolt CLI would work on its files directly. Where a server
			// on the dialed port does serve it, bd's own server keeps the
			// directory and its recorded remotes (the GH#2118 veto).
			unserved := leftoverDatabaseStore(t, t.TempDir(), &Config{ServerHost: host, ServerPort: testServedPort})
			checkLeftoverDatabaseDecisions(t, unserved, false, "host "+host+", a copy nothing serves")
			served := leftoverDatabaseStore(t, t.TempDir(), &Config{ServerHost: host, ServerPort: testServedPort})
			serveDoltDatabase(t, served, served.dbPath, testServedPort)
			checkLeftoverDatabaseDecisions(t, served, bd, "host "+host+", served on the dialed port")

			cfg := &Config{BeadsDir: managedWorkspace(t, 3307), ServerHost: host, ServerPort: 3307}
			if got := !sharedServerDatabase(cfg); got != bd {
				t.Errorf("sharedServerDatabase(host %q) exempts the consent gate = %v, want %v", host, got, bd)
			}

			calls := 0
			ensureResolvedPortFile = func(string, int) error {
				calls++
				return nil
			}
			if err := persistResolvedPortFile(&Config{ServerHost: host, ServerPort: 3307}, t.TempDir()); err != nil {
				t.Fatalf("persistResolvedPortFile(host %q): %v", host, err)
			}
			if got := calls > 0; got != bd {
				t.Errorf("persistResolvedPortFile(host %q) wrote bd's port file = %v, want %v", host, got, bd)
			}
		})
	}

	// At 127.0.0.1 with no port variable set, the directory is the server's
	// only where a dolt process holds its lock and the server info file the
	// dolt CLI finds for it names the dialed port and a live process: a copy
	// that carries a running server's file but no held lock, a server on
	// another port, or one that has exited, does not serve it, and nothing
	// reads the store's port without a Config. A server started in the
	// database itself serves it.
	for _, tc := range []struct {
		name  string
		serve func(t *testing.T, store *DoltStore)
		port  int
		want  bool
	}{
		{"a copy of a served directory", func(t *testing.T, s *DoltStore) { serveDoltDir(t, s.dbPath, os.Getpid(), testServedPort) }, testServedPort, false},
		{"server on another port", func(t *testing.T, s *DoltStore) { serveDoltDatabase(t, s, s.dbPath, testServedPort+1) }, testServedPort, false},
		{"server gone", func(t *testing.T, s *DoltStore) { serveDoltDir(t, s.dbPath, deadPID, testServedPort) }, testServedPort, false},
		{"no dialed port", func(t *testing.T, s *DoltStore) { serveDoltDatabase(t, s, s.dbPath, testServedPort) }, 0, false},
		{"server started in the database", func(t *testing.T, s *DoltStore) { serveDoltDatabase(t, s, s.CLIDir(), testServedPort) }, testServedPort, true},
	} {
		t.Run("host=127.0.0.1/"+tc.name, func(t *testing.T) {
			clearPredicateEnv(t)
			clearSizingModeEnv(t)
			store := leftoverDatabaseStore(t, t.TempDir(), &Config{ServerHost: "127.0.0.1", ServerPort: tc.port})
			tc.serve(t, store)
			checkLeftoverDatabaseDecisions(t, store, tc.want, "127.0.0.1, "+tc.name)
		})
	}
	t.Run("host=127.0.0.1/no Config", func(t *testing.T) {
		clearPredicateEnv(t)
		clearSizingModeEnv(t)
		store := leftoverDatabaseStore(t, t.TempDir(), nil)
		serveDoltDatabase(t, store, store.dbPath, testServedPort)
		checkLeftoverDatabaseDecisions(t, store, false, "a store without a Config")
	})

	// bd's own server is a plain TCP server: a gateway, a TLS endpoint and a
	// unix socket are someone else's even at 127.0.0.1, so the workspace's
	// Dolt root is neither their local data directory nor a database the
	// dolt CLI moves as theirs, and its remotes are not theirs.
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"gateway", Config{Gateway: true}},
		{"TLS", Config{ServerTLS: true}},
		{"unix socket", Config{ServerSocket: "/tmp/dolt.sock"}},
	} {
		t.Run(tc.name+"/host=127.0.0.1", func(t *testing.T) {
			clearPredicateEnv(t)
			clearSizingModeEnv(t)
			beadsDir := t.TempDir()
			cfg := tc.cfg
			cfg.Path, cfg.BeadsDir, cfg.Database, cfg.ServerHost, cfg.ServerPort, cfg.AutoStart = filepath.Join(beadsDir, "dolt"), beadsDir, "active", "127.0.0.1", testServedPort, true
			if dataDir := resolveLocalActiveDatabaseDir(&cfg); dataDir != "" {
				t.Errorf("resolveLocalActiveDatabaseDir(%s) = %q, want none", tc.name, dataDir)
			}
			// Even served on that port, the transport is not bd's.
			store := leftoverDatabaseStore(t, beadsDir, &cfg)
			serveDoltDatabase(t, store, store.dbPath, testServedPort)
			checkLeftoverDatabaseDecisions(t, store, false, tc.name+" at 127.0.0.1")
		})
	}

	// A port the environment names may be any server, such as a container or
	// a tunnel on 127.0.0.1, so even at bd's own host the workspace's Dolt
	// root is then counted as neither the local data directory nor the
	// directory the dolt CLI moves as the server's data. The host=127.0.0.1
	// case above is the same store without the variable. The CLI routes and
	// the recorded remotes accept the proof sharedServerDatabase accepts
	// (GH#6169): bd's state files for the workspace name a live server on
	// that very port, as when a multi-agent rig names the server bd started.
	// Since a wrong answer runs the dolt CLI in that directory, the recorded
	// process must also be a dolt sql-server: a process that took the PID of
	// bd's server after it died is no proof. The local data directory, which
	// grants garbage collection, accepts neither.
	for _, portVar := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		for _, tc := range []struct {
			name           string
			bdServerOnPort int
			// notDolt records a live process that is not a dolt sql-server.
			notDolt bool
			// unserved leaves the directory without a server's info file
			// or lock: no server on the port serves it.
			unserved bool
			want     bool
		}{
			{name: "no server bd runs"},
			{name: "bd server on that port", bdServerOnPort: 3310, want: true},
			{name: "bd server on that port, directory not served", bdServerOnPort: 3310, unserved: true},
			{name: "bd server on another port", bdServerOnPort: 3311},
			{name: "a process that is not dolt on that port", bdServerOnPort: 3310, notDolt: true},
		} {
			t.Run(portVar+"/host=127.0.0.1/"+tc.name, func(t *testing.T) {
				clearPredicateEnv(t)
				clearSizingModeEnv(t)
				t.Setenv(portVar, "3310")

				beadsDir := unmanagedWorkspace(t)
				switch {
				case tc.notDolt:
					beadsDir = managedWorkspace(t, tc.bdServerOnPort)
				case tc.bdServerOnPort > 0:
					beadsDir = doltManagedWorkspace(t, tc.bdServerOnPort)
				}
				if dataDir := resolveLocalActiveDatabaseDir(&Config{
					Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir, Database: "active", ServerHost: "127.0.0.1", ServerPort: 3310, AutoStart: true,
				}); dataDir != "" {
					t.Errorf("resolveLocalActiveDatabaseDir with %s set = %q, want none: the variable may name any server", portVar, dataDir)
				}
				store := leftoverDatabaseStore(t, beadsDir, &Config{ServerHost: "127.0.0.1", ServerPort: 3310, BeadsDir: beadsDir})
				if !tc.unserved {
					serveDoltDatabase(t, store, store.dbPath, 3310)
				}
				checkLeftoverDatabaseDecisions(t, store, tc.want, portVar+" set, "+tc.name)
			})
		}
	}
}
