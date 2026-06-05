package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
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

func (r *configSQLRepositoryImpl) GetCustomTypes(ctx context.Context) ([]string, error) {
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
	rows, err := r.runner.QueryContext(ctx, "SELECT name, category FROM custom_statuses ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("db: GetCustomStatuses: query custom_statuses: %w", err)
	}
	defer rows.Close()
	var result []types.CustomStatus
	for rows.Next() {
		var name, category string
		if err := rows.Scan(&name, &category); err != nil {
			return nil, fmt.Errorf("db: GetCustomStatuses: scan: %w", err)
		}
		result = append(result, types.CustomStatus{
			Name:     name,
			Category: types.StatusCategory(category),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: GetCustomStatuses: read custom_statuses: %w", err)
	}
	return result, nil
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
