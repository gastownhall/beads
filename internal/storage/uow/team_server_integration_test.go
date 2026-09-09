package uow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type teamServerHarness struct {
	port         int
	storeRootDir string
	logPath      string
}

func newTeamServerHarness(t *testing.T) *teamServerHarness {
	t.Helper()
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

	return &teamServerHarness{
		port:         portInt,
		storeRootDir: storeRootDir,
		logPath:      filepath.Join(t.TempDir(), "server.log"),
	}
}

func (h *teamServerHarness) openProvider(ctx context.Context, database string, teamServer bool, expectedProjectID string, opts ...ProviderOption) (UnitOfWorkProvider, error) {
	return NewExternalDoltServerUOWProvider(
		ctx,
		h.storeRootDir,
		database,
		h.logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
		"root",
		"",
		0,
		0,
		teamServer,
		expectedProjectID,
		opts...,
	)
}

// directDB connects straight to the dolt sql-server, bypassing the proxy.
func (h *teamServerHarness) directDB(t *testing.T, database string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", h.port, database)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type migrationsSnapshot struct {
	count      int
	maxVersion int
}

func snapshotMigrations(t *testing.T, ctx context.Context, db *sql.DB) migrationsSnapshot {
	t.Helper()
	var s migrationsSnapshot
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&s.count, &s.maxVersion))
	return s
}

func TestTeamServerMode_Integration(t *testing.T) {
	h := newTeamServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const database = "beads_ts"

	t.Run("missing database refused with bts init", func(t *testing.T) {
		p, err := h.openProvider(ctx, "beads_ts_missing", true, "")
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "bts init")
	})

	t.Run("existing empty database refused with bts init", func(t *testing.T) {
		// The container pre-creates beads_test with no beads schema in it.
		p, err := h.openProvider(ctx, "beads_test", true, "")
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "bts init")
	})

	// Provision the database the way bts would (a normal, migrating open).
	provisioner, err := h.openProvider(ctx, database, false, "", WithCreateIfMissing(true))
	require.NoError(t, err)
	require.NoError(t, provisioner.Close(ctx))

	direct := h.directDB(t, database)
	before := snapshotMigrations(t, ctx, direct)
	require.Positive(t, before.maxVersion, "provisioning must have applied migrations")

	t.Run("matching version opens and leaves schema_migrations untouched", func(t *testing.T) {
		p, err := h.openProvider(ctx, database, true, "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.Close(ctx) })

		sqlProv, ok := p.(*doltSQLProvider)
		require.True(t, ok, "expected *doltSQLProvider, got %T", p)
		var n int
		require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&n))

		assert.Equal(t, before, snapshotMigrations(t, ctx, direct),
			"team-server open must not modify schema_migrations")
	})

	t.Run("project identity is verified on every open, not just at init", func(t *testing.T) {
		// bts provisions the shared database with a project identity; bd
		// adopts it at init and, before this guard existed, never looked at
		// it again on the proxied path.
		_, err := direct.ExecContext(ctx,
			"REPLACE INTO metadata (`key`, value) VALUES (?, ?)", "_project_id", "project-owning-this-db")
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := direct.ExecContext(ctx, "DELETE FROM metadata WHERE `key` = ?", "_project_id")
			require.NoError(t, err)
		})

		t.Run("matching workspace identity opens", func(t *testing.T) {
			p, err := h.openProvider(ctx, database, true, "project-owning-this-db")
			require.NoError(t, err)
			t.Cleanup(func() { _ = p.Close(ctx) })
		})

		// The negative control: a workspace belonging to another project must
		// be refused rather than silently served this database.
		t.Run("foreign workspace identity is refused", func(t *testing.T) {
			p, err := h.openProvider(ctx, database, true, "project-somewhere-else")
			require.Error(t, err)
			assert.Nil(t, p)
			assert.Contains(t, err.Error(), "PROJECT IDENTITY MISMATCH")
			assert.Contains(t, err.Error(), "project-somewhere-else")
			assert.Contains(t, err.Error(), "project-owning-this-db")
		})

		// The adoption path (bd init --team-server) asserts nothing.
		t.Run("adoption path still opens", func(t *testing.T) {
			p, err := h.openProvider(ctx, database, true, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = p.Close(ctx) })
		})
	})

	t.Run("behind database refused with bts migrate", func(t *testing.T) {
		// Roll the migration cursor back one version to simulate a behind DB.
		var savedHash sql.NullString
		require.NoError(t, direct.QueryRowContext(ctx,
			"SELECT content_hash FROM schema_migrations WHERE version = ?", before.maxVersion).Scan(&savedHash))
		_, err := direct.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", before.maxVersion)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := direct.ExecContext(ctx,
				"INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)", before.maxVersion, savedHash)
			require.NoError(t, err)
		})
		rolledBack := snapshotMigrations(t, ctx, direct)
		require.Less(t, rolledBack.maxVersion, before.maxVersion)

		p, err := h.openProvider(ctx, database, true, "")
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "bts migrate")
		assert.NotContains(t, err.Error(), "run any bd write command",
			"must not suggest bd-driven migration on a bts-owned schema")

		assert.Equal(t, rolledBack, snapshotMigrations(t, ctx, direct),
			"refused team-server open must not migrate the database")
	})
}
