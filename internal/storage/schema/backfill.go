package schema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func ensureBackfilledCustomStatusesCustomTypes(ctx context.Context, db DBConn) (bool, error) {
	typesWrote, err := backfillCustomTypes(ctx, db)
	if err != nil {
		return typesWrote, fmt.Errorf("backfill custom_types: %w", err)
	}
	statusesWrote, err := backfillCustomStatuses(ctx, db)
	if err != nil {
		return typesWrote || statusesWrote, fmt.Errorf("backfill custom_statuses: %w", err)
	}
	return typesWrote || statusesWrote, nil
}

// customTableBackfillProbeQuery reads in one statement everything
// needsBackfilledCustomStatusesCustomTypes decides on: the row count of each
// custom table and the config value each one is backfilled from. config.value
// is NOT NULL (main migration 0006), so a NULL from a value subquery means the
// key has no row. The four values used to be four autocommit reads; one
// statement reads them from a single snapshot, which is at least as consistent.
const customTableBackfillProbeQuery = "SELECT " +
	"(SELECT COUNT(*) FROM custom_types), " +
	"(SELECT `value` FROM config WHERE `key` = 'types.custom'), " +
	"(SELECT COUNT(*) FROM custom_statuses), " +
	"(SELECT `value` FROM config WHERE `key` = 'status.custom')"

// needsBackfilledCustomStatusesCustomTypes reports whether custom_types or
// custom_statuses is empty while its config key holds values that the backfill
// would insert. It is the last question migrationWorkNeeded asks of a database
// that is otherwise current, so it sends one statement. Parsing, the
// empty and malformed fallbacks, and the error on a failed read stay here.
func needsBackfilledCustomStatusesCustomTypes(ctx context.Context, db DBConn) (bool, error) {
	var typesCount, statusesCount int
	var typesValue, statusesValue sql.NullString
	err := db.QueryRowContext(ctx, customTableBackfillProbeQuery).
		Scan(&typesCount, &typesValue, &statusesCount, &statusesValue)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			// The cursor says config and both custom tables exist, but one is
			// gone. One statement cannot say which, so ask the separate reads,
			// which give the answer they always gave: a missing custom table is
			// an error, and config is not read while the custom tables hold rows.
			return needsBackfilledCustomStatusesCustomTypesSeparately(ctx, db)
		}
		return false, fmt.Errorf("custom_types/custom_statuses backfill probe: %w", err)
	}
	typesNeed := typesCount == 0 && typesValue.Valid && customTypesConfigNeedsBackfill(typesValue.String)
	statusesNeed := statusesCount == 0 && statusesValue.Valid && customStatusesConfigNeedsBackfill(statusesValue.String)
	return typesNeed || statusesNeed, nil
}

// needsBackfilledCustomStatusesCustomTypesSeparately is the backfill decision
// made with one read per table and per config key. It serves only the case
// where the one-statement probe failed on a missing table.
func needsBackfilledCustomStatusesCustomTypesSeparately(ctx context.Context, db DBConn) (bool, error) {
	typesNeed, err := needsCustomTypesBackfill(ctx, db)
	if err != nil {
		return false, fmt.Errorf("custom_types: %w", err)
	}
	statusesNeed, err := needsCustomStatusesBackfill(ctx, db)
	if err != nil {
		return false, fmt.Errorf("custom_statuses: %w", err)
	}
	return typesNeed || statusesNeed, nil
}

func needsCustomTypesBackfill(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	var value string
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'types.custom'").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return customTypesConfigNeedsBackfill(value), nil
}

// customTypesConfigNeedsBackfill reports whether a types.custom config value
// names at least one type.
func customTypesConfigNeedsBackfill(value string) bool {
	return len(issueops.ParseTypesConfigValue(value)) > 0
}

// customStatusesConfigNeedsBackfill reports whether a status.custom config
// value names at least one valid status. An empty or malformed value needs no
// backfill: backfillCustomStatuses logs and skips it.
func customStatusesConfigNeedsBackfill(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	parsed, parseErr := types.ParseCustomStatusConfig(value)
	if parseErr != nil {
		return false
	}
	return len(parsed) > 0
}

func needsCustomStatusesBackfill(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	var value string
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'status.custom'").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return customStatusesConfigNeedsBackfill(value), nil
}

func backfillCustomTypes(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	var value string
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'types.custom'").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value == "" {
		return false, nil
	}

	wrote := false
	// ParseTypesConfigValue already trims elements and drops empties.
	for _, name := range issueops.ParseTypesConfigValue(value) {
		res, err := db.ExecContext(ctx, "INSERT IGNORE INTO custom_types (name) VALUES (?)", name)
		if err != nil {
			return wrote, fmt.Errorf("inserting type %q: %w", name, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			wrote = true
		}
	}
	return wrote, nil
}

func backfillCustomStatuses(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	var value string
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'status.custom'").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value == "" {
		return false, nil
	}

	parsed, parseErr := types.ParseCustomStatusConfig(value)
	if parseErr != nil {
		log.Printf("schema: skipping invalid status.custom entries: %v", parseErr)
		return false, nil
	}
	wrote := false
	for _, s := range parsed {
		res, err := db.ExecContext(ctx, "INSERT IGNORE INTO custom_statuses (name, category) VALUES (?, ?)", s.Name, string(s.Category))
		if err != nil {
			return wrote, fmt.Errorf("inserting status %q: %w", s.Name, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			wrote = true
		}
	}
	return wrote, nil
}
