package uow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
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
	verifiedShutdownCleanup(t, storeRootDir)
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
	verifiedShutdownCleanup(t, storeRootDir)
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
	verifiedShutdownCleanup(t, storeRootDir)
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
	verifiedShutdownCleanup(t, storeRootDir)
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

// TestExternalProviderRediscoversAfterProxyRetirement pins #7295 for the
// external-server constructor: its upstream is external, but the proxy in front
// of it is local and retires on idle. The next physical connection must reach a
// rediscovered proxy and, through it, the same upstream, database, credentials
// and ClientFoundRows semantics.
func TestExternalProviderRediscoversAfterProxyRetirement(t *testing.T) {
	upstreamPort, err := strconv.Atoi(testutil.StartIsolatedDoltContainer(t))
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })
	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const database = "external_rediscovery"
	provider, err := NewExternalDoltServerUOWProvider(ctx, storeRootDir, database,
		filepath.Join(t.TempDir(), "server.log"),
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: upstreamPort},
		"root", "", 0, retirementIdleTimeout, false, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	p, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "NewExternalDoltServerUOWProvider returned %T, want *doltSQLProvider", provider)

	commitProbeRow(ctx, t, p, "rediscovery_probe", "persisted before idle")
	p.SetPoolLimits(PoolLimits{MaxIdleConns: -1})

	old := waitForProxyRetirement(t, storeRootDir)
	stale := listenOnRetiredPort(t, old.Port)

	request, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	tx, err := p.BeginTx(request)
	require.NoError(t, err, "unit of work after the local proxy retired on idle")
	defer func() { _ = tx.Rollback(request) }()
	conn := tx.(*doltServerTx).conn

	var gotDatabase, user, value string
	var serverPort int
	require.NoError(t, conn.QueryRowContext(request,
		"SELECT DATABASE(), CURRENT_USER(), @@port, value FROM rediscovery_probe WHERE id = 1",
	).Scan(&gotDatabase, &user, &serverPort, &value))
	assert.Equal(t, database, gotDatabase)
	assert.Equal(t, "persisted before idle", value)
	assert.Contains(t, user, "root@", "credentials")
	assert.Equal(t, upstreamPort, serverPort, "the rediscovered proxy must front the original external upstream")
	// A no-op UPDATE reports its matched row only under ClientFoundRows, which
	// the provider's writes rely on.
	res, err := conn.ExecContext(request, "UPDATE rediscovery_probe SET value = value WHERE id = 1")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected, "ClientFoundRows must survive rediscovery")
	assert.Zero(t, stale.accepts.Load(), "the provider dialed the retired proxy's port")

	fresh, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.NotEqual(t, old.Port, fresh.Port, "replacement proxy port")
}

// TestExternalDoltServerOpenRefusesForwardDrift is the end-to-end leg of
// gastownhall/beads#8121: an existing database whose schema cursor is AHEAD of
// this binary is refused by the real provider open, on the converged fast path
// and on the locked path, and BD_IGNORE_SCHEMA_SKEW=1 lets both through with
// one warning. The unit seam in forward_drift_open_test.go cannot prove the
// real gate composition or a successful locked-path open; this does.
func TestExternalDoltServerOpenRefusesForwardDrift(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	const database = "beads_ahead"
	open := func() (UnitOfWorkProvider, error) {
		return NewExternalDoltServerUOWProvider(
			ctx,
			storeRootDir,
			database,
			logPath,
			configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
			"root",
			"",
			0,
			0,
			false,
			"",
		)
	}
	openAndClose := func(t *testing.T) error {
		t.Helper()
		provider, err := open()
		if provider != nil {
			require.NoError(t, provider.Close(context.Background()))
		}
		return err
	}

	// A normal first open creates and migrates the database to latest.
	require.NoError(t, openAndClose(t))

	admin, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", portInt, database))
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })

	// A newer bd has migrated the database one version past this binary.
	ahead := schema.LatestVersion() + 1
	_, err = admin.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", ahead)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'test: schema cursor ahead of this binary')")
	require.NoError(t, err)

	headHash := func(t *testing.T) string {
		t.Helper()
		var h string
		require.NoError(t, admin.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&h))
		return h
	}
	dirtyTables := func(t *testing.T) []string {
		t.Helper()
		rows, err := admin.QueryContext(ctx, "SELECT table_name FROM dolt_status")
		require.NoError(t, err)
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
		}
		require.NoError(t, rows.Err())
		return names
	}
	tempPatternSeeded := func(t *testing.T) bool {
		t.Helper()
		var n int
		require.NoError(t, admin.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_ignore WHERE pattern = '__temp__%'").Scan(&n))
		return n > 0
	}
	requireSkew := func(t *testing.T, err error) {
		t.Helper()
		var skew *schema.SchemaSkewError
		require.ErrorAs(t, err, &skew)
		require.Equal(t, ahead, skew.DBVersion)
		require.Equal(t, schema.LatestVersion(), skew.BinaryVersion)
	}
	openWithOverride := func(t *testing.T) {
		t.Helper()
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		// t is the parent test: put the variable back for the steps after.
		defer t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
		var openErr error
		stderr := captureStderr(t, func() { openErr = openAndClose(t) })
		require.NoError(t, openErr, "BD_IGNORE_SCHEMA_SKEW=1 must let the open through")
		require.Equal(t, 1, strings.Count(stderr, skewIgnoredWarning),
			"want exactly one skew-ignored warning, stderr:\n%s", stderr)
	}

	// Converged shape: seeded, clean, cursors at or past latest. The open is
	// refused and writes nothing.
	head := headHash(t)
	requireSkew(t, openAndClose(t))
	require.Equal(t, head, headHash(t), "the refused open committed to the ahead database")
	require.Empty(t, dirtyTables(t), "the refused open wrote to the ahead database")
	openWithOverride(t)

	// Locked shape: an under-seeded dolt_ignore makes the convergence probe
	// decline, so the open takes the migration lock and runs the gate. The
	// refusal must land before MigrateUp re-seeds the pattern.
	_, err = admin.ExecContext(ctx, "DELETE FROM dolt_ignore WHERE pattern = '__temp__%'")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'test: drop the __temp__ ignore pattern')")
	require.NoError(t, err)
	require.False(t, tempPatternSeeded(t))
	head = headHash(t)
	requireSkew(t, openAndClose(t))
	require.False(t, tempPatternSeeded(t), "MigrateUp seeded dolt_ignore on the ahead database before the refusal")
	require.Equal(t, head, headHash(t), "the refused open committed to the ahead database")
	require.Empty(t, dirtyTables(t), "the refused open wrote to the ahead database")

	// With the override, the locked path proceeds and MigrateUp heals the
	// pattern — still one warning.
	openWithOverride(t)
	require.True(t, tempPatternSeeded(t), "the overridden locked-path open did not run MigrateUp")
}
