package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/config"
)

// resolveSyncRemote returns the effective sync remote URL.
// Resolution order:
//  1. sync.remote (primary — any Dolt-compatible remote URL)
//  2. sync.git-remote (deprecated fallback)
//  3. "" (not configured)
func resolveSyncRemote() string {
	if v := config.GetString("sync.remote"); v != "" {
		return v
	}
	return config.GetString("sync.git-remote")
}

// resolveSyncRemoteFromDir is like resolveSyncRemote but reads from a
// specific beads directory's config.yaml. Used by context_cmd, doctor,
// and other paths that operate on a resolved beads dir rather than CWD.
func resolveSyncRemoteFromDir(beadsDir string) string {
	if v := config.GetStringFromDir(beadsDir, "sync.remote"); v != "" {
		return v
	}
	return config.GetStringFromDir(beadsDir, "sync.git-remote")
}

// commitBeadsConfig stages .beads/config.yaml and commits it.
// Silently no-ops if the file is clean or the commit fails (e.g. hooks,
// nothing to commit). Used by bd dolt remote add/remove to keep the
// working tree clean after persisting sync.remote.
//
// The commit runs with --no-verify so the beads pre-commit hook (which
// shells out to `bd export`) does not re-enter bd while the parent
// `bd dolt remote add/remove` process still holds the embedded Dolt flock
// — that re-entry deadlocks the whole process tree (GH#3340). Same
// rationale as init.go's bootstrap commit.
func commitBeadsConfig(msg string) {
	addCmd := exec.Command("git", "add", ".beads/config.yaml")
	if err := addCmd.Run(); err != nil {
		return
	}
	commitCmd := exec.Command("git", "commit", "--no-verify", "-m", msg) //nolint:gosec // G702: msg is from internal callers only, not user input
	if out, err := commitCmd.CombinedOutput(); err != nil {
		// "nothing to commit" is normal if the file was already staged
		if !strings.Contains(string(out), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit config change: %v\n", err)
		}
	}
}

// doltNativeSchemes are URL schemes that Dolt understands natively
// and should not be converted through gitURLToDoltRemote.
var doltNativeSchemes = []string{
	"dolthub://",
	"file://",
	"aws://",
	"gs://",
	"git+https://",
	"git+ssh://",
	"git+http://",
}

// normalizeRemoteURL converts a remote URL to a Dolt-compatible format.
// Dolt-native URLs (dolthub://, file://, aws://, gs://, git+...) are
// returned as-is. Git URLs (https://, ssh://, git@...) are converted
// via gitURLToDoltRemote. Unknown schemes are returned as-is and let
// dolt clone decide.
func normalizeRemoteURL(url string) string {
	for _, scheme := range doltNativeSchemes {
		if strings.HasPrefix(url, scheme) {
			return url
		}
	}
	// Git-style URLs need conversion to dolt remote format
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") ||
		strings.HasPrefix(url, "ssh://") {
		return gitURLToDoltRemote(url)
	}
	// SCP-style git@host:path
	if idx := strings.Index(url, ":"); idx > 0 && !strings.Contains(url[:idx], "/") && strings.Contains(url, "@") {
		return gitURLToDoltRemote(url)
	}
	// Unknown scheme — return as-is, let dolt handle it
	return url
}
