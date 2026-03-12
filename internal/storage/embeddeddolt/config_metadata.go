//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
