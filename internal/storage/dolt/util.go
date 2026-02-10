//go:build cgo

package dolt

import (
	"database/sql"
	"time"
)

// parseTimeString parses a time string from database TEXT columns (non-nullable).
// Used for required timestamp fields like created_at/updated_at when stored as TEXT.
// Returns zero time if parsing fails, which maintains backwards compatibility.
func parseTimeString(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339Nano first (more precise), then RFC3339, then SQLite format
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{} // Unparseable - shouldn't happen with valid data
}

// parseNullableTimeString parses a nullable time string from database TEXT columns.
// For columns declared as TEXT (not DATETIME), we must parse manually.
// Supports RFC3339, RFC3339Nano, and SQLite's native format.
func parseNullableTimeString(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	// Try RFC3339Nano first (more precise), then RFC3339, then SQLite format
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			return &t
		}
	}
	return nil // Unparseable - shouldn't happen with valid data
}
