//go:build windows

package versioncontrolops

import (
	neturl "net/url"
	"strings"
	"testing"
)

// TestDirToFileURLWindowsPathParses pins bd-2das8g (GH#6019): on Windows,
// filepath.Abs returns a drive-letter path with backslashes, and gluing that
// onto "file://" yields "file://C:\Users\...", which net/url (and so Dolt's
// DOLT_BACKUP('add')) rejects with `invalid port ":\Users\..." after host`.
// The URL BackupToDir registers as backup_export must parse and carry
// forward slashes.
func TestDirToFileURLWindowsPathParses(t *testing.T) {
	dir := `C:\Users\alxta\atl-logs-parser\.beads\backup`
	got, err := DirToFileURL(dir)
	if err != nil {
		t.Fatalf("DirToFileURL(%q): %v", dir, err)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("DirToFileURL(%q) = %q, want a file URL with forward slashes", dir, got)
	}
	if _, err := neturl.Parse(got); err != nil {
		t.Errorf("DirToFileURL(%q) = %q, which does not parse as a URL: %v", dir, got, err)
	}
}
