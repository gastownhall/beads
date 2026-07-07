package db

import (
	"context"
	"fmt"
	"strings"
)

type DoltVersionControlSQLRepository interface {
	Checkout(ctx context.Context, args ...string) error
	Branch(ctx context.Context, args ...string) error
	Add(ctx context.Context, args ...string) error
	Commit(ctx context.Context, args ...string) error
	Merge(ctx context.Context, args ...string) error
	Remote(ctx context.Context, args ...string) error
	Fetch(ctx context.Context, args ...string) error
	Push(ctx context.Context, args ...string) error
	Pull(ctx context.Context, args ...string) error
	Clone(ctx context.Context, args ...string) error
}

func NewDoltVersionControlSQLRepository(runner Runner) DoltVersionControlSQLRepository {
	return &doltVersionControlSQLRepository{runner: runner}
}

type doltVersionControlSQLRepository struct {
	runner Runner
}

var _ DoltVersionControlSQLRepository = (*doltVersionControlSQLRepository)(nil)

func (i *doltVersionControlSQLRepository) Checkout(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_CHECKOUT", args...)
}

func (i *doltVersionControlSQLRepository) Branch(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_BRANCH", args...)
}

func (i *doltVersionControlSQLRepository) Add(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_ADD", args...)
}

func (i *doltVersionControlSQLRepository) Commit(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_COMMIT", args...)
}

func (i *doltVersionControlSQLRepository) Merge(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_MERGE", args...)
}

func (i *doltVersionControlSQLRepository) Remote(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_REMOTE", args...)
}

func (i *doltVersionControlSQLRepository) Fetch(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_FETCH", args...)
}

func (i *doltVersionControlSQLRepository) Push(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_PUSH", args...)
}

func (i *doltVersionControlSQLRepository) Pull(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_PULL", args...)
}

func (i *doltVersionControlSQLRepository) Clone(ctx context.Context, args ...string) error {
	return i.call(ctx, "DOLT_CLONE", args...)
}

func (i *doltVersionControlSQLRepository) call(ctx context.Context, proc string, args ...string) error {
	placeholders := make([]string, len(args))
	iargs := make([]any, len(args))
	for j, a := range args {
		placeholders[j] = "?"
		iargs[j] = a
	}
	query := "CALL " + proc + "(" + strings.Join(placeholders, ", ") + ")"
	if _, err := i.runner.ExecContext(ctx, query, iargs...); err != nil {
		return fmt.Errorf("db: %s: %w", proc, err)
	}
	return nil
}
