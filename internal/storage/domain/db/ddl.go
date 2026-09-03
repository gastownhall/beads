package db

import (
	"context"
	"fmt"
	"regexp"
)

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const maxIdentifierLength = 64

// ValidateIdentifier checks whether name is safe to use, unquoted, as a
// database or table identifier in this package's DDL statements.
func ValidateIdentifier(name string) error {
	if len(name) > maxIdentifierLength {
		return fmt.Errorf("identifier too long: %q (max %d chars)", name, maxIdentifierLength)
	}
	if !validIdentifier.MatchString(name) {
		return fmt.Errorf("invalid identifier: %q", name)
	}
	return nil
}

type DDLSQLRepository interface {
	CreateDatabaseIfNotExists(ctx context.Context, database string) error
	// CreateDatabase issues a bare CREATE DATABASE (no IF NOT EXISTS) so the
	// server arbitrates creation atomically: success proves this call created
	// the database; an already-exists error (MySQL 1007) proves it did not.
	// The error is returned unmapped (wrapped with %w) so callers can
	// classify it against their driver.
	CreateDatabase(ctx context.Context, database string) error
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

func (r *ddlSQLRepository) CreateDatabase(ctx context.Context, database string) error {
	ident, err := quoteIdentifier(database)
	if err != nil {
		return fmt.Errorf("db: CreateDatabase: %w", err)
	}
	if _, err := r.runner.ExecContext(ctx, "CREATE DATABASE "+ident); err != nil {
		return fmt.Errorf("db: CreateDatabase: %w", err)
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

// QuoteIdentifier validates name and returns it back-quoted, ready to embed in
// a statement. It is the one place this repository's identifiers are quoted;
// callers outside the package that must build a schema-qualified name (see
// uow's convergence-probe database selector) go through it rather than
// re-deriving the rule.
func QuoteIdentifier(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return "`" + name + "`", nil
}

func quoteIdentifier(name string) (string, error) {
	return QuoteIdentifier(name)
}
