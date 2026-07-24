package doltutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
)

var cliRemoteLocks sync.Map

func cliRemoteLock(dbPath string) *sync.Mutex {
	lock, _ := cliRemoteLocks.LoadOrStore(dbPath, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// listCLIRemotesTimeoutBroken caps `dolt remote -v` wallclock for a database
// directory that lacks .dolt/repo_state.json — the known broken-parent-dir
// failure mode (e.g. a multi-DB server root) that otherwise takes ~12s to
// error out. There is never a real answer coming from a directory in this
// state, so failing fast here carries no risk of mistaking a slow-but-valid
// remote list for "absent". (be-1he)
const listCLIRemotesTimeoutBroken = 2 * time.Second

// listCLIRemotesTimeoutHealthy caps `dolt remote -v` wallclock for a
// directory that does have .dolt/repo_state.json — a real Dolt repo. This is
// deliberately generous: callers such as FindCLIRemote fold any
// ListCLIRemotes error (including a timeout) into "remote absent", and
// EnsureCLIRemote then blind-adds on that signal, which hard-fails if the
// remote in fact exists. A real repo's `dolt remote -v` is ~130ms even when
// under load, so 30s only ever bites a genuinely hung subprocess — it must
// not be tightened to a value a slow-but-valid call could plausibly cross
// (review should-fix, 2026-07-24).
const listCLIRemotesTimeoutHealthy = 30 * time.Second

// listCLIRemotesTimeout picks the wallclock cap for dbPath based on whether
// it looks like a real Dolt repo (has .dolt/repo_state.json) or the known
// broken-parent-dir case (doesn't). Pure and stat-only so it's cheap to call
// per-invocation and independently testable without shelling out to dolt.
func listCLIRemotesTimeout(dbPath string) time.Duration {
	if _, err := os.Stat(filepath.Join(dbPath, ".dolt", "repo_state.json")); err != nil {
		return listCLIRemotesTimeoutBroken
	}
	return listCLIRemotesTimeoutHealthy
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
	ctx, cancel := context.WithTimeout(context.Background(), listCLIRemotesTimeout(dbPath))
	defer cancel()
	cmd := exec.CommandContext(ctx, "dolt", "remote", "-v") // #nosec G204 -- fixed command
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
