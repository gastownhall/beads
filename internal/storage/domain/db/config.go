package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func NewConfigSQLRepository(runner Runner) domain.ConfigSQLRepository {
	return &configSQLRepositoryImpl{runner: runner}
}

type configSQLRepositoryImpl struct {
	runner Runner
}

var _ domain.ConfigSQLRepository = (*configSQLRepositoryImpl)(nil)

func (r *configSQLRepositoryImpl) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := r.runner.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: GetMetadata %s: %w", key, err)
	}
	return value, nil
}

func (r *configSQLRepositoryImpl) SetMetadata(ctx context.Context, key, value string) error {
	if _, err := r.runner.ExecContext(ctx, "REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("db: SetMetadata %s: %w", key, err)
	}
	return nil
}

func (r *configSQLRepositoryImpl) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := r.runner.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: GetLocalMetadata %s: %w", key, err)
	}
	return value, nil
}

func (r *configSQLRepositoryImpl) SetLocalMetadata(ctx context.Context, key, value string) error {
	if _, err := r.runner.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("db: SetLocalMetadata %s: %w", key, err)
	}
	return nil
}

func (r *configSQLRepositoryImpl) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := r.runner.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: GetConfig %s: %w", key, err)
	}
	return value, nil
}

func (r *configSQLRepositoryImpl) SetConfig(ctx context.Context, key, value string) error {
	if key == "issue_prefix" {
		value = strings.TrimSuffix(value, "-")
	}
	if _, err := r.runner.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("db: SetConfig %s: %w", key, err)
	}
	return nil
}

func (r *configSQLRepositoryImpl) DeleteConfig(ctx context.Context, key string) error {
	if _, err := r.runner.ExecContext(ctx, "DELETE FROM config WHERE `key` = ?", key); err != nil {
		return fmt.Errorf("db: DeleteConfig %s: %w", key, err)
	}
	return nil
}

func (r *configSQLRepositoryImpl) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := r.runner.QueryContext(ctx, "SELECT `key`, value FROM config")
	if err != nil {
		return nil, fmt.Errorf("db: GetAllConfig: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("db: GetAllConfig: scan: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: GetAllConfig: read: %w", err)
	}
	return out, nil
}

func (r *configSQLRepositoryImpl) GetCustomTypes(ctx context.Context) ([]string, error) {
	fromTable, err := r.readCustomTypesTable(ctx)
	if err != nil {
		return nil, err
	}

	fromDB := fromTable
	if len(fromDB) == 0 {
		fromConfig, err := r.readCustomTypesConfig(ctx)
		if err != nil {
			return nil, err
		}
		fromDB = fromConfig
	}

	return unionWithYAMLCustomTypes(fromDB, config.GetCustomTypesFromYAML()), nil
}

func (r *configSQLRepositoryImpl) readCustomTypesTable(ctx context.Context) ([]string, error) {
	rows, err := r.runner.QueryContext(ctx, "SELECT name FROM custom_types ORDER BY name")
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: GetCustomTypes: query custom_types: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: GetCustomTypes: scan custom_types: %w", err)
		}
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: GetCustomTypes: read custom_types: %w", err)
	}
	return out, nil
}

func (r *configSQLRepositoryImpl) readCustomTypesConfig(ctx context.Context) ([]string, error) {
	value, err := r.GetConfig(ctx, "types.custom")
	if err != nil {
		return nil, fmt.Errorf("db: GetCustomTypes: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var jsonTypes []string
	if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
		return parseCustomTypesList(jsonTypes), nil
	}
	return parseCustomTypesList(strings.Split(value, ",")), nil
}

func unionWithYAMLCustomTypes(dbTypes, yamlTypes []string) []string {
	if len(dbTypes) == 0 && len(yamlTypes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(dbTypes)+len(yamlTypes))
	out := make([]string, 0, len(dbTypes)+len(yamlTypes))
	for _, src := range [][]string{dbTypes, yamlTypes} {
		for _, t := range src {
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
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseCustomTypesList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *configSQLRepositoryImpl) GetAllowedPrefixes(ctx context.Context) (string, error) {
	value, err := r.GetConfig(ctx, "allowed_prefixes")
	if err != nil {
		return "", fmt.Errorf("db: GetAllowedPrefixes: %w", err)
	}
	return value, nil
}

func (r *configSQLRepositoryImpl) GetAdaptiveIDConfig(ctx context.Context) (domain.AdaptiveIDConfig, error) {
	cfg := domain.DefaultAdaptiveConfig()

	if probStr, err := r.GetConfig(ctx, "max_collision_prob"); err != nil {
		return cfg, fmt.Errorf("db: GetAdaptiveIDConfig: read max_collision_prob: %w", err)
	} else if probStr != "" {
		if prob, perr := strconv.ParseFloat(probStr, 64); perr == nil {
			cfg.MaxCollisionProbability = prob
		}
	}

	if minStr, err := r.GetConfig(ctx, "min_hash_length"); err != nil {
		return cfg, fmt.Errorf("db: GetAdaptiveIDConfig: read min_hash_length: %w", err)
	} else if minStr != "" {
		if v, perr := strconv.Atoi(minStr); perr == nil {
			cfg.MinLength = v
		}
	}

	if maxStr, err := r.GetConfig(ctx, "max_hash_length"); err != nil {
		return cfg, fmt.Errorf("db: GetAdaptiveIDConfig: read max_hash_length: %w", err)
	} else if maxStr != "" {
		if v, perr := strconv.Atoi(maxStr); perr == nil {
			cfg.MaxLength = v
		}
	}

	return cfg, nil
}

func (r *configSQLRepositoryImpl) GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error) {
	return issueops.ResolveCustomStatusesDetailedInTx(ctx, r.runner)
}

func (r *configSQLRepositoryImpl) ListAllStatusNames(ctx context.Context) ([]string, error) {
	builtins := []types.Status{
		types.StatusOpen, types.StatusInProgress, types.StatusBlocked,
		types.StatusDeferred, types.StatusClosed, types.StatusPinned, types.StatusHooked,
	}
	custom, err := r.GetCustomStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(builtins)+len(custom))
	for _, s := range builtins {
		out = append(out, string(s))
	}
	for _, c := range custom {
		out = append(out, c.Name)
	}
	return out, nil
}

func (r *configSQLRepositoryImpl) GetInfraTypes(ctx context.Context) (map[string]bool, error) {
	value, err := r.GetConfig(ctx, "types.infra")
	if err != nil {
		return nil, fmt.Errorf("db: GetInfraTypes: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return map[string]bool{}, nil
	}
	parts := strings.Split(value, ",")
	result := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result[p] = true
		}
	}
	return result, nil
}
