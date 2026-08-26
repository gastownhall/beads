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
	// DatabaseExists reports whether the named database is present on the
	// server. It iterates SHOW DATABASES rather than using SHOW DATABASES LIKE
	// because Dolt treats _ and % as wildcards without backslash escaping, so
	// names like "beads_vulcan" would match unrelated databases.
	DatabaseExists(ctx context.Context, database string) (bool, error)
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

func (r *ddlSQLRepository) DatabaseExists(ctx context.Context, database string) (bool, error) {
	rows, err := r.runner.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return false, fmt.Errorf("db: DatabaseExists: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("db: DatabaseExists: %w", err)
		}
		if name == database {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("db: DatabaseExists: %w", err)
	}
	return false, nil
}

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

func quoteIdentifier(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return "`" + name + "`", nil
}
