package doltutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

var cliRemoteLocks sync.Map

func cliRemoteLock(dbPath string) *sync.Mutex {
	lock, _ := cliRemoteLocks.LoadOrStore(dbPath, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

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
// git-over-HTTPS (git+https://), git+file://, and plain git:// protocol.
func IsGitProtocolURL(url string) bool {
	return IsSSHURL(url) ||
		strings.HasPrefix(url, "git+https://") ||
		strings.HasPrefix(url, "git+http://") ||
		strings.HasPrefix(url, "git+file://") ||
		strings.HasPrefix(url, "git://")
}

// PersistedRemotes reads the Dolt remotes recorded in
// <dbPath>/.dolt/repo_state.json directly, without shelling out to the dolt
// CLI — so it works when the dolt binary is absent and its failure modes are
// distinguishable (bd-6dnrw.33). A missing .dolt directory or repo_state.json
// means "not a dolt repository here" and returns (nil, nil); an unreadable or
// unparseable file returns an error so callers can tell "definitely none"
// from "could not tell". Results are sorted by name.
func PersistedRemotes(dbPath string) ([]storage.RemoteInfo, error) {
	path := filepath.Join(dbPath, ".dolt", "repo_state.json")
	data, err := os.ReadFile(path) // #nosec G304 -- repo-local dolt state file
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var state struct {
		Remotes map[string]struct {
			URL string `json:"url"`
		} `json:"remotes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	remotes := make([]storage.RemoteInfo, 0, len(state.Remotes))
	for name, r := range state.Remotes {
		remotes = append(remotes, storage.RemoteInfo{Name: name, URL: r.URL})
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	return remotes, nil
}

// ListCLIRemotes parses `dolt remote -v` output from the given database
// directory. This is a read-only guard for deciding whether CLI push/pull/fetch
// can safely run from that directory; remote mutation still goes through SQL.
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
		parts := strings.Fields(line)
		if len(parts) >= 2 && !seen[parts[0]] {
			seen[parts[0]] = true
			remotes = append(remotes, storage.RemoteInfo{Name: parts[0], URL: parts[1]})
		}
	}
	return remotes, nil
}

// RemoteURLsMatch compares remote URLs after Dolt-compatible normalization.
func RemoteURLsMatch(got, want string) bool {
	if got == "" || want == "" {
		return got == want
	}
	if got == want || doltremote.Normalize(got) == doltremote.Normalize(want) {
		return true
	}
	return false
}

// AddCLIRemote adds a remote at the filesystem level via dolt CLI.
// Remote mutation should normally go through SQL; this is reserved for the
// local CLI mirror required by subprocess push/pull/fetch routing.
func AddCLIRemote(dbPath, name, url string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	if err := remotecache.ValidateRemoteURL(url); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}
	cmd := exec.Command("dolt", "remote", "add", name, url) // #nosec G204 -- validated argv
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt remote add failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RemoveCLIRemote removes a remote at the filesystem level via dolt CLI.
func RemoveCLIRemote(dbPath, name string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	cmd := exec.Command("dolt", "remote", "remove", name) // #nosec G204 -- validated argv
	cmd.Dir = dbPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt remote remove failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// FindCLIRemote returns the URL for a named remote in dbPath, or "" when the
// directory cannot be inspected or the remote is absent.
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

// EnsureCLIRemote makes the local CLI remote match the SQL-visible remote URL.
// It is intentionally idempotent and only mutates the CLI surface when the
// remote is absent or points somewhere else.
func EnsureCLIRemote(dbPath, name, url string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	if err := remotecache.ValidateRemoteURL(url); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}

	lock := cliRemoteLock(dbPath)
	lock.Lock()
	defer lock.Unlock()

	current := FindCLIRemote(dbPath, name)
	if RemoteURLsMatch(current, url) {
		return nil
	}
	if current != "" {
		if err := RemoveCLIRemote(dbPath, name); err != nil {
			return err
		}
	}
	if err := AddCLIRemote(dbPath, name, url); err != nil {
		if current == "" {
			return err
		}
		if restoreErr := AddCLIRemote(dbPath, name, current); restoreErr != nil {
			return fmt.Errorf("add replacement CLI remote failed: %w; additionally failed to restore previous URL %q: %v", err, current, restoreErr)
		}
		return fmt.Errorf("add replacement CLI remote failed; previous URL %q restored: %w", current, err)
	}
	return nil
}
