package doltutil

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// IsSSHURL returns true if the URL uses SSH transport.
// Matches git+ssh://, ssh://, and git@host: patterns.
func IsSSHURL(url string) bool {
	return strings.HasPrefix(url, "git+ssh://") ||
		strings.HasPrefix(url, "ssh://") ||
		strings.HasPrefix(url, "git@")
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
func AddCLIRemote(dbPath, name, url string) error {
	cmd := exec.Command("dolt", "remote", "add", name, url) // #nosec G204
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt remote add failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RemoveCLIRemote removes a remote at the filesystem level via dolt CLI.
func RemoveCLIRemote(dbPath, name string) error {
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
