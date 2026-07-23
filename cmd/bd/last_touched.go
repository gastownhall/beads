package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
)

const lastTouchedFile = "last-touched"

// GetLastTouchedID returns the ID of the last touched issue.
// Returns empty string if no last touched issue exists or the file is unreadable.
func GetLastTouchedID() string {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ""
	}

	lastTouchedPath := filepath.Join(beadsDir, lastTouchedFile)
	data, err := os.ReadFile(lastTouchedPath) // #nosec G304 -- path constructed from beadsDir
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// SetLastTouchedID saves the ID of the last touched issue.
// Silently ignores errors (best-effort tracking).
func SetLastTouchedID(issueID string) {
	if issueID == "" {
		return
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	lastTouchedPath := filepath.Join(beadsDir, lastTouchedFile)
	// Write with restrictive permissions (local-only state)
	if err := os.WriteFile(lastTouchedPath, []byte(issueID+"\n"), 0600); err != nil {
		return
	}
	// Always advance mtime, even when the same ID is rewritten, so file-watch
	// fingerprints and cache validators that key on mtime never see an
	// "identical" marker after a write (GH#3965).
	now := time.Now()
	_ = os.Chtimes(lastTouchedPath, now, now)
}

// ClearLastTouched removes the last touched file.
// Silently ignores errors.
func ClearLastTouched() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	lastTouchedPath := filepath.Join(beadsDir, lastTouchedFile)
	_ = os.Remove(lastTouchedPath)
}
