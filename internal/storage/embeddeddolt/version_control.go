//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *EmbeddedDoltStore) Commit(ctx context.Context, message string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
			return fmt.Errorf("dolt add: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", message); err != nil {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

func (s *EmbeddedDoltStore) AddRemote(ctx context.Context, name, url string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
		return err
	})
}

func (s *EmbeddedDoltStore) HasRemote(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM dolt_remotes WHERE name = ?", name).Scan(&count)
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
