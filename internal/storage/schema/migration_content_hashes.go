package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// validMigrationRefPattern matches the refs this package builds for AS OF reads
// (Dolt commit hashes or branch/remote-tracking names like
// "remotes/origin/main"). It mirrors issueops.ValidateRef but is kept local so
// the schema package — which sits below issueops — has no import-cycle risk.
var validMigrationRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

func validateMigrationRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref cannot be empty")
	}
	if len(ref) > 128 {
		return fmt.Errorf("ref too long")
	}
	if !validMigrationRefPattern.MatchString(ref) {
		return fmt.Errorf("invalid ref format: %s", ref)
	}
	return nil
}

// ReadMigrationContentHashes reads version -> content_hash from schema_migrations,
// either at HEAD (ref == "") or AS OF ref (e.g. "remotes/origin/main"). NULL/empty
// hashes are dropped. It returns an error when the table, column, or ref is
// unavailable; the caller classifies it with RemoteRefUnavailableErr /
// MissingMigrationObjectErr.
//
// Dolt requires a literal ref in AS OF: bind parameters (including inside CONCAT)
// fail server-side with `unbound variable "v1" in query`, so the validated ref is
// interpolated into the SQL text (bd-6dnrw.27).
func ReadMigrationContentHashes(ctx context.Context, db DBConn, ref string) (map[int]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if ref == "" {
		rows, err = db.QueryContext(ctx, "SELECT version, content_hash FROM schema_migrations")
	} else {
		if verr := validateMigrationRef(ref); verr != nil {
			return nil, fmt.Errorf("invalid ref: %w", verr)
		}
		// A cached ref may legitimately predate schema_migrations or its
		// content_hash column. Probe the historical shape before selecting from
		// it: letting the SELECT fail emits a Dolt server warning for every
		// read-only doctor run.
		//nolint:gosec // G201: ref is validated above — AS OF requires a literal, not a bind param
		hasTable, queryErr := queryHasRows(ctx, db,
			fmt.Sprintf("SHOW TABLES AS OF '%s' LIKE 'schema_migrations'", ref))
		if queryErr != nil {
			return nil, queryErr
		}
		if !hasTable {
			return nil, fmt.Errorf("table not found: schema_migrations at %q", ref)
		}
		//nolint:gosec // G201: ref is validated above — AS OF requires a literal, not a bind param
		hasContentHash, queryErr := queryHasRows(ctx, db,
			fmt.Sprintf("SHOW COLUMNS FROM schema_migrations AS OF '%s' LIKE 'content_hash'", ref))
		if queryErr != nil {
			return nil, queryErr
		}
		if !hasContentHash {
			return nil, fmt.Errorf("unknown column content_hash in schema_migrations at %q", ref)
		}
		//nolint:gosec // G201: ref is validated above — AS OF requires a literal, not a bind param
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

func queryHasRows(ctx context.Context, db DBConn, query string) (bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, nil
	}
	return false, rows.Err()
}

// RemoteRefUnavailableErr reports whether err means the AS OF ref does not exist
// locally (e.g. a remote-tracking ref that was never cached by a clone/pull).
// Dolt 2.x: "branch not found: remotes/origin/main".
func RemoteRefUnavailableErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "branch not found") ||
		strings.Contains(s, "invalid ref spec")
}

// MissingMigrationObjectErr reports whether err means schema_migrations or its
// content_hash column does not exist (at HEAD or at the AS OF ref) — an old
// database or an old cached ref, which is a legitimate "nothing to compare".
func MissingMigrationObjectErr(err error) bool {
	if dberrors.IsTableNotExist(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "table not found") ||
		(strings.Contains(s, "column") && strings.Contains(s, "could not be found")) ||
		strings.Contains(s, "unknown column")
}
