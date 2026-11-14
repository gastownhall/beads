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
	verifiedShutdownCleanup(t, storeRootDir)

	return &teamServerHarness{
		port:         portInt,
		storeRootDir: storeRootDir,
		logPath:      filepath.Join(t.TempDir(), "server.log"),
	}
}

func (h *teamServerHarness) openProvider(ctx context.Context, database string, teamServer bool, expectedProjectID string) (UnitOfWorkProvider, error) {
	return h.openProviderAs(ctx, database, "root", "", teamServer, expectedProjectID)
}

func (h *teamServerHarness) openProviderAs(ctx context.Context, database, user, password string, teamServer bool, expectedProjectID string) (UnitOfWorkProvider, error) {
	return NewExternalDoltServerUOWProvider(
		ctx,
		h.storeRootDir,
		database,
		h.logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: h.port},
		user,
		password,
		0,
		0,
		teamServer,
		expectedProjectID,
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

	// A privileged credential CAN see that the database is absent, so the
	// refusal says exactly that and asks for provisioning. It must not repeat
	// the old blanket "not found" for failures it did not classify.
	t.Run("missing database refused as absent, with provisioning guidance", func(t *testing.T) {
		p, err := h.openProvider(ctx, "beads_ts_missing", true, "")
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), `database "beads_ts_missing" does not exist on the server`)
		assert.Contains(t, err.Error(), "ask the server administrator to create it")
	})

	t.Run("existing empty database refused with bts init", func(t *testing.T) {
		// The container pre-creates beads_test with no beads schema in it.
		p, err := h.openProvider(ctx, "beads_test", true, "")
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "bts init")
	})

	// Provision the database the way bts would (a normal, migrating open).
	provisioner, err := h.openProvider(ctx, database, false, "")
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

	// A credential granted only its own database is what a gateway front door
	// hands out, and the server deliberately answers "access denied" for both
	// absent and merely ungranted names — so bd cannot tell them apart and must
	// stop claiming it can.
	t.Run("scoped credential", func(t *testing.T) {
		admin := h.directDB(t, "")
		_, err := admin.ExecContext(ctx, "CREATE USER IF NOT EXISTS 'scoped'@'%' IDENTIFIED BY 'pw'")
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := admin.ExecContext(ctx, "DROP USER IF EXISTS 'scoped'@'%'")
			assert.NoError(t, err)
		})
		_, err = admin.ExecContext(ctx, "GRANT ALL ON `"+database+"`.* TO 'scoped'@'%'")
		require.NoError(t, err)

		t.Run("granted database still opens", func(t *testing.T) {
			p, err := h.openProviderAs(ctx, database, "scoped", "pw", true, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = p.Close(ctx) })
		})

		// beads_test exists on the server; the scoped credential has no grant
		// for it. Both this and the absent name below must produce the SAME
		// honest message, because the server gives bd the same error.
		for _, tc := range []struct {
			name     string
			database string
		}{
			{name: "ungranted existing database", database: "beads_test"},
			{name: "absent database", database: "beads_ts_absent"},
		} {
			t.Run(tc.name+" is reported as denied, not as absent", func(t *testing.T) {
				p, err := h.openProviderAs(ctx, tc.database, "scoped", "pw", true, "")
				require.Error(t, err)
				assert.Nil(t, p)
				assert.Contains(t, err.Error(), "access to database "+strconv.Quote(tc.database)+" was denied")
				assert.Contains(t, err.Error(), "ask the server administrator to provision the database and grant access")
				assert.NotContains(t, err.Error(), "does not exist on the server",
					"a denial hides existence — bd must not claim the database is absent")
				assert.NotContains(t, err.Error(), "bts init",
					"the remedy for a denial is a grant, not provisioning by a named product")
			})
		}
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
		assert.NotContains(t, err.Error(), "bd migrate schema",
			"must not suggest bd-driven migration on a bts-owned schema")

		assert.Equal(t, rolledBack, snapshotMigrations(t, ctx, direct),
			"refused team-server open must not migrate the database")
	})
}

// TestPlainProxiedServerMode_ForeignProjectIdentityRefused_Integration is the
// bd-rgvivq (gastownhall/beads#5572) repro: a plain (non-team-server)
// proxied open that carries the workspace's project identity must not attach
// to a beads database that belongs to a DIFFERENT project. The CLI wiring
// (resolveProxiedServerUOWTopology) passes persisted.ProjectID as
// expectedProjectID for plain proxied workspaces too; only the team-server
// branch of initSchemaAttempt ever asserted it.
func TestPlainProxiedServerMode_ForeignProjectIdentityRefused_Integration(t *testing.T) {
	h := newTeamServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const database = "beads_other_project"

	// Another project's beads database on the same server.
	provisioner, err := h.openProvider(ctx, database, false, "")
	require.NoError(t, err)
	require.NoError(t, provisioner.Close(ctx))

	direct := h.directDB(t, database)
	_, err = direct.ExecContext(ctx,
		"REPLACE INTO metadata (`key`, value) VALUES (?, ?)", "_project_id", "project-owning-this-db")
	require.NoError(t, err)

	// A plain proxied workspace of another project names it via --database.
	p, err := h.openProvider(ctx, database, false, "project-somewhere-else")
	if p != nil {
		_ = p.Close(ctx)
	}
	require.Error(t, err, "plain proxied open attached to a foreign project's beads database with no identity assertion")
	assert.Contains(t, err.Error(), "PROJECT IDENTITY MISMATCH")
}

// TestPlainProxiedExcursionIdentity_NonBeadsTarget_Integration pins the
// compatibility promise of the bd-rgvivq guard: a --database excursion into
// a database that is not a beads database stays fully usable — across
// repeated excursions, including after an earlier excursion open installed
// the beads schema in it without a project identity — and no open ever mints
// an identity there.
func TestPlainProxiedExcursionIdentity_NonBeadsTarget_Integration(t *testing.T) {
	h := newTeamServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	admin := h.directDB(t, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE excursion_widgets")
	require.NoError(t, err)
	t.Cleanup(func() {
		// The parent's ctx is canceled by its deferred cancel() before
		// cleanup functions run; use a fresh one.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS excursion_widgets")
		assert.NoError(t, err)
	})
	_, err = admin.ExecContext(ctx, "CREATE TABLE excursion_widgets.widgets (id INT PRIMARY KEY, name VARCHAR(32))")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "INSERT INTO excursion_widgets.widgets VALUES (1, 'gear')")
	require.NoError(t, err)
	// Commit the seed data dolt-side, the way any real pre-existing database
	// holds it (and the CLI lane's proxy does for its sessions): the
	// migrating open refuses uncommitted working-set changes to tables it
	// does not own, so the compatibility envelope under test is the
	// committed shape.
	seeded := h.directDB(t, "excursion_widgets")
	_, err = seeded.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed widgets')")
	require.NoError(t, err)

	// First excursion from a workspace that carries a project identity: the
	// target has no beads schema at all, so the check finds no metadata
	// table and the migrating open installs the schema.
	p, err := h.openProvider(ctx, "excursion_widgets", false, "project-workspace")
	require.NoError(t, err, "a --database excursion into a non-beads database must stay permitted")
	require.NoError(t, p.Close(ctx))

	// The user's own data survived the migrating open.
	var n int
	require.NoError(t, admin.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM excursion_widgets.widgets").Scan(&n))
	assert.Equal(t, 1, n, "excursion open must not disturb the target's own tables")

	// The open must not have minted an identity for the workspace.
	var minted int
	require.NoError(t, admin.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM excursion_widgets.metadata WHERE `key` = '_project_id'").Scan(&minted))
	assert.Zero(t, minted, "a plain excursion must not mint a project identity in the target database")

	// Second excursion: the beads schema now exists but carries no identity
	// row, so the open passes again — and still mints nothing.
	p, err = h.openProvider(ctx, "excursion_widgets", false, "project-workspace")
	require.NoError(t, err, "repeated excursion into the now-schema'd database must stay permitted")
	require.NoError(t, p.Close(ctx))
	require.NoError(t, admin.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM excursion_widgets.metadata WHERE `key` = '_project_id'").Scan(&minted))
	assert.Zero(t, minted, "repeated excursion must not adopt or mint an identity either")
}
