package migrations

import (
	"database/sql"
	"fmt"
	"strings"
)

func MigrateExternalRefUnique(db *sql.DB) error {
	var hasConstraint bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM sqlite_master
		WHERE type = 'index'
		  AND name = 'idx_issues_external_ref_unique'
	`).Scan(&hasConstraint)
	if err != nil {
		return fmt.Errorf("failed to check for UNIQUE constraint: %w", err)
	}

	if hasConstraint {
		return nil
	}

	existingDuplicates, err := findExternalRefDuplicates(db)
	if err != nil {
		return fmt.Errorf("failed to check for duplicate external_ref values: %w", err)
	}

	if len(existingDuplicates) > 0 {
		return fmt.Errorf("cannot add UNIQUE constraint: found %d duplicate external_ref values (resolve with 'bd duplicates' or manually)", len(existingDuplicates))
	}

	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_external_ref_unique ON issues(external_ref) WHERE external_ref IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("failed to create UNIQUE index on external_ref: %w", err)
	}

	return nil
}

func findExternalRefDuplicates(db *sql.DB) (map[string][]string, error) {
	rows, err := db.Query(`
		SELECT external_ref, GROUP_CONCAT(id, ',') as ids
		FROM issues
		WHERE external_ref IS NOT NULL
		GROUP BY external_ref
		HAVING COUNT(*) > 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := make(map[string][]string)
	for rows.Next() {
		var externalRef, idsCSV string
		if err := rows.Scan(&externalRef, &idsCSV); err != nil {
			return nil, err
		}
		ids := strings.Split(idsCSV, ",")
		duplicates[externalRef] = ids
	}

	return duplicates, rows.Err()
}
