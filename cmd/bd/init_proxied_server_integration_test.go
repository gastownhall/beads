//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
)

// readProxiedMetadataRow reads one `key`/value row from the project's
// database (metadata or local_metadata), reporting false when the key is
// absent. These tests assert on shared-table state an older bd would have
// written, so they read the tables directly rather than through bd.
func readProxiedMetadataRow(t *testing.T, db *sql.DB, table, key string) (string, bool) {
	t.Helper()
	var value string
	err := db.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT value FROM %s WHERE `key` = ?", table), key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	require.NoError(t, err, "read %s.%s", table, key)
	return value, true
}

// TestProxiedServerInitSecondCloneAdoptsDatabaseProjectID pins #7576: a plain
// (non --team-server) proxied init that adopts an already-identified shared
// database must save the adopted project id to .beads/metadata.json. The file
// is composed with a locally-minted id before any database connection exists,
// and the tail git-commits it, so a clone that keeps its minted id commits an
// identity that disagrees with the database's _project_id.
func TestProxiedServerInitSecondCloneAdoptsDatabaseProjectID(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser)

	// Clone A bootstraps the shared database, recording its minted id as the
	// database's _project_id.
	a := bdProxiedInit(t, bd, "shid", args...)
	a.database = database
	db := openProxiedDB(t, a)
	adopted, ok := readProxiedMetadataRow(t, db, "metadata", "_project_id")
	require.True(t, ok, "bootstrap recorded no _project_id")
	require.NotEmpty(t, adopted)

	// Clone B is a fresh git repository with no metadata.json of its own; the
	// same args select the same database, so its init adopts A's identity.
	b := bdProxiedInit(t, bd, "shid", args...)
	bCfg, err := configfile.Load(b.beadsDir)
	require.NoError(t, err)
	require.NotNil(t, bCfg)
	require.Equal(t, adopted, bCfg.ProjectID,
		"second clone metadata.json project_id must be the adopted database identity")

	// The tail git-commits .beads/, so the committed copy must carry the
	// adopted id too, not just the working tree.
	show := gitCommand("show", "HEAD:.beads/metadata.json")
	show.Dir = b.dir
	out, err := show.Output()
	require.NoError(t, err, "git show HEAD:.beads/metadata.json in the second clone")
	var committedCfg configfile.Config
	require.NoError(t, json.Unmarshal(out, &committedCfg))
	require.Equal(t, adopted, committedCfg.ProjectID,
		"the tail-committed .beads/metadata.json must carry the adopted id")
}

// TestProxiedServerInitPrefixOnlyDatabaseKeepsLocalProjectID pins the AC-4
// half of #7576: a database carrying a prefix but no _project_id is a partial
// identity the adopt branch already treats as identified, so a clone initing
// against it must keep its own minted id — an empty adopted id is never saved,
// and the clone's id is not written back to the identified database either.
func TestProxiedServerInitPrefixOnlyDatabaseKeepsLocalProjectID(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser)

	a := bdProxiedInit(t, bd, "pfx", args...)
	a.database = database
	db := openProxiedDB(t, a)

	// Reduce the database to a prefix-only identity: identified, but with no
	// _project_id for a clone to adopt.
	_, err := db.ExecContext(context.Background(), "DELETE FROM metadata WHERE `key` = '_project_id'")
	require.NoError(t, err, "delete _project_id")

	c := bdProxiedInit(t, bd, "pfx", args...)
	cCfg, err := configfile.Load(c.beadsDir)
	require.NoError(t, err)
	require.NotNil(t, cCfg)
	require.NotEmpty(t, cCfg.ProjectID,
		"a prefix-only database must not blank the clone's own project_id")

	// The adopt branch never rewrites database identity, so the partial
	// identity must still be exactly that.
	still, ok := readProxiedMetadataRow(t, db, "metadata", "_project_id")
	require.False(t, ok,
		"the clone must not write its id back to the identified database; found _project_id %q", still)
}

// TestProxiedServerInitBootstrapWritesNoCloneFingerprints pins #7577: a
// fresh plain proxied init must record no repo_id/clone_id in the shared
// database's metadata table. Every clone proxying to the database shares
// that row, so a per-clone fingerprint there is last-init-wins and feeds
// false cross-project mismatch diagnostics; team-server mode and gateway
// init write neither for exactly this reason.
func TestProxiedServerInitBootstrapWritesNoCloneFingerprints(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	a := newSharedProxiedProject(t, bd, "shfp")
	db := openProxiedDB(t, a)

	for _, key := range []string{"repo_id", "clone_id"} {
		_, ok := readProxiedMetadataRow(t, db, "metadata", key)
		require.False(t, ok,
			"a fresh proxied bootstrap recorded the per-clone fingerprint %q in the shared metadata table", key)
	}
}

// TestProxiedServerInitKeepsExistingSharedFingerprints pins #7577's
// shared-database half: a database whose metadata table already carries
// repo_id/clone_id rows (an older bd wrote them) must keep them exactly as
// they are when another clone runs plain proxied init against it.
func TestProxiedServerInitKeepsExistingSharedFingerprints(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser)

	a := bdProxiedInit(t, bd, "shfp", args...)
	a.database = database
	db := openProxiedDB(t, a)

	// Seed the rows an older bd would have left on this shared database.
	_, err := db.ExecContext(context.Background(),
		"REPLACE INTO metadata (`key`, value) VALUES ('repo_id','seed-repo'),('clone_id','seed-clone')")
	require.NoError(t, err, "seed fingerprint rows")

	// Clone B inits against the same database with its own fingerprints.
	b := bdProxiedInit(t, bd, "shfp", args...)
	bCfg, err := configfile.Load(b.beadsDir)
	require.NoError(t, err)
	require.NotNil(t, bCfg)

	repo, ok := readProxiedMetadataRow(t, db, "metadata", "repo_id")
	require.True(t, ok, "the seeded repo_id row vanished")
	require.Equal(t, "seed-repo", repo,
		"a second clone's init overwrote the shared repo_id")
	clone, ok := readProxiedMetadataRow(t, db, "metadata", "clone_id")
	require.True(t, ok, "the seeded clone_id row vanished")
	require.Equal(t, "seed-clone", clone,
		"a second clone's init overwrote the shared clone_id")
}

// TestProxiedServerInitStillRecordsVersionAndImportTime is #7577's guard
// against removing too much: dropping the per-clone fingerprints from
// recordProxiedInitTrackingState must not take the database-level
// bookkeeping with it. bd_version (local_metadata) feeds version
// reconciliation, and last_import_time (metadata) still marks the init.
func TestProxiedServerInitStillRecordsVersionAndImportTime(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	a := newSharedProxiedProject(t, bd, "shvt")
	db := openProxiedDB(t, a)

	version, ok := readProxiedMetadataRow(t, db, "local_metadata", "bd_version")
	require.True(t, ok, "proxied init recorded no bd_version in local_metadata")
	require.NotEmpty(t, version, "bd_version is empty")

	importedAt, ok := readProxiedMetadataRow(t, db, "metadata", "last_import_time")
	require.True(t, ok, "proxied init recorded no last_import_time in metadata")
	_, err := time.Parse(time.RFC3339, importedAt)
	require.NoError(t, err, "last_import_time %q is not an RFC3339 timestamp", importedAt)
}
