package schema

import (
	"context"
	"fmt"
)

// MetadataIndexSpec describes a STORED generated column plus secondary index
// that mirrors a JSON metadata key, enabling indexed equality filtering.
//
// Column and Index must be safe SQL identifiers (the caller builds them via
// storage.MetadataColumnName / storage.MetadataIndexName, which sanitize the
// key). JSONPath is a validated JSON path expression (storage.JSONMetadataPath).
type MetadataIndexSpec struct {
	Column   string // e.g. "bd_md_alias"
	Index    string // e.g. "idx_bd_md_alias"
	JSONPath string // e.g. `$.alias`
}

// EnsureMetadataIndexes idempotently creates, for each spec, a STORED generated
// column mirroring issues.metadata at JSONPath plus a secondary index, so that
// equality filters on that metadata key use an index instead of a full
// JSON_EXTRACT scan. Columns/indexes that already exist are left untouched.
//
// Background: Dolt (like MySQL) cannot index a JSON column directly, and its
// optimizer does NOT rewrite a JSON_EXTRACT predicate onto a generated-column
// index — verified empirically — so the query layer must target the generated
// column (see issueops.BuildIssueFilterClauses and
// types.IssueFilter.IndexedMetadataKeys). On a 31k-row store this is ~24x
// faster for the per-key equality lookups used by session/mail routing.
func EnsureMetadataIndexes(ctx context.Context, db DBConn, specs []MetadataIndexSpec) error {
	for _, s := range specs {
		hasCol, err := metadataColumnExists(ctx, db, "issues", s.Column)
		if err != nil {
			return err
		}
		if !hasCol {
			// Generated-column expression cannot be a bound parameter; Column and
			// JSONPath are sanitized/validated by the caller.
			ddl := fmt.Sprintf(
				"ALTER TABLE issues ADD COLUMN %s VARCHAR(255) AS (JSON_UNQUOTE(JSON_EXTRACT(metadata, '%s'))) STORED",
				s.Column, s.JSONPath)
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("add metadata column %s: %w", s.Column, err)
			}
		}

		hasIdx, err := metadataIndexExists(ctx, db, "issues", s.Index)
		if err != nil {
			return err
		}
		if !hasIdx {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE INDEX %s ON issues (%s)", s.Index, s.Column)); err != nil {
				return fmt.Errorf("create metadata index %s: %w", s.Index, err)
			}
		}
	}
	return nil
}

func metadataColumnExists(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?",
		table, column).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking column %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}

func metadataIndexExists(ctx context.Context, db DBConn, table, index string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_name = ? AND index_name = ?",
		table, index).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking index %s on %s: %w", index, table, err)
	}
	return n > 0, nil
}
