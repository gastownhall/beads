//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gastownhall/beads/internal/config"
	"github.com/gastownhall/beads/internal/storage/dolt"
	"github.com/gastownhall/beads/internal/types"
)

func (s *EmbeddedDoltStore) SetConfig(ctx context.Context, key, value string) error {
	// Normalize issue_prefix: strip trailing hyphen to avoid double-hyphen IDs,
	// matching DoltStore behavior.
	if key == "issue_prefix" {
		value = strings.TrimSuffix(value, "-")
	}
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES (?, ?)", key, value)
		return err
	})
}

func (s *EmbeddedDoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("embeddeddolt: get config %q: %w", key, err)
	}
	return value, nil
}

func (s *EmbeddedDoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	result := make(map[string]string)
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT `key`, value FROM config")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			result[k] = v
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *EmbeddedDoltStore) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("GetMetadata(%q): %w", key, err)
	}
	return value, nil
}

func (s *EmbeddedDoltStore) SetMetadata(ctx context.Context, key, value string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value)
		return err
	})
}

// GetInfraTypes returns the set of infrastructure types that should be routed
// to the wisps table. Reads from DB config "types.infra", falls back to YAML,
// then to hardcoded defaults (agent, rig, role, message).
func (s *EmbeddedDoltStore) GetInfraTypes(ctx context.Context) map[string]bool {
	var typeList []string

	value, err := s.GetConfig(ctx, "types.infra")
	if err == nil && value != "" {
		for _, t := range strings.Split(value, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				typeList = append(typeList, t)
			}
		}
	}

	if len(typeList) == 0 {
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			typeList = yamlTypes
		}
	}

	if len(typeList) == 0 {
		typeList = dolt.DefaultInfraTypes()
	}

	result := make(map[string]bool, len(typeList))
	for _, t := range typeList {
		result[t] = true
	}
	return result
}

// IsInfraTypeCtx returns true if the issue type is an infrastructure type.
func (s *EmbeddedDoltStore) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}
