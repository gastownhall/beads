package doltutil

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

// ShellQuote returns s wrapped in single quotes with any embedded single
// quotes escaped, making it safe to interpolate into a shell command string.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// IsSSHURL returns true if the URL uses SSH transport.
// Matches git+ssh://, ssh://, and git@host: patterns.
func IsSSHURL(url string) bool {
	return strings.HasPrefix(url, "git+ssh://") ||
		strings.HasPrefix(url, "ssh://") ||
		strings.HasPrefix(url, "git@")
}

// IsGitProtocolURL returns true if the URL uses the git wire protocol.
// This includes SSH transports (git+ssh://, ssh://, git@host:) and
// git-over-HTTPS (git+https://) and plain git:// protocol.
// These remotes involve network I/O that can exceed MySQL connection
// timeouts and should use CLI-based push/pull instead of SQL.
func IsGitProtocolURL(url string) bool {
	return IsSSHURL(url) ||
		strings.HasPrefix(url, "git+https://") ||
		strings.HasPrefix(url, "git+http://") ||
		strings.HasPrefix(url, "git://")
}

// ListCLIRemotes parses `dolt remote -v` output from the given database directory.
func ListCLIRemotes(dbPath string) ([]storage.RemoteInfo, error) {
	cmd := exec.Command("dolt", "remote", "-v") // #nosec G204 -- fixed command
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dolt remote -v failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	seen := map[string]bool{}
	var remotes []storage.RemoteInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// dolt remote -v outputs: name <whitespace> url [<whitespace> (fetch|push)]
		parts := strings.Fields(line)
		if len(parts) >= 2 && !seen[parts[0]] {
			seen[parts[0]] = true
			remotes = append(remotes, storage.RemoteInfo{Name: parts[0], URL: parts[1]})
		}
	}
	return remotes, nil
}

// AddCLIRemote adds a remote at the filesystem level via dolt CLI.
// Both name and URL are validated before being passed to exec.Command
// as a defense-in-depth measure.
func AddCLIRemote(dbPath, name, url string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	if err := remotecache.ValidateRemoteURL(url); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}
	cmd := exec.Command("dolt", "remote", "add", name, url) // #nosec G204
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt remote add failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RemoveCLIRemote removes a remote at the filesystem level via dolt CLI.
// The name is validated before being passed to exec.Command.
func RemoveCLIRemote(dbPath, name string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	cmd := exec.Command("dolt", "remote", "remove", name) // #nosec G204
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt remote remove failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// FindCLIRemote returns the URL for a named CLI remote, or "" if not found.
func FindCLIRemote(dbPath, name string) string {
	remotes, err := ListCLIRemotes(dbPath)
	if err != nil {
		return ""
	}
	for _, r := range remotes {
		if r.Name == name {
			return r.URL
		}
	}
	return ""
}

// ToRemoteNameMap converts a RemoteInfo slice to a map keyed by name.
// Useful for de-duplicating remotes (e.g., from `dolt remote -v` which may list fetch+push).
func ToRemoteNameMap(remotes []storage.RemoteInfo) map[string]string {
	m := make(map[string]string, len(remotes))
	for _, r := range remotes {
		m[r.Name] = r.URL
	}
	return m
}
