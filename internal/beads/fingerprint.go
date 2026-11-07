package beads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RepoIDSource identifies how a repo fingerprint was derived.
type RepoIDSource string

const (
	// RepoIDSourceRemote means the fingerprint hashes the canonicalized
	// remote.origin.url — the same value on every host that clones the repo.
	RepoIDSourceRemote RepoIDSource = "remote"
	// RepoIDSourcePath means no origin remote was configured and the
	// fingerprint hashes the local repo root path — a host-local value that
	// can never match a remote-derived fingerprint stored by another host.
	RepoIDSourcePath RepoIDSource = "path"
)

// ComputeRepoID generates a unique identifier for this git repository
func ComputeRepoID() (string, error) {
	return ComputeRepoIDForPath("")
}

// ComputeRepoIDForPath generates a unique identifier for the git repository
// rooted at or containing repoPath. An empty repoPath uses the current cwd.
func ComputeRepoIDForPath(repoPath string) (string, error) {
	id, _, err := ComputeRepoIDForPathWithSource(repoPath)
	return id, err
}

// RepoFingerprint is the repository identity bd stamps into repo_id, plus
// how it was derived.
type RepoFingerprint struct {
	// ID is the fingerprint to store and compare.
	ID string
	// Source says whether ID hashes the remote URL or the local repo path.
	Source RepoIDSource
	// LegacyID is the pre-GH#5990 fingerprint of this checkout, which hashed
	// the written SSH alias, when it differs from ID; empty otherwise.
	LegacyID string
	// SSHAlias is the written SSH host when it was resolved to SSHHostName
	// through the local SSH configuration (GH#5990); empty otherwise.
	SSHAlias string
	// SSHHostName is the HostName the SSH alias resolved to; empty otherwise.
	SSHHostName string
}

// Matches reports whether a stored repo_id identifies this checkout: its
// current fingerprint, or the legacy literal-alias fingerprint a database
// stamped by this checkout before alias resolution still holds. It cannot
// recognize the legacy fingerprint of another clone's alias.
func (f RepoFingerprint) Matches(stored string) bool {
	if stored == "" {
		return false
	}
	return stored == f.ID || (f.LegacyID != "" && stored == f.LegacyID)
}

// AliasResolved reports whether the remote's SSH host alias was replaced by
// its configured HostName.
func (f RepoFingerprint) AliasResolved() bool {
	return f.SSHHostName != ""
}

// ComputeRepoIDForPathWithSource is ComputeRepoIDForPath plus which derivation
// produced the fingerprint, so callers (bd doctor, bd migrate --update-repo-id)
// can tell a canonical remote-derived fingerprint from the path fallback
// (bd-46vla: on a synced clone without the canonical origin remote, a
// path-fallback mismatch against the stored id is cosmetic, and stamping the
// local value into the versioned metadata table propagates it to every clone).
func ComputeRepoIDForPathWithSource(repoPath string) (string, RepoIDSource, error) {
	fp, err := ComputeRepoFingerprintForPath(repoPath)
	if err != nil {
		return "", "", err
	}
	return fp.ID, fp.Source, nil
}

// ComputeRepoFingerprintForPath computes the repository fingerprint for the
// git repository rooted at or containing repoPath. An empty repoPath uses
// the current cwd.
//
// GH#2867: When running from a git worktree, the path-based fallback (no remote)
// uses the main repository root instead of the worktree root. This ensures all
// worktrees sharing a database produce the same fingerprint. Without this,
// worktree operations would compute a different repo_id and bd doctor would
// report a fingerprint mismatch.
//
// GH#5990: an SSH remote written through a host alias (git@alias-a:org/repo)
// hashes the HostName the local SSH configuration maps the alias to, so
// clones reaching one repository through different aliases agree.
func ComputeRepoFingerprintForPath(repoPath string) (RepoFingerprint, error) {
	output, err := runGitInRepo(repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		// No remote configured — fall back to path-based fingerprint.
		// Use --git-common-dir to derive the main repo root so that
		// worktrees produce the same fingerprint as the main checkout.
		repoRoot, rootErr := mainRepoRootForPath(repoPath)
		if rootErr != nil {
			return RepoFingerprint{}, fmt.Errorf("not a git repository")
		}

		return RepoFingerprint{ID: hashRepoIdentity(normalizedRepoPath(repoRoot)), Source: RepoIDSourcePath}, nil
	}

	repoURL := strings.TrimSpace(string(output))
	canonical, err := canonicalizeGitURL(repoURL)
	if err != nil {
		return RepoFingerprint{}, fmt.Errorf("failed to canonicalize URL: %w", err)
	}

	fp := RepoFingerprint{ID: hashRepoIdentity(canonical), Source: RepoIDSourceRemote}
	if resolved, alias, hostName, ok := resolveSSHAliasCanonical(repoPath, repoURL, canonical); ok {
		fp.LegacyID = fp.ID
		fp.ID = hashRepoIdentity(resolved)
		fp.SSHAlias = alias
		fp.SSHHostName = hostName
	}
	return fp, nil
}

func hashRepoIdentity(canonical string) string {
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:16])
}

// Seams for SSH alias resolution, replaced by tests.
var (
	sshLookPath         = exec.LookPath
	sshResolveTimeout   = 2 * time.Second
	sshResolveWaitDelay = 500 * time.Millisecond
	resolveSSHHostName  = sshConfigHostName
)

// sshRemote is the destination part of an SSH remote URL.
type sshRemote struct {
	user string
	host string
	port string
}

// safeSSHToken matches the host and user tokens handed to ssh.
var safeSSHToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseSSHRemote splits an SSH remote (scp-style [user@]host:path, or an
// ssh://, git+ssh:// or ssh+git:// URL) into its destination. Other
// transports and local paths report false. Host detection mirrors
// canonicalizeGitURL so the written host is a prefix of its output.
func parseSSHRemote(rawURL string) (sshRemote, bool) {
	rawURL = strings.TrimSpace(rawURL)

	if i := strings.Index(rawURL, "://"); i >= 0 {
		switch strings.ToLower(rawURL[:i]) {
		case "ssh", "git+ssh", "ssh+git":
		default:
			return sshRemote{}, false
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" {
			return sshRemote{}, false
		}
		remote := sshRemote{host: u.Hostname(), port: u.Port()}
		if u.User != nil {
			remote.user = u.User.Username()
		}
		return remote, true
	}

	colonIdx := strings.Index(rawURL, ":")
	slashIdx := strings.Index(rawURL, "/")
	if colonIdx <= 0 || (slashIdx != -1 && slashIdx < colonIdx) {
		return sshRemote{}, false
	}
	if colonIdx == 1 {
		// A one-letter host is indistinguishable from a Windows drive (C:...).
		return sshRemote{}, false
	}
	hostPart := rawURL[:colonIdx]
	remote := sshRemote{host: hostPart}
	if atIdx := strings.LastIndex(hostPart, "@"); atIdx >= 0 {
		remote.user = hostPart[:atIdx]
		remote.host = hostPart[atIdx+1:]
	}
	return remote, true
}

// resolveSSHAliasCanonical returns the canonical identity of an SSH remote
// with its written host replaced by the HostName the local SSH configuration
// maps it to. It reports false, leaving the legacy identity in force,
// whenever the remote is not SSH, Git would use a custom SSH transport, or
// the alias cannot be resolved.
func resolveSSHAliasCanonical(repoPath, rawURL, legacyCanonical string) (canonical, alias, hostName string, ok bool) {
	remote, ok := parseSSHRemote(rawURL)
	if !ok {
		return "", "", "", false
	}
	host := strings.ToLower(remote.host)
	if !strings.HasPrefix(legacyCanonical, host) {
		return "", "", "", false
	}
	tail := legacyCanonical[len(host):]
	if !safeSSHToken.MatchString(host) || net.ParseIP(host) != nil {
		return "", "", "", false
	}
	// An unsafe user would be skipped rather than dropped: ssh applies
	// Match user rules, so a different user could select a different host.
	if remote.user != "" && !safeSSHToken.MatchString(remote.user) {
		return "", "", "", false
	}
	if customSSHTransport(repoPath) {
		return "", "", "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), sshResolveTimeout)
	defer cancel()
	out, err := resolveSSHHostName(ctx, remote.user, remote.host, remote.port)
	if err != nil {
		return "", "", "", false
	}
	resolved, ok := normalizeSSHHostName(out)
	if !ok || resolved == strings.TrimSuffix(host, ".") {
		return "", "", "", false
	}
	return resolved + tail, host, resolved, true
}

// customSSHTransport reports whether Git would reach the remote through an
// SSH command other than ssh with its default configuration, whose HostName
// mapping ssh -G cannot speak for.
func customSSHTransport(repoPath string) bool {
	for _, key := range []string{"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	out, err := runGitInRepo(repoPath, "config", "--get", "core.sshCommand")
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// normalizeSSHHostName validates a HostName printed by ssh -G and returns it
// lowercased without a trailing dot.
func normalizeSSHHostName(hostName string) (string, bool) {
	hostName = strings.TrimSuffix(strings.ToLower(hostName), ".")
	if net.ParseIP(hostName) != nil {
		return hostName, true
	}
	return hostName, safeSSHToken.MatchString(hostName)
}

// sshConfigHostName asks ssh which HostName its configuration gives the
// destination. ssh -G evaluates the configuration without connecting, but it
// is local policy, not a pure function: Match exec runs programs and
// CanonicalizeHostname may consult DNS. The lookup is bounded by ctx and
// WaitDelay; stdin and stderr are not connected, and an ssh found only
// through a relative PATH entry is refused.
func sshConfigHostName(ctx context.Context, user, host, port string) (string, error) {
	sshPath, err := sshLookPath("ssh")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(sshPath) {
		return "", fmt.Errorf("ssh resolved to relative path %q", sshPath)
	}

	dest := host
	if user != "" {
		dest = user + "@" + host
	}
	args := []string{"-G"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", dest)

	cmd := exec.CommandContext(ctx, sshPath, args...)
	cmd.WaitDelay = sshResolveWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseSSHConfigHostName(out)
}

// parseSSHConfigHostName returns the value of the first hostname line of
// ssh -G output.
func parseSSHConfigHostName(out []byte) (string, error) {
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimRight(line, "\r"))
		if len(fields) == 0 || !strings.EqualFold(fields[0], "hostname") {
			continue
		}
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed hostname line %q", line)
		}
		return fields[1], nil
	}
	return "", errors.New("no hostname in ssh -G output")
}

func canonicalizeGitURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)

	if strings.Contains(rawURL, "://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}

		host := strings.ToLower(u.Hostname())
		if port := u.Port(); port != "" && port != "22" && port != "80" && port != "443" {
			host = host + ":" + port
		}

		path := strings.TrimRight(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		path = filepath.ToSlash(path)

		return host + path, nil
	}

	// Detect scp-style URLs: [user@]host:path
	// Must contain ":" before any "/" and not be a Windows path
	colonIdx := strings.Index(rawURL, ":")
	slashIdx := strings.Index(rawURL, "/")
	if colonIdx > 0 && (slashIdx == -1 || colonIdx < slashIdx) {
		// Could be scp-style or Windows path (C:/)
		// Windows paths have colon at position 1 and are followed by backslash or forward slash
		if colonIdx == 1 && len(rawURL) > 2 && (rawURL[2] == '/' || rawURL[2] == '\\') {
			// Windows path, fall through to local path handling
		} else {
			// scp-style: [user@]host:path
			parts := strings.SplitN(rawURL, ":", 2)
			if len(parts) == 2 {
				hostPart := parts[0]
				pathPart := parts[1]

				atIdx := strings.LastIndex(hostPart, "@")
				if atIdx >= 0 {
					hostPart = hostPart[atIdx+1:]
				}

				host := strings.ToLower(hostPart)
				path := strings.TrimRight(pathPart, "/")
				path = strings.TrimSuffix(path, ".git")
				path = filepath.ToSlash(path)

				return host + "/" + path, nil
			}
		}
	}

	absPath, err := filepath.Abs(rawURL)
	if err != nil {
		absPath = rawURL
	}

	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		evalPath = absPath
	}

	return filepath.ToSlash(evalPath), nil
}

// GetCloneID generates a unique ID for this specific clone (not shared with other clones)
func GetCloneID() (string, error) {
	return GetCloneIDForPath("")
}

// GetCloneIDForPath generates a unique ID for the specific clone rooted at or
// containing repoPath. An empty repoPath uses the current cwd.
//
// GH#2867: Uses the main repo root (not worktree root) so that all worktrees
// sharing a database produce the same clone ID.
func GetCloneIDForPath(repoPath string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w", err)
	}

	repoRoot, err := mainRepoRootForPath(repoPath)
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	normalizedPath := normalizedRepoPath(repoRoot)
	hash := sha256.Sum256([]byte(hostname + ":" + normalizedPath))
	return hex.EncodeToString(hash[:8]), nil
}

func runGitInRepo(repoPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	return cmd.Output()
}

// mainRepoRootForPath returns the main repository root for a given path,
// correctly handling git worktrees. For worktrees, this returns the parent
// of --git-common-dir (the main repo root), not --show-toplevel (the
// worktree root). For regular repos, both are equivalent.
//
// GH#2867: This ensures fingerprint computation uses a stable path that is
// the same across all worktrees sharing a database.
func mainRepoRootForPath(repoPath string) (string, error) {
	// Get both toplevel and common-dir in one pass to detect worktrees.
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-common-dir")
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	lines := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected git rev-parse output")
	}

	toplevel := strings.TrimSpace(lines[0])
	commonDir := strings.TrimSpace(lines[1])

	// Resolve to absolute paths for comparison.
	absToplevel, err := filepath.Abs(toplevel)
	if err != nil {
		return toplevel, nil
	}

	// commonDir may be relative; resolve relative to the working directory
	// (repoPath if set, else CWD).
	if !filepath.IsAbs(commonDir) {
		base := repoPath
		if base == "" {
			base, _ = os.Getwd()
		}
		commonDir = filepath.Join(base, commonDir)
	}
	absCommonDir, err := filepath.Abs(commonDir)
	if err != nil {
		return absToplevel, nil
	}

	// Derive gitDir path for comparison. In a normal repo, --git-common-dir
	// equals <toplevel>/.git. In a worktree, it points to the main repo's .git.
	absGitDir := filepath.Join(absToplevel, ".git")

	if absCommonDir != absGitDir {
		// Worktree detected: the main repo root is the parent of common-dir.
		return filepath.Dir(absCommonDir), nil
	}

	// Regular repo: toplevel is the main root.
	return absToplevel, nil
}

func normalizedRepoPath(repoPath string) string {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}

	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		evalPath = absPath
	}

	return filepath.ToSlash(evalPath)
}
