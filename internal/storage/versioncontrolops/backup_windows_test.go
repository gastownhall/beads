//go:build windows

package versioncontrolops

import (
	neturl "net/url"
	"strings"
	"testing"
)

// TestDirToFileURLWindowsPathParses pins bd-2das8g (GH#6019) on a real
// Windows host, where filepath.Abs keeps the drive spelling it is handed
// (PowerShell keeps a lowercase one) and backslashes. Glued onto "file://",
// "c:\work\..." slipped past Dolt's URL parser, which special-cases only an
// uppercase drive letter, and fell through to net/url, which read "c:" as a
// host with the invalid port ":\work\...". The URL BackupToDir registers as
// backup_export must be the uppercase-drive, forward-slash form.
func TestDirToFileURLWindowsPathParses(t *testing.T) {
	dir := `c:\work\.beads\backup`
	got, err := DirToFileURL(dir)
	if err != nil {
		t.Fatalf("DirToFileURL(%q): %v", dir, err)
	}
	if want := "file://C:/work/.beads/backup"; got != want {
		t.Fatalf("DirToFileURL(%q) = %q, want %q", dir, got, want)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("DirToFileURL(%q) = %q, want a file URL with forward slashes", dir, got)
	}
	if _, err := neturl.Parse(got); err != nil {
		t.Errorf("DirToFileURL(%q) = %q, which net/url does not parse: %v", dir, got, err)
	}
}
