package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ParseStatusFallback converts legacy []string status names (from YAML) to []CustomStatus.
// Tries the new "name:category" format first; falls back to treating each entry
// as an untyped name with CategoryUnspecified.
func ParseStatusFallback(names []string) []types.CustomStatus {
	joined := strings.Join(names, ",")
	if parsed, err := types.ParseCustomStatusConfig(joined); err == nil {
		return parsed
	}
	result := make([]types.CustomStatus, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			result = append(result, types.CustomStatus{Name: name, Category: types.CategoryUnspecified})
		}
	}
	return result
}

// ParseCommaSeparatedList splits a comma-separated string into a slice of
// trimmed, non-empty entries.
func ParseCommaSeparatedList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// ResolveCustomStatusesDetailedInTx reads custom statuses from the custom_statuses
// table, falling back to the config string and then config.yaml if the table
// doesn't exist (pre-migration databases).
// Returns nil on parse errors (degraded mode). Does not cache or log —
// callers layer those concerns on top.
func ResolveCustomStatusesDetailedInTx(ctx context.Context, tx *sql.Tx) ([]types.CustomStatus, error) {
	// Try the normalized table first
	rows, err := tx.QueryContext(ctx, "SELECT name, category FROM custom_statuses ORDER BY name")
	if err == nil {
		defer rows.Close()
		var result []types.CustomStatus
		for rows.Next() {
			var name, category string
			if err := rows.Scan(&name, &category); err != nil {
				continue
			}
			result = append(result, types.CustomStatus{
				Name:     name,
				Category: types.StatusCategory(category),
			})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("reading custom_statuses: %w", err)
		}
		// Table query succeeded — return result even if empty.
		// Only fall through to config string when the table doesn't exist (query error above).
		return result, nil
	}

	// Fallback: table doesn't exist (pre-migration) — read from config string
	value, err := GetConfigInTx(ctx, tx, "status.custom")
	if err != nil {
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return ParseStatusFallback(yamlStatuses), nil
		}
		return nil, err
	}

	if value != "" {
		parsed, parseErr := types.ParseCustomStatusConfig(value)
		if parseErr != nil {
			return nil, nil
		}
		return parsed, nil
	}

	if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
		return ParseStatusFallback(yamlStatuses), nil
	}
	return nil, nil
}

// ResolveCustomTypesInTx reads custom issue types from the custom_types table,
// falling back to config string and then config.yaml if the table doesn't exist
// (pre-migration databases).
// Does not cache — callers layer caching on top.
func ResolveCustomTypesInTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	// Try the normalized table first
	rows, err := tx.QueryContext(ctx, "SELECT name FROM custom_types ORDER BY name")
	if err == nil {
		defer rows.Close()
		var result []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			result = append(result, name)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("reading custom_types: %w", err)
		}
		// Table query succeeded — return result even if empty.
		// Only fall through to config string when the table doesn't exist (query error above).
		return result, nil
	}

	// Fallback: table doesn't exist (pre-migration) — read from config string
	value, err := GetConfigInTx(ctx, tx, "types.custom")
	if err != nil {
		if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			return yamlTypes, nil
		}
		return nil, err
	}

	if value != "" {
		// Try JSON array first (e.g. '["gate","convoy"]'), fall back to comma-separated
		var jsonTypes []string
		if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
			return jsonTypes, nil
		}
		return ParseCommaSeparatedList(value), nil
	}

	if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
		return yamlTypes, nil
	}
	return nil, nil
}

// ResolveInfraTypesInTx reads infrastructure types from the database,
// falling back to config.yaml then to hardcoded defaults.
// Returns a map[string]bool for O(1) lookups.
// Does not cache — callers layer caching on top.
func ResolveInfraTypesInTx(ctx context.Context, tx *sql.Tx) map[string]bool {
	var typeList []string

	value, err := GetConfigInTx(ctx, tx, "types.infra")
	if err == nil && value != "" {
		typeList = ParseCommaSeparatedList(value)
	}

	if len(typeList) == 0 {
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			typeList = yamlTypes
		}
	}

	if len(typeList) == 0 {
		typeList = storage.DefaultInfraTypes()
	}

	result := make(map[string]bool, len(typeList))
	for _, t := range typeList {
		result[t] = true
	}
	return result
}
