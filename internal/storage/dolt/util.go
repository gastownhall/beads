package dolt

import (
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// parseTimeString delegates to issueops.ParseTimeString.
var parseTimeString = issueops.ParseTimeString

// parseNullableTimeString parses a nullable time string from database TEXT columns.
// For columns declared as TEXT (not DATETIME), we must parse manually.
// Supports RFC3339, RFC3339Nano, and DATETIME format (YYYY-MM-DD HH:MM:SS).
func parseNullableTimeString(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	// Try RFC3339Nano first (more precise), then RFC3339, then DATETIME format
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			return &t
		}
	}
	return nil // Unparseable - shouldn't happen with valid data
}
