// Package migration provides read-side access to the town-wide
// MIGRATION-FREEZE write-freeze sentinel used during Gas Town dolt
// migrations. The sentinel file itself is created and removed by the gt CLI
// (see gt migrate freeze/thaw, gastownhall/gastown's internal/migration
// package) — bd only reads it, before a write command runs, to refuse
// human-typed writes that would bypass the gt-layer gate (dc-6jaq).
package migration

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the freeze sentinel placed at the town root.
const FileName = "MIGRATION-FREEZE"

// Info is the parsed contents of a MIGRATION-FREEZE file.
type Info struct {
	Operator  string    // who initiated the freeze (e.g. "mayor", a username)
	Reason    string    // human-readable migration reason
	Timestamp time.Time // when the freeze was set
}

// FilePath returns the full path to the freeze sentinel file.
func FilePath(townRoot string) string {
	return filepath.Join(townRoot, FileName)
}

// IsFrozen reports whether a migration freeze is currently active.
func IsFrozen(townRoot string) bool {
	if townRoot == "" {
		return false
	}
	_, err := os.Stat(FilePath(townRoot))
	return err == nil
}

// Read parses the freeze sentinel. Returns nil if not frozen or unreadable.
func Read(townRoot string) *Info {
	if townRoot == "" {
		return nil
	}
	data, err := os.ReadFile(FilePath(townRoot))
	if err != nil {
		return nil
	}
	return parse(string(data))
}

// parse mirrors gt's own freeze-file format: operator, RFC3339 timestamp,
// and reason, tab-separated on a single line.
func parse(content string) *Info {
	content = strings.TrimSpace(content)
	if content == "" {
		// No recorded timestamp to parse (e.g. an empty sentinel file, such
		// as one created with `touch`) — leave Timestamp at its zero value
		// rather than fabricating "now": a caller that ever inspects it
		// should not be told the freeze started at check-time.
		return &Info{}
	}
	parts := strings.SplitN(content, "\t", 3)
	info := &Info{}
	if len(parts) >= 1 {
		info.Operator = parts[0]
	}
	if len(parts) >= 2 {
		if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
			info.Timestamp = t
		}
	}
	if len(parts) >= 3 {
		info.Reason = parts[2]
	}
	return info
}
