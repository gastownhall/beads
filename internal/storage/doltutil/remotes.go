package doltutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
// from "could not tell". Results are sorted by name. Ref is the remote's
// git_ref parameter when recorded, else empty; a git_ref that is present but
// is not a string is unparseable in the same sense (see
// storage.GitRefFromParams) and is reported as an error naming the remote.
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
			URL    string         `json:"url"`
			Params map[string]any `json:"params"`
		} `json:"remotes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	remotes := make([]storage.RemoteInfo, 0, len(state.Remotes))
	for name, r := range state.Remotes {
		ref, err := storage.GitRefFromParams(r.Params)
		if err != nil {
			return nil, fmt.Errorf("remote %s in %s: %w", name, path, err)
		}
		remotes = append(remotes, storage.RemoteInfo{
			Name: name,
			URL:  r.URL,
			Ref:  ref,
		})
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	return remotes, nil
}

// ListCLIRemotes parses `dolt remote -v` output from the given database
// directory. This is a read-only guard for deciding whether CLI push/pull/fetch
// can safely run from that directory; remote mutation still goes through SQL.
// Ref is not populated here (the params column is not parsed from the
// listing); callers that need it use FindCLIRemoteRef.
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

// ValidateGitDataRefArg checks a git data ref before it becomes a dolt
// command-line argument. It refuses the bytes git's ref rules refuse, a
// space, an ASCII control character, and DEL, plus a leading dash, so the
// value can never be mistaken for an option. Dolt itself accepts all of them
// through exec, so this is bd policy at the argv boundary, not a ref-name
// check: a non-ASCII space such as U+00A0 is a valid ref byte for git and
// for dolt and passes here like any other byte, and the full ref-name rules
// belong to the command layer. An empty ref is valid and means Dolt's
// default.
func ValidateGitDataRefArg(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("git data ref %q must not start with a dash", ref)
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return fmt.Errorf("git data ref %q contains a space or an ASCII control character", ref)
		}
	}
	return nil
}

// SQLDoubleQuoted returns s as a MySQL string literal in double quotes, with
// the backslash and the double quote escaped, for a statement that is shown
// to a person to run rather than bound as a parameter.
func SQLDoubleQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// AddCLIRemote adds a remote at the filesystem level via dolt CLI.
// Remote mutation should normally go through SQL; this is reserved for the
// local CLI mirror required by subprocess push/pull/fetch routing.
//
// It has no production caller: EnsureCLIRemote, its only one, now goes through
// AddCLIRemoteWithRef. It is kept deliberately as the default-ref spelling for
// tests and callers that have no ref to carry, so a reader changing
// AddCLIRemoteWithRef does not have to re-derive that.
func AddCLIRemote(dbPath, name, url string) error {
	return AddCLIRemoteWithRef(dbPath, name, url, "")
}

// AddCLIRemoteWithRef is AddCLIRemote for a git-backed remote whose Dolt data
// lives on the git ref ref (`dolt remote add --ref`). An empty ref is
// AddCLIRemote.
func AddCLIRemoteWithRef(dbPath, name, url, ref string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	if err := remotecache.ValidateRemoteURL(url); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}
	ref = strings.TrimSpace(ref)
	if err := ValidateGitDataRefArg(ref); err != nil {
		return fmt.Errorf("invalid git data ref: %w", err)
	}
	args := []string{"remote", "add"}
	if ref != "" {
		args = append(args, "--ref", ref)
	}
	args = append(args, name, url)
	cmd := exec.Command("dolt", args...) // #nosec G204 -- validated argv
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

// cliRefProbePrefix names the throwaway remote probeCLIRemoteRef adds.
const cliRefProbePrefix = "bd-ref-probe-"

// ownCLIRefProbe names the throwaway remote this process's probe uses.
func ownCLIRefProbe() string {
	return fmt.Sprintf("%s%d", cliRefProbePrefix, os.Getpid())
}

// sweepLeftoverProbes removes probe remotes left behind by a run killed
// between its add and its remove: a remote named exactly as probeCLIRemoteRef
// names its probes whose pid is no longer running, and one under this
// process's own name (see leftoverProbe). EnsureCLIRemote runs it before
// every change to the mirror, whatever ref the change is onto, so a dead
// run's leftover is removed at the next re-materialization of that mirror
// where the platform has a liveness check (unix and windows; elsewhere only
// the own name is swept). For another run's leftover that removal is
// best-effort: a failure is warned about and tried again at the next change
// rather than failing the caller's own mirror change, and a pid an unrelated
// process has since taken keeps the leftover until that pid exits. A probe of
// a live run is left alone, so two bd processes re-materializing the same
// mirror at the same moment do not remove each other's probe. A leftover
// under this process's own name must go, or the probe's add would fail and be
// read as the server's refusal; its removal failing is reported as such.
func sweepLeftoverProbes(dbPath string) error {
	own := ownCLIRefProbe()
	remotes, err := PersistedRemotes(dbPath)
	if err != nil {
		return fmt.Errorf("read the recorded remotes in %s before changing the CLI mirror: %w", dbPath, err)
	}
	for _, r := range remotes {
		if !leftoverProbe(r.Name, own) {
			continue
		}
		if err := RemoveCLIRemote(dbPath, r.Name); err != nil {
			if r.Name == own {
				return fmt.Errorf("remove leftover ref probe remote %s in %s: %w", own, dbPath, err)
			}
			// Best-effort, but never silent: an un-removable leftover stays
			// visible in `bd remote list` until some run succeeds, and without
			// this line nothing ever says why.
			fmt.Fprintf(os.Stderr,
				"Warning: could not remove the ref probe remote %s left in %s by an exited run (will retry at the next mirror change): %v\n",
				r.Name, dbPath, err)
		}
	}
	return nil
}

// probeCLIRemoteRef adds and removes a throwaway remote with url and ref
// before EnsureCLIRemote removes the real one: a dolt CLI proxied to a
// running sql-server refuses remote parameters, and that refusal must come
// before anything is deleted. The caller has swept leftovers already.
func probeCLIRemoteRef(dbPath, url, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	probe := ownCLIRefProbe()
	if err := AddCLIRemoteWithRef(dbPath, probe, url, ref); err != nil {
		return fmt.Errorf("cannot record git data ref %s on the CLI mirror in %s (a dolt sql-server serving this directory refuses remote parameters over the CLI): %w", ref, dbPath, err)
	}
	if err := RemoveCLIRemote(dbPath, probe); err != nil {
		return fmt.Errorf("remove ref probe remote %s in %s: %w", probe, dbPath, err)
	}
	return nil
}

// leftoverProbe reports whether the remote called name may be swept as a
// probe left by an earlier run: this process's own probe name, or the exact
// shape probeCLIRemoteRef generates (the prefix followed by a pid in
// canonical decimal, nothing else) for a pid that is not running. A remote
// that merely shares the prefix, such as one a user named bd-ref-probe-backup,
// and the probe of a run still alive are not leftovers.
func leftoverProbe(name, own string) bool {
	if name == own {
		return true
	}
	digits, ok := strings.CutPrefix(name, cliRefProbePrefix)
	if !ok || digits == "" {
		return false
	}
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != digits {
		return false
	}
	return !probeProcessAlive(pid)
}

// FindCLIRemoteRef returns the git data ref recorded for the named remote in
// dbPath's .dolt/repo_state.json, verbatim (compare with
// storage.RemoteRefsMatch), or "" when the remote is absent or carries no
// ref. A state file that cannot be read or parsed is an error, never the
// default ref: treating it as the default would feed a remove-and-re-add of
// a ref remote onto refs/dolt/data.
func FindCLIRemoteRef(dbPath, name string) (string, error) {
	remotes, err := PersistedRemotes(dbPath)
	if err != nil {
		return "", err
	}
	for _, r := range remotes {
		if r.Name == name {
			return r.Ref, nil
		}
	}
	return "", nil
}

// EnsureCLIRemote makes the local CLI remote match the SQL-visible remote URL
// and git data ref (an empty ref is Dolt's default). It is intentionally
// idempotent and only mutates the CLI surface when the remote is absent,
// points somewhere else, or sits on a different ref; before it mutates, it
// sweeps probe remotes left by interrupted runs.
//
// Reading the mirror's recorded ref comes before the no-op short-circuit, so a
// state file that cannot be read fails the route for a default-ref remote too,
// not only for a remote with a ref. That is deliberate: an unreadable file
// cannot rule out a ref on the mirror, so a URL match alone does not prove the
// mirror is already correct, and continuing would push onto whatever ref the
// mirror is secretly pinned to. Failing is loud and mutates nothing.
func EnsureCLIRemote(dbPath, name, url, ref string) error {
	if err := remotecache.ValidateRemoteName(name); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	if err := remotecache.ValidateRemoteURL(url); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}
	if err := ValidateGitDataRefArg(strings.TrimSpace(ref)); err != nil {
		return fmt.Errorf("invalid git data ref: %w", err)
	}

	lock := cliRemoteLock(dbPath)
	lock.Lock()
	defer lock.Unlock()

	current := FindCLIRemote(dbPath, name)
	currentRef, err := FindCLIRemoteRef(dbPath, name)
	if err != nil {
		return fmt.Errorf("read the recorded ref of CLI remote %q in %s: %w", name, dbPath, err)
	}
	if RemoteURLsMatch(current, url) && storage.RemoteRefsMatch(currentRef, ref) {
		return nil
	}
	if err := sweepLeftoverProbes(dbPath); err != nil {
		return err
	}
	if current != "" {
		if err := probeCLIRemoteRef(dbPath, url, ref); err != nil {
			return err
		}
		if err := RemoveCLIRemote(dbPath, name); err != nil {
			return err
		}
	}
	if err := AddCLIRemoteWithRef(dbPath, name, url, ref); err != nil {
		if current == "" {
			return err
		}
		if restoreErr := AddCLIRemoteWithRef(dbPath, name, current, currentRef); restoreErr != nil {
			return fmt.Errorf("add replacement CLI remote failed: %w; additionally failed to restore previous URL %q: %v", err, current, restoreErr)
		}
		return fmt.Errorf("add replacement CLI remote failed; previous URL %q restored: %w", current, err)
	}
	return nil
}
