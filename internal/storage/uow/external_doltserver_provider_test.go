package uow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExternalDoltServerUOWProvider_ValidationErrors(t *testing.T) {
	validExt := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3306}

	cases := []struct {
		name     string
		database string
		rootUser string
		external configfile.ExternalDoltConfig
		want     string
	}{
		{"empty database", "", "root", validExt, "database name must not be empty"},
		{"empty rootUser", "beads", "", validExt, "rootUser must not be empty"},
		{"empty external config", "beads", "root", configfile.ExternalDoltConfig{}, "external"},
		{"external host without port", "beads", "root", configfile.ExternalDoltConfig{Host: "db"}, "Host requires Port"},
		{"external tls cert without key", "beads", "root", configfile.ExternalDoltConfig{
			Host: "db", Port: 3306, TLSCert: "/etc/beads/client.pem",
		}, "TLSCert set without TLSKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewExternalDoltServerUOWProvider(
				context.Background(),
				t.TempDir(),
				tc.database,
				"",
				tc.external,
				tc.rootUser,
				"",
				0,
				0,
				false,
				"",
			)
			assert.Nil(t, p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewExternalDoltServerUOWProvider_EndToEnd(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_test",
		logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root",
		"",
		0,
		0,
		false,
		"",
		WithCreateIfMissing(true),
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	sqlProv, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "expected *doltSQLProvider, got %T", provider)

	var one int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)

	var dbName string
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName))
	assert.Equal(t, "beads_test", dbName)

	_, err = sqlProv.db.ExecContext(ctx, "CREATE TABLE t_external_e2e (id INT PRIMARY KEY, v VARCHAR(64))")
	require.NoError(t, err)
	_, err = sqlProv.db.ExecContext(ctx, "INSERT INTO t_external_e2e (id, v) VALUES (1, 'alpha'), (2, 'beta')")
	require.NoError(t, err)

	var count int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_external_e2e").Scan(&count))
	assert.Equal(t, 2, count)

	var v string
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT v FROM t_external_e2e WHERE id = 2").Scan(&v))
	assert.Equal(t, "beta", v)
}

func TestNewExternalDoltServerUOWProvider_ConcurrentInstantiation(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")
	external := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt}

	const concurrency = 10
	type result struct {
		provider UnitOfWorkProvider
		err      error
	}
	results := make([]result, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			p, err := NewExternalDoltServerUOWProvider(
				context.Background(),
				storeRootDir,
				"beads_test",
				logPath,
				external,
				"root",
				"",
				0,
				0,
				false,
				"",
				WithCreateIfMissing(true),
			)
			results[i] = result{provider: p, err: err}
		}()
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, r := range results {
			if r.provider != nil {
				_ = r.provider.Close(context.Background())
			}
		}
	})

	for i, r := range results {
		assert.NoErrorf(t, r.err, "provider %d", i)
		assert.NotNilf(t, r.provider, "provider %d", i)
	}
}

// TestNewExternalDoltServerUOWProvider_FreshInitSelfHealsAfterMidPassFailure is
// the regression test for gastownhall/beads#5012: the proxied shard-1 CI flake
// where a fresh `bd init --proxied-server` fails with
//
//	uow: migrate: pending schema migrations alter pre-existing dirty tables: issues
//
// The shape of the failure: initSchema's first attempt dies mid-migration-pass
// on the shared server (transient serialization failure / poisoned session —
// the "[mysql] busy buffer" breadcrumb), leaving the failed step's SQL applied
// to the Dolt working set but never Dolt-committed. The backoff retry then
// re-runs MigrateUp, whose #4566 dirty-table guard reads that debris, decides
// the pass's own half-applied step is "pre-existing dirty user data", and
// converts a retryable transient into a permanent init failure — on a database
// that did not exist seconds earlier.
//
// The test reproduces that exact interleaving deterministically: the per-step
// fault hook fires once after migration 0001 commits, dirties `issues` in the
// working set the way a mid-step death does (SQL applied, no Dolt commit), and
// fails the pass with a retryable serialization error. The retry must
// self-heal (discard the bootstrap debris and finish the pass) instead of
// reporting DirtyTablesError.
func TestNewExternalDoltServerUOWProvider_FreshInitSelfHealsAfterMidPassFailure(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")

	// Fire exactly once, right after migration step 1 (create issues) has
	// committed: dirty `issues` in the working set (schema change applied, no
	// Dolt commit — indistinguishable from a step whose session died between
	// its SQL and its per-step commit), then kill the pass with an error the
	// provider's retry loop classifies as retryable.
	fired := false
	restore := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 1 || fired {
			return nil
		}
		fired = true
		if _, err := db.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN bd5012_debris INT"); err != nil {
			return fmt.Errorf("injecting mid-step debris: %w", err)
		}
		return &mysql.MySQLError{Number: 1213, Message: "test-injected deadlock (mid-pass session death)"}
	})
	t.Cleanup(restore)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_fresh_heal",
		logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root",
		"",
		0,
		0,
		false,
		"",
		WithCreateIfMissing(true),
	)
	require.NoError(t, err, "fresh init must converge after a mid-pass transient failure, not trip the #4566 guard on its own bootstrap debris")
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	require.True(t, fired, "fault hook never fired; test no longer exercises the mid-pass failure path")

	sqlProv, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "expected *doltSQLProvider, got %T", provider)

	// The injected debris must have been discarded, not committed into history.
	rows, err := sqlProv.db.QueryContext(ctx, "SHOW COLUMNS FROM issues LIKE 'bd5012_debris'")
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next(), "bootstrap debris column survived the self-heal")
	require.NoError(t, rows.Err())

	// And the migration pass must have completed to the latest version.
	var version int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version))
	assert.Equal(t, schema.LatestVersion(), version)

	// The working set must not still carry `issues` dirt.
	statusRows, err := sqlProv.db.QueryContext(ctx,
		"SELECT table_name FROM dolt_status WHERE table_name = 'issues'")
	require.NoError(t, err)
	defer statusRows.Close()
	assert.False(t, statusRows.Next(), "issues still dirty in dolt_status after init")
	require.NoError(t, statusRows.Err())
}

// TestNewExternalDoltServerUOWProvider_PreexistingDirtyDatabaseIsNotHealed is
// the ownership-negative counterpart of the fresh-init self-heal: an
// initializer that did NOT create the database (its bare CREATE DATABASE loses
// to an existing one) must keep the #4566 guard's refusal and must not
// DOLT_RESET the working set — the dirt it sees could be another actor's
// legitimate uncommitted state, which only the proven creator may discard.
func TestNewExternalDoltServerUOWProvider_PreexistingDirtyDatabaseIsNotHealed(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Another actor created the database and left `issues` dirty in the
	// working set (created, never Dolt-committed) — the state a mid-pass
	// death leaves behind, indistinguishable from uncommitted user work.
	admin, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?parseTime=true", portInt))
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	_, err = admin.ExecContext(ctx, "CREATE DATABASE beads_loser")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "CREATE TABLE beads_loser.issues (id INT PRIMARY KEY)")
	require.NoError(t, err)

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_loser",
		logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root",
		"",
		0,
		0,
		false,
		"",
	)
	if provider != nil {
		t.Cleanup(func() { _ = provider.Close(context.Background()) })
	}
	require.Error(t, err, "non-creator init over a dirty database must keep the #4566 refusal")
	require.Contains(t, err.Error(), "pending schema migrations alter pre-existing dirty tables: issues")

	// The dirt must be untouched: no DOLT_RESET may have fired.
	var count int
	require.NoError(t, admin.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM beads_loser.dolt_status WHERE table_name = 'issues'").Scan(&count))
	require.Equal(t, 1, count, "issues working-set dirt was discarded by a non-creator init")
}

// TestNewExternalDoltServerUOWProvider_ExistingDatabaseOpensWithoutCreatePrivilege
// is the gastownhall/beads#5079 regression: opening an already-initialized
// database must not attempt CREATE DATABASE, so a least-privilege account (full
// DML on its own database, no server CREATE privilege) can run reads like
// `bd list`. Before the probe-first change the open issued a bare CREATE
// DATABASE on its first attempt and Dolt denied it with Error 1105 ("command
// denied"), which the retry loop classified as permanent; every command
// failed, not just writes.
func TestNewExternalDoltServerUOWProvider_ExistingDatabaseOpensWithoutCreatePrivilege(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")
	external := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Root creates and fully initializes the database, then disconnects.
	initProvider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_restricted",
		logPath,
		external,
		"root",
		"",
		0,
		0,
		false,
		"",
		WithCreateIfMissing(true),
	)
	require.NoError(t, err)
	require.NoError(t, initProvider.Close(ctx))

	// Provision a least-privilege account: full DML on its own database but no
	// CREATE privilege anywhere, so CREATE DATABASE is denied (Error 1105)
	// while reads and writes within the database are allowed.
	admin, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?parseTime=true", portInt))
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	_, err = admin.ExecContext(ctx, "CREATE USER 'beads_app'@'%' IDENTIFIED BY 'app-pass'")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "GRANT SELECT, INSERT, UPDATE, DELETE ON `beads_restricted`.* TO 'beads_app'@'%'")
	require.NoError(t, err)

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_restricted",
		logPath,
		external,
		"beads_app",
		"app-pass",
		0,
		0,
		false,
		"",
	)
	require.NoError(t, err, "opening an existing database as a least-privilege account must not attempt CREATE DATABASE")
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	sqlProv, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "expected *doltSQLProvider, got %T", provider)

	var got string
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&got))
	assert.Equal(t, "beads_restricted", got)

	var one int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)
}

// TestNewExternalDoltServerUOWProvider_MissingDatabaseWithCreateDisabled
// verifies that with create-if-missing disabled (the default; the explicit
// option below documents intent), opening a database that does not exist
// fails with a clear, permanent not-found error that points at both bd init
// (new database) and bd bootstrap (existing project), and creates nothing;
// only a caller that explicitly initializes a workspace may create one
// (#2189).
func TestNewExternalDoltServerUOWProvider_MissingDatabaseWithCreateDisabled(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")
	external := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_absent",
		logPath,
		external,
		"root",
		"",
		0,
		0,
		false,
		"",
		WithCreateIfMissing(false),
	)
	if provider != nil {
		t.Cleanup(func() { _ = provider.Close(context.Background()) })
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), `database "beads_absent" not found on Dolt server`)
	assert.Contains(t, err.Error(), "run 'bd init' to create a new database")
	assert.Contains(t, err.Error(), "or 'bd bootstrap' to restore an existing project")

	// Nothing may have been created by the failed open.
	admin, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?parseTime=true", portInt))
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	var name string
	err = admin.QueryRowContext(ctx, "SHOW DATABASES LIKE 'beads_absent'").Scan(&name)
	assert.ErrorIs(t, err, sql.ErrNoRows, "a missing-database open with create disabled must not create the database")
}
