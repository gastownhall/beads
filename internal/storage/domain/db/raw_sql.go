package db

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
)

func NewRawSQLRepository(runner Runner) domain.RawSQLRepository {
	return &rawSQLRepositoryImpl{runner: runner}
}

type rawSQLRepositoryImpl struct {
	runner Runner
}

var _ domain.RawSQLRepository = (*rawSQLRepositoryImpl)(nil)

func (r *rawSQLRepositoryImpl) Query(ctx context.Context, query string, args ...any) (*domain.RawSQLResult, error) {
	rows, err := r.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: RawSQL Query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("db: RawSQL Query: columns: %w", err)
	}

	result := &domain.RawSQLResult{Columns: columns}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("db: RawSQL Query: scan: %w", err)
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: RawSQL Query: rows: %w", err)
	}
	return result, nil
}

func (r *rawSQLRepositoryImpl) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := r.runner.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("db: RawSQL Exec: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: RawSQL Exec: rows affected: %w", err)
	}
	return affected, nil
}
