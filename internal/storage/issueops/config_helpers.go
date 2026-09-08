package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
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

func ResolveCustomConfigInTx(ctx context.Context, tx DBTX) (statuses []types.CustomStatus, customTypes []string, err error) {
	statuses, statusesFromTable, err := resolveCustomStatusesFromTableInTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	customTypes, typesFromTable, err := resolveCustomTypesFromTableInTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if statusesFromTable && typesFromTable {
		return statuses, customTypes, nil
	}

	cfg, err := getConfigKeysInTx(ctx, tx, "status.custom", "types.custom")
	if err != nil {
		if !statusesFromTable {
			if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
				statuses = ParseStatusFallback(yamlStatuses)
			}
		}
		if !typesFromTable {
			if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
				customTypes = yamlTypes
			}
		}
		return statuses, customTypes, nil
	}

	if !statusesFromTable {
		if v := cfg["status.custom"]; v != "" {
			if parsed, parseErr := types.ParseCustomStatusConfig(v); parseErr == nil {
				statuses = parsed
			}
		} else if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			statuses = ParseStatusFallback(yamlStatuses)
		}
	}
	if !typesFromTable {
		if v := cfg["types.custom"]; v != "" {
			var jsonTypes []string
			if jsonErr := json.Unmarshal([]byte(v), &jsonTypes); jsonErr == nil {
				customTypes = jsonTypes
			} else {
				customTypes = ParseCommaSeparatedList(v)
			}
		} else if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			customTypes = yamlTypes
		}
	}
	return statuses, customTypes, nil
}

func resolveCustomStatusesFromTableInTx(ctx context.Context, tx DBTX) ([]types.CustomStatus, bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name, category FROM custom_statuses ORDER BY name")
	if err != nil {
		return nil, false, nil
	}
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
		return nil, false, fmt.Errorf("reading custom_statuses: %w", err)
	}
	return result, len(result) > 0, nil
}

func resolveCustomTypesFromTableInTx(ctx context.Context, tx DBTX) ([]string, bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM custom_types ORDER BY name")
	if err != nil {
		return nil, false, nil
	}
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
		return nil, false, fmt.Errorf("reading custom_types: %w", err)
	}
	return result, len(result) > 0, nil
}

func getConfigKeysInTx(ctx context.Context, tx DBTX, keys ...string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	//nolint:gosec // G201: only ? placeholders are formatted in.
	q := fmt.Sprintf("SELECT `key`, value FROM config WHERE `key` IN (%s)", strings.Join(placeholders, ","))
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get config keys: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string, len(keys))
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("get config keys: scan: %w", err)
		}
		result[k] = v
	}
	return result, rows.Err()
}

// ResolveCustomStatusesDetailedInTx reads custom statuses from the custom_statuses
// table, falling back to the config string and then config.yaml if the table
// doesn't exist (pre-migration databases).
// Returns nil on parse errors (degraded mode). Does not cache or log —
// callers layer those concerns on top.
func ResolveCustomStatusesDetailedInTx(ctx context.Context, tx DBTX) ([]types.CustomStatus, error) {
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
		// Table has rows — use them as the authoritative source.
		// If the table is empty (e.g. schema migration created the table but
		// failed to populate it from status.custom config), fall through to
		// the config string so existing custom statuses aren't silently lost.
		if len(result) > 0 {
			return result, nil
		}
	}

	// Fallback: table doesn't exist or is empty — read from config string
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
// falling back to config string when the table is empty or pre-migration,
// and always overlay-unions project-extension types from .beads/config.yaml on
// top of the database-side result (gastownhall/beads#4024).
//
// The overlay shape: built-in types are the enforced floor (handled by
// IsValidWithCustom), database-side custom_types are the next layer (operator-
// /agent-installed project types), and .beads/config.yaml's types.custom is
// the topmost union-add (project-extension types declared in version-controlled
// config). A bead whose type is in any of those three layers validates; a bead
// whose type is in none still fails cleanly.
//
// Does not cache — callers layer caching on top.
func ResolveCustomTypesInTx(ctx context.Context, tx DBTX) ([]string, error) {
	var fromDB []string

	// Try the normalized table first.
	rows, err := tx.QueryContext(ctx, "SELECT name FROM custom_types ORDER BY name")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			fromDB = append(fromDB, name)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("reading custom_types: %w", err)
		}
	}

	// If the table didn't yield any rows, fall through to the legacy config-string
	// fallback. The table-empty case is intentional: schema migration creates the
	// table but may not have populated it from the existing types.custom config
	// string yet.
	if len(fromDB) == 0 {
		value, err := GetConfigInTx(ctx, tx, "types.custom")
		if err != nil {
			return customTypesYAMLFallback(config.GetCustomTypesFromYAML, err)
		}
		if value != "" {
			// Try JSON array first (e.g. '["gate","convoy"]'), fall back to comma-separated.
			var jsonTypes []string
			if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
				fromDB = jsonTypes
			} else {
				fromDB = ParseCommaSeparatedList(value)
			}
		}
	}

	// Overlay-union with YAML types.custom regardless of the DB-side result.
	// gastownhall/beads#4024: project-extension types declared in .beads/config.yaml
	// must validate server-side even when the database side hasn't been migrated
	// or populated with those types.
	return mergeWithYAMLCustomTypes(fromDB, config.GetCustomTypesFromYAML), nil
}

// mergeWithYAMLCustomTypes returns the union of dbTypes and the YAML-declared
// types.custom set (in that order), with duplicates removed. dbTypes preserve
// their order; YAML-only types are appended in their declared order. nil
// inputs are tolerated. The function is the union-add step that implements
// gastownhall/beads#4024's overlay semantics; keeping it isolated lets the
// merge logic be unit-tested without sql.Tx mocking.
func mergeWithYAMLCustomTypes(dbTypes []string, yamlGetter func() []string) []string {
	if yamlGetter == nil {
		return dbTypes
	}
	yamlTypes := yamlGetter()
	if len(yamlTypes) == 0 {
		return dbTypes
	}
	if len(dbTypes) == 0 {
		// Dedup YAML alone too, in case the YAML file itself listed the same
		// type twice (a configuration smell but not worth failing on).
		return dedupePreservingOrder(yamlTypes)
	}
	seen := make(map[string]struct{}, len(dbTypes)+len(yamlTypes))
	out := make([]string, 0, len(dbTypes)+len(yamlTypes))
	for _, t := range dbTypes {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range yamlTypes {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// customTypesYAMLFallback decides what to return from ResolveCustomTypesInTx
// when the in-tx config-string read errors. If YAML types.custom supplies any
// types, return them with a nil error so in-tx callers (NewBatchContext,
// UpdateIssueInTx) treat this as the YAML-fallback case rather than a fatal
// validation failure. If YAML has nothing, propagate the original error.
// Mirrors the YAML-fallback shape used by ResolveCustomStatusesDetailedInTx,
// and preserves the gastownhall/beads#4024 overlay goal under degraded /
// pre-migration DB conditions. Extracted as a pure function so the fallback
// decision is testable without sql.Tx mocking.
func customTypesYAMLFallback(yamlGetter func() []string, dbErr error) ([]string, error) {
	merged := mergeWithYAMLCustomTypes(nil, yamlGetter)
	if len(merged) > 0 {
		return merged, nil
	}
	return nil, dbErr
}

func dedupePreservingOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// SyncCustomStatusesTable replaces all rows in custom_statuses with parsed config value.
// Used by both DoltStore and EmbeddedDoltStore when "status.custom" config changes.
func SyncCustomStatusesTable(ctx context.Context, tx DBTX, value string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	parsed, err := types.ParseCustomStatusConfig(value)
	if err != nil {
		return fmt.Errorf("invalid status.custom value: %w", err)
	}
	for _, s := range parsed {
		if _, err := tx.ExecContext(ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)",
			s.Name, string(s.Category)); err != nil {
			return err
		}
	}
	return nil
}

// SyncCustomTypesTable replaces all rows in custom_types with parsed config value.
// Used by both DoltStore and EmbeddedDoltStore when "types.custom" config changes.
func SyncCustomTypesTable(ctx context.Context, tx DBTX, value string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM custom_types"); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	names := parseTypesValue(value)
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, "INSERT INTO custom_types (name) VALUES (?)", name); err != nil {
			return err
		}
	}
	return nil
}

// parseTypesValue tries JSON array first, then falls back to comma-separated.
func parseTypesValue(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	// Try JSON array first (e.g. '["gate","convoy"]')
	var jsonTypes []string
	if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
		return jsonTypes
	}
	// Fall back to comma-separated
	return ParseCommaSeparatedList(value)
}

// EnsureCustomTypeInTx registers name as a custom type if it is not
// already a built-in type and not already in the custom_types table.
// This is used by bd mol pour/wisp to auto-register types that the
// formula system creates implicitly (e.g. "gate" for async coordination
// beads) so that operators don't have to run bd config set types.custom
// manually before pouring a formula with gate steps. See GH#3213.
func EnsureCustomTypeInTx(ctx context.Context, tx DBTX, name string) error {
	if types.IssueType(name).IsValid() {
		return nil
	}
	existing, err := ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range existing {
		if t == name {
			return nil
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO custom_types (name) VALUES (?)", name)
	return err
}

// ResolveInfraTypesInTx reads infrastructure types from the database,
// falling back to config.yaml then to hardcoded defaults.
// Returns a map[string]bool for O(1) lookups.
// Does not cache — callers layer caching on top.
func ResolveInfraTypesInTx(ctx context.Context, tx DBTX) map[string]bool {
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
		typeList = domain.DefaultInfraTypes()
	}

	result := make(map[string]bool, len(typeList))
	for _, t := range typeList {
		result[t] = true
	}
	return result
}
