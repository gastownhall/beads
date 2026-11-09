package uow

import (
	"context"
	"database/sql"
	"errors"
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

// leastPrivilegeAccounts provisions the account model of
// gastownhall/beads#5079 on the harness server: a SELECT-only reader, and a
// DML account with full rights on the one database it uses but no global
// CREATE — the piece no database-scoped grant can substitute for when an open
// path issues CREATE DATABASE unconditionally.
func leastPrivilegeAccounts(t *testing.T, ctx context.Context, admin *sql.DB, database string) {
	t.Helper()
	for _, stmt := range []string{
		"CREATE USER IF NOT EXISTS 'lp_reader'@'%' IDENTIFIED BY 'readerpw'",
		"GRANT SELECT ON `" + database + "`.* TO 'lp_reader'@'%'",
		"CREATE USER IF NOT EXISTS 'lp_dml'@'%' IDENTIFIED BY 'dmlpw'",
		"GRANT ALL PRIVILEGES ON `" + database + "`.* TO 'lp_dml'@'%'",
	} {
		_, err := admin.ExecContext(ctx, stmt)
		require.NoErrorf(t, err, "provisioning: %s", stmt)
	}
	t.Cleanup(func() {
		_, err := admin.ExecContext(context.Background(), "DROP USER IF EXISTS 'lp_reader'@'%'")
		assert.NoError(t, err)
		_, err = admin.ExecContext(context.Background(), "DROP USER IF EXISTS 'lp_dml'@'%'")
		assert.NoError(t, err)
	})
}

// lockWaiterVisible reports whether any session other than holderID is
// currently executing a GET_LOCK — the observable signature of the reader's
// open queuing on the migration lock the admin holds. Adapted from
// internal/storage/dolt's migrationLockWaiterVisible (initschema_concurrent),
// which pins the same observation for the direct stack.
func lockWaiterVisible(ctx context.Context, observer *sql.DB, holderID int64) (bool, error) {
	rows, err := observer.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	idIndex, infoIndex := -1, -1
	for i, column := range columns {
		switch strings.ToLower(column) {
		case "id":
			idIndex = i
		case "info":
			infoIndex = i
		}
	}
	if idIndex < 0 || infoIndex < 0 {
		return false, fmt.Errorf("SHOW PROCESSLIST columns missing id/info: %v", columns)
	}

	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	for rows.Next() {
		for i := range values {
			values[i] = sql.NullString{}
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return false, err
		}
		if values[idIndex].Valid {
			if id, err := strconv.ParseInt(values[idIndex].String, 10, 64); err == nil && id == holderID {
				continue
			}
		}
		if values[infoIndex].Valid && strings.Contains(strings.ToUpper(values[infoIndex].String), "GET_LOCK") {
			return true, nil
		}
	}
	return false, rows.Err()
}

// TestNewExternalDoltServerUOWProvider_LeastPrivilegeAccounts is the
// integration regression for gastownhall/beads#5079 (AC-5): on a converged
// database, a SELECT-only account must be able to open the provider and read
// issues with no CREATE privilege at all — through the LOCKED path, not just
// the converged fast path, because a peer holding the migration lock forces
// every open through locked preparation. Before the probe, that preparation
// issued a bare CREATE DATABASE, so the reader's open was refused before the
// read it opened for.
func TestNewExternalDoltServerUOWProvider_LeastPrivilegeAccounts(t *testing.T) {
	h := newTeamServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const database = "beads_least_priv"

	// Provision as the administrator: an ordinary migrating open creates and
	// converges the database, which is the quiet baseline every read expects.
	provisioner, err := h.openProvider(ctx, database, false, "")
	require.NoError(t, err)
	require.NoError(t, provisioner.Close(ctx))

	admin := h.directDB(t, "")
	leastPrivilegeAccounts(t, ctx, admin, database)

	openAs := func(user, password string, opts ...ProviderOption) (UnitOfWorkProvider, error) {
		return NewExternalDoltServerUOWProvider(
			ctx, h.storeRootDir, database, h.logPath,
			configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
			user, password, 0, 0, false, "", opts...)
	}

	t.Run("read-only open forced through the locked path succeeds without CREATE", func(t *testing.T) {
		lockName := schema.MigrationLockName(database)

		// A peer holds the database's migration lock on a pinned admin
		// connection, which forces the reader's open through the locked path
		// instead of the converged fast path.
		holder, err := admin.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = holder.Close() }) // closing the session releases the lock
		var holderID int64
		require.NoError(t, holder.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&holderID))
		var locked sql.NullInt64
		require.NoError(t, holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&locked))
		require.True(t, locked.Valid && locked.Int64 == 1, "admin could not hold the migration lock")

		type openResult struct {
			provider UnitOfWorkProvider
			err      error
		}
		opened := make(chan openResult, 1)
		go func() {
			p, err := openAs("lp_reader", "readerpw", WithReadOnly())
			opened <- openResult{provider: p, err: err}
		}()

		// Wait until the reader is observably queuing on the lock — the proof
		// the open really is on the locked path — then release.
		waitDeadline := time.Now().Add(15 * time.Second)
		visible := false
		for time.Now().Before(waitDeadline) {
			select {
			case res := <-opened:
				t.Fatalf("reader open returned before queuing on the lock: err = %v", res.err)
			default:
			}
			visible, err = lockWaiterVisible(ctx, admin, holderID)
			require.NoError(t, err)
			if visible {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for the reader's GET_LOCK: %v", ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		require.True(t, visible, "the reader never queued on the migration lock; the locked path was not forced")

		var released sql.NullInt64
		require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released))
		require.True(t, released.Valid && released.Int64 == 1)

		var res openResult
		select {
		case res = <-opened:
		case <-ctx.Done():
			t.Fatalf("reader open did not complete after the lock was released: %v", ctx.Err())
		}
		require.NoError(t, res.err, "a SELECT-only read of a converged database must not require CREATE (gastownhall/beads#5079)")
		require.NotNil(t, res.provider)
		t.Cleanup(func() { _ = res.provider.Close(context.Background()) })

		sqlProv, ok := res.provider.(*doltSQLProvider)
		require.True(t, ok, "expected *doltSQLProvider, got %T", res.provider)
		var issues int
		require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&issues))
	})

	t.Run("unprovisioned target is refused naming both possibilities, and nothing is created", func(t *testing.T) {
		const absent = "beads_least_priv_absent"
		p, err := NewExternalDoltServerUOWProvider(
			ctx, h.storeRootDir, absent, h.logPath,
			configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
			"lp_reader", "readerpw", 0, 0, false, "")
		require.Error(t, err)
		assert.Nil(t, p)
		// The probe said the database was not visible, so the CREATE denial
		// cannot tell a missing database from an ungranted one: both must be
		// named, with the administrator remedy (AC-4).
		assert.Contains(t, err.Error(), `database "beads_least_priv_absent" is not visible to this credential and creating it was denied`)
		assert.Contains(t, err.Error(), "either the database is not provisioned on the server or this credential has not been granted access to it")
		assert.Contains(t, err.Error(), "ask the server administrator to provision the database and grant access")
		// And no fallback created anything.
		var count int
		require.NoError(t, admin.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", absent).Scan(&count))
		assert.Zero(t, count, "the refused open must not leave a database behind")
	})
}

// TestNewExternalDoltServerUOWProvider_BehindDatabaseReadThroughAndWriteRefusal
// is the integration regression for the AC-6 half of gastownhall/beads#5079's
// fix: a readable database one migration behind, with no remote and no
// migration consent, must keep its existing gate behavior for restricted
// accounts — the SELECT-only reader is warned and served at the current
// schema, the database-DML writer is refused with the shared-no-remote gate
// error — and neither open may change migration state.
func TestNewExternalDoltServerUOWProvider_BehindDatabaseReadThroughAndWriteRefusal(t *testing.T) {
	h := newTeamServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// No consent, and restore the package's global consent state afterwards.
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Cleanup(func() {
		schema.SetSharedMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
	})

	const database = "beads_behind_rw"

	// Provision as the administrator, then roll the cursor back one version:
	// the latest schema (every column present, so reads work), one migration
	// behind, no remote — the readable pending-migration shape.
	provisioner, err := h.openProvider(ctx, database, false, "")
	require.NoError(t, err)
	require.NoError(t, provisioner.Close(ctx))

	direct := h.directDB(t, database)
	var maxVersion int
	var savedHash sql.NullString
	require.NoError(t, direct.QueryRowContext(ctx,
		"SELECT MAX(version) FROM schema_migrations").Scan(&maxVersion))
	require.NoError(t, direct.QueryRowContext(ctx,
		"SELECT content_hash FROM schema_migrations WHERE version = ?", maxVersion).Scan(&savedHash))
	_, err = direct.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", maxVersion)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := direct.ExecContext(context.Background(),
			"INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", maxVersion, savedHash)
		assert.NoError(t, err)
	})

	admin := h.directDB(t, "")
	leastPrivilegeAccounts(t, ctx, admin, database)
	before := snapshotMigrations(t, ctx, direct)
	require.Less(t, before.maxVersion, maxVersion, "fixture must be one migration behind")

	// The SELECT-only reader opens read-only and is served at the current
	// schema, with the existing warning.
	var readerErr error
	var readerProvider UnitOfWorkProvider
	warning := captureStderr(t, func() {
		readerProvider, readerErr = NewExternalDoltServerUOWProvider(
			ctx, h.storeRootDir, database, h.logPath,
			configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
			"lp_reader", "readerpw", 0, 0, false, "", WithReadOnly())
	})
	require.NoError(t, readerErr, "a SELECT-only read-only open must read through the refused migration")
	t.Cleanup(func() { _ = readerProvider.Close(context.Background()) })
	for _, want := range []string{"Read-only command", "without migrating"} {
		assert.Contains(t, warning, want)
	}
	sqlProv, ok := readerProvider.(*doltSQLProvider)
	require.True(t, ok, "expected *doltSQLProvider, got %T", readerProvider)
	var issues int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&issues))
	assert.Equal(t, before, snapshotMigrations(t, ctx, direct),
		"the read-through must not change migration state")

	// The database-DML writer is refused with the existing shared-no-remote
	// gate error — the denial the bare CREATE used to mask.
	_, writeErr := NewExternalDoltServerUOWProvider(
		ctx, h.storeRootDir, database, h.logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
		"lp_dml", "dmlpw", 0, 0, false, "")
	require.Error(t, writeErr, "a write open behind a migration must keep the shared-no-remote refusal")
	var gateErr *schema.RemoteMigrateGateError
	require.Truef(t, errors.As(writeErr, &gateErr),
		"write open error = %T (%v), want *schema.RemoteMigrateGateError", writeErr, writeErr)
	assert.Equal(t, "shared-no-remote", gateErr.Decision)
	assert.Equal(t, before, snapshotMigrations(t, ctx, direct),
		"the refused write open must not change migration state")
}
