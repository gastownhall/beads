package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

const migrationContentSkewCheckName = "Migration Content Skew"

// CheckMigrationContentSkew detects when this database and its remote applied
// DIFFERENT content for the same migration version — the silent schema fork from
// gastownhall/beads#4259. It compares the local schema_migrations content hashes
// against the already-cached remote-tracking ref (no network fetch) and warns on
// any divergence.
//
// This is a read-only diagnostic; it never gates push/pull. It skips cleanly
// when there is no remote, no cached remote ref, or no recorded hashes to
// compare. In an embedded workspace SharedStore only ever holds a server-mode
// store (nil here), so the check falls back to opening the embedded database
// directly — embedded mode is what #4259 was reported against, and without
// the fallback the detection half of the guard never ran there (bd-578h9.13).
func CheckMigrationContentSkew(ss *SharedStore) DoctorCheck {
	if store := ss.Store(); store != nil {
		return checkMigrationContentSkew(context.Background(), store.DB(), store.RemoteName())
	}
	if check, ok := checkMigrationContentSkewEmbedded(context.Background(), sharedStoreBeadsDir(ss)); ok {
		return check
	}
	return DoctorCheck{
		Name:     migrationContentSkewCheckName,
		Status:   StatusOK,
		Message:  "N/A (no database)",
		Category: CategoryData,
	}
}

// checkMigrationContentSkewEmbedded opens the workspace's embedded Dolt
// database (read-only diagnostic queries only; nothing is written) and runs
// the skew comparison on it. Returns ok=false when there is no embedded
// database to inspect.
func checkMigrationContentSkewEmbedded(ctx context.Context, beadsDir string) (DoctorCheck, bool) {
	if beadsDir == "" {
		return DoctorCheck{}, false
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if _, err := os.Stat(dataDir); err != nil {
		return DoctorCheck{}, false
	}
	database := configfile.DefaultDoltDatabase
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "")
	if err != nil {
		return DoctorCheck{
			Name:     migrationContentSkewCheckName,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not check migration content skew (open embedded database): %v", err),
			Detail:   "The skew check failed to run; this does not mean skew exists. Re-run `bd doctor` and report the error if it persists.",
			Category: CategoryData,
		}, true
	}
	defer func() { _ = cleanup() }()
	return checkMigrationContentSkew(ctx, db, ""), true
}

// checkMigrationContentSkew compares local migration content hashes against the
// configured sync remote's cached tracking ref. remote is the sync remote name
// (DoltStore.RemoteName(), "origin" by default) — NOT whichever remote happens
// to sort first in dolt_remotes.
func checkMigrationContentSkew(ctx context.Context, db *sql.DB, remote string) DoctorCheck {
	ok := func(msg string) DoctorCheck {
		return DoctorCheck{Name: migrationContentSkewCheckName, Status: StatusOK, Message: msg, Category: CategoryData}
	}
	// "Cannot check" is NOT "checked and matches": surface unexpected failures
	// as a warning instead of swallowing them as OK (bd-6dnrw.27 — a broken
	// query made this check a silent permanent no-op).
	cannot := func(stage string, err error) DoctorCheck {
		return DoctorCheck{
			Name:     migrationContentSkewCheckName,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not check migration content skew (%s): %v", stage, err),
			Detail:   "The skew check failed to run; this does not mean skew exists. Re-run `bd doctor` and report the error if it persists.",
			Category: CategoryData,
		}
	}

	if remote == "" {
		remote = "origin"
	}

	// Without the configured sync remote there is nothing to compare against.
	var remoteCount int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_remotes WHERE name = ?", remote).Scan(&remoteCount); err != nil {
		if dberrors.IsTableNotExist(err) {
			return ok("No remote configured — nothing to compare")
		}
		return cannot("read dolt_remotes", err)
	}
	if remoteCount == 0 {
		return ok(fmt.Sprintf("Sync remote %q not configured — nothing to compare", remote))
	}

	branch := "main"
	var active string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&active); err == nil && active != "" {
		branch = active
	}

	local, err := readMigrationContentHashes(ctx, db, "")
	if err != nil {
		if missingObjectErr(err) {
			return ok("No local migration content hashes recorded yet")
		}
		return cannot("read local schema_migrations", err)
	}
	if len(local) == 0 {
		return ok("No local migration content hashes recorded yet")
	}

	ref := "remotes/" + remote + "/" + branch
	remoteHashes, err := readMigrationContentHashes(ctx, db, ref)
	if err != nil {
		// The remote-tracking ref is not cached yet (e.g. never pushed/pulled),
		// or the cached ref predates schema_migrations/content_hash.
		if remoteRefUnavailableErr(err) || missingObjectErr(err) {
			return ok(fmt.Sprintf("No cached remote ref %q to compare", ref))
		}
		return cannot(fmt.Sprintf("read schema_migrations at %q", ref), err)
	}

	skewed := schema.ContentHashSkew(local, remoteHashes)
	if len(skewed) == 0 {
		return ok(fmt.Sprintf("Applied migrations match remote %q", remote))
	}

	versions := make([]string, len(skewed))
	for i, v := range skewed {
		versions[i] = fmt.Sprintf("%04d", v)
	}
	return DoctorCheck{
		Name:     migrationContentSkewCheckName,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("This database and remote %q applied different content for migration(s) %s", remote, strings.Join(versions, ", ")),
		Detail:   "Two clones ran different migration content for the same version number — a silent schema fork (gastownhall/beads#4259). `bd dolt pull` may fail to merge cryptically.",
		Fix:      "Upgrade every clone to a bd version that carries the schema-convergence migration. If a merge already fails, make one clone canonical and re-bootstrap the others from the remote.",
		Category: CategoryData,
	}
}

// remoteRefUnavailableErr reports whether err means the AS OF ref does not
// exist locally. Dolt 2.x: "branch not found: remotes/origin/main".
func remoteRefUnavailableErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "branch not found") ||
		strings.Contains(s, "invalid ref spec")
}

// missingObjectErr reports whether err means schema_migrations or its
// content_hash column does not exist (at HEAD or at the AS OF ref) — an old
// database or an old cached ref, which is a legitimate "nothing to compare".
// Dolt 2.x AS OF phrasing: "table not found: x",
// `column "content_hash" could not be found in any table in scope`.
func missingObjectErr(err error) bool {
	if dberrors.IsTableNotExist(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "table not found") ||
		(strings.Contains(s, "column") && strings.Contains(s, "could not be found")) ||
		strings.Contains(s, "unknown column")
}

// readMigrationContentHashes reads version -> content_hash from
// schema_migrations, either at HEAD (ref == "") or AS OF ref (e.g.
// "remotes/origin/main"). NULL/empty hashes are dropped. It returns an error
// when the table, column, or ref is unavailable; the caller classifies it.
//
// Dolt requires a literal ref in AS OF: bind parameters (including inside
// CONCAT) fail server-side with `unbound variable "v1" in query`, so the
// validated ref is interpolated into the SQL text (bd-6dnrw.27).
func readMigrationContentHashes(ctx context.Context, db *sql.DB, ref string) (map[int]string, error) {
	var rows *sql.Rows
	var err error
	if ref == "" {
		rows, err = db.QueryContext(ctx, "SELECT version, content_hash FROM schema_migrations")
	} else {
		if err := issueops.ValidateRef(ref); err != nil {
			return nil, fmt.Errorf("invalid ref: %w", err)
		}
		//nolint:gosec // G201: ref is validated by issueops.ValidateRef above — AS OF requires a literal
		rows, err = db.QueryContext(ctx,
			fmt.Sprintf("SELECT version, content_hash FROM schema_migrations AS OF '%s'", ref))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var version int
		var hash sql.NullString
		if err := rows.Scan(&version, &hash); err != nil {
			return nil, err
		}
		if hash.Valid && hash.String != "" {
			out[version] = hash.String
		}
	}
	return out, rows.Err()
}
