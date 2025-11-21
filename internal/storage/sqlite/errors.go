package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
)

// Sentinel errors for common database conditions
var (
	// ErrNotFound indicates the requested resource was not found in the database
	ErrNotFound = errors.New("not found")

	// ErrInvalidID indicates an ID format or validation error
	ErrInvalidID = errors.New("invalid ID")

	// ErrConflict indicates a unique constraint violation or conflicting state
	ErrConflict = errors.New("conflict")

	// ErrCycle indicates a dependency cycle would be created
	ErrCycle = errors.New("dependency cycle detected")
)

// wrapDBError wraps a database error with operation context
// It converts sql.ErrNoRows to ErrNotFound for consistent error handling
func wrapDBError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// wrapDBErrorf wraps a database error with formatted operation context
// It converts sql.ErrNoRows to ErrNotFound for consistent error handling
func wrapDBErrorf(err error, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	op := fmt.Sprintf(format, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// IsNotFound checks if an error is or wraps ErrNotFound
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsConflict checks if an error is or wraps ErrConflict
func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

// IsCycle checks if an error is or wraps ErrCycle
func IsCycle(err error) bool {
	return errors.Is(err, ErrCycle)
}
