package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ResolveCustomStatusesDetailedInTx reads custom statuses from the database,
// falling back to config.yaml if DB config is unavailable or empty.
// Returns nil on parse errors (degraded mode). Does not cache or log —
// callers layer those concerns on top.
func ResolveCustomStatusesDetailedInTx(ctx context.Context, tx *sql.Tx) ([]types.CustomStatus, error) {
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

// ResolveCustomTypesInTx reads custom issue types from the database,
// falling back to config.yaml if DB config is unavailable or empty.
// Does not cache — callers layer caching on top.
func ResolveCustomTypesInTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
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
