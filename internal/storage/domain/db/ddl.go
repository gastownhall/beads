package db

import (
	"context"
	"fmt"
	"regexp"
)

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type DDLSQLRepository interface {
	CreateDatabaseIfNotExists(ctx context.Context, database string) error
	UseDatabase(ctx context.Context, database string) error
}

func NewDDLSQLRepository(runner Runner) DDLSQLRepository {
	return &ddlSQLRepository{runner: runner}
}

type ddlSQLRepository struct {
	runner Runner
}

var _ DDLSQLRepository = (*ddlSQLRepository)(nil)

func (r *ddlSQLRepository) CreateDatabaseIfNotExists(ctx context.Context, database string) error {
	ident, err := quoteIdentifier(database)
	if err != nil {
		return fmt.Errorf("db: CreateDatabaseIfNotExists: %w", err)
	}
	if _, err := r.runner.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+ident); err != nil {
		return fmt.Errorf("db: CreateDatabaseIfNotExists: %w", err)
	}
	return nil
}

func (r *ddlSQLRepository) UseDatabase(ctx context.Context, database string) error {
	ident, err := quoteIdentifier(database)
	if err != nil {
		return fmt.Errorf("db: UseDatabase: %w", err)
	}
	if _, err := r.runner.ExecContext(ctx, "USE "+ident); err != nil {
		return fmt.Errorf("db: UseDatabase: %w", err)
	}
	return nil
}

func quoteIdentifier(name string) (string, error) {
	if !validIdentifier.MatchString(name) {
		return "", fmt.Errorf("invalid identifier: %q", name)
	}
	return "`" + name + "`", nil
}
