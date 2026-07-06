package schema

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

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

func needsBackfilledCustomStatusesCustomTypes(ctx context.Context, db DBConn) (bool, error) {
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
	return len(parseTypesValue(value)) > 0, nil
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
	if strings.TrimSpace(value) == "" {
		return false, nil
	}

	parsed, parseErr := types.ParseCustomStatusConfig(value)
	if parseErr != nil {
		return false, nil
	}
	return len(parsed) > 0, nil
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
	for _, name := range parseTypesValue(value) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
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

func parseTypesValue(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var jsonTypes []string
	if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
		return jsonTypes
	}
	parts := strings.Split(value, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
