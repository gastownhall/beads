package beads

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// RepoFingerprint is this checkout's repository identity plus the context
// needed to compare it against a stored repo_id.
//
// GH#5990: a checkout whose origin remote spells the forge through an SSH
// host alias (git@alias-a:org/repo.git) hashed differently from the same
// repository reached by its canonical host. When the local SSH
// configuration resolves such an alias to a canonical HostName, ID is the
// resolved-host fingerprint and LegacyID keeps the pre-change literal-host
// hash, so an id this same checkout stored earlier still matches. SSHAlias
// and SSHHostName are set only when an alias was resolved; every other
// derivation leaves them empty and ID equals LegacyID.
type RepoFingerprint struct {
	// ID is the fingerprint to compute and store going forward.
	ID string
	// Source reports whether the fingerprint derived from the origin remote
	// or from the local path fallback.
	Source RepoIDSource
	// LegacyID is the pre-GH#5990 fingerprint: the same remote hashed
	// without SSH alias resolution.
	LegacyID string
	// SSHAlias is the written host of the resolved SSH alias, if any.
	SSHAlias string
	// SSHHostName is the HostName the local SSH config resolved it to.
	SSHHostName string
}

// Matches reports whether stored is a fingerprint this checkout computed:
// the current ID, or the LegacyID of a checkout whose alias resolution just
// changed what it computes. It deliberately never recognizes another
// machine's alias spelling — a stored id can only match hashes derived from
// this checkout's own remote.
func (fp RepoFingerprint) Matches(stored string) bool {
	if stored == "" {
		return false
	}
	if stored == fp.ID {
		return true
	}
	return fp.LegacyID != "" && stored == fp.LegacyID
}

// ComputeRepoIDForPathWithSource is ComputeRepoIDForPath plus which derivation
// produced the fingerprint, so callers (bd doctor, bd migrate --update-repo-id)
// can tell a canonical remote-derived fingerprint from the path fallback
// (bd-46vla: on a synced clone without the canonical origin remote, a
// path-fallback mismatch against the stored id is cosmetic, and stamping the
// local value into the versioned metadata table propagates it to every clone).
//
// GH#2867: When running from a git worktree, the path-based fallback (no remote)
// uses the main repository root instead of the worktree root. This ensures all
// worktrees sharing a database produce the same fingerprint. Without this,
// worktree operations would compute a different repo_id and bd doctor would
// report a fingerprint mismatch.
func ComputeRepoIDForPathWithSource(repoPath string) (string, RepoIDSource, error) {
	fp, err := ComputeRepoFingerprintForPath(repoPath)
	if err != nil {
		return "", "", err
	}
	return fp.ID, fp.Source, nil
}

// ComputeRepoFingerprintForPath computes the repository fingerprint for the
// git repository rooted at or containing repoPath, resolving supported SSH
// host aliases through the local SSH configuration before hashing
// (GH#5990). Resolution failures are not errors: the fingerprint keeps its
// legacy literal-host value, matching what pre-GH#5990 code computed.
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

		normalized := normalizedRepoPath(repoRoot)
		id := repoIDHash(normalized)
		return RepoFingerprint{ID: id, LegacyID: id, Source: RepoIDSourcePath}, nil
	}

	repoURL := strings.TrimSpace(string(output))
	canonical, err := canonicalizeGitURL(repoURL)
	if err != nil {
		return RepoFingerprint{}, fmt.Errorf("failed to canonicalize URL: %w", err)
	}

	legacy := repoIDHash(canonical)
	fp := RepoFingerprint{ID: legacy, LegacyID: legacy, Source: RepoIDSourceRemote}
	if split, ok := splitSSHRemote(repoURL); ok {
		if host, resolved := resolveSSHAliasHost(split); resolved {
			fp.ID = repoIDHash(host + split.portSuffix + split.tail)
			fp.SSHAlias = strings.ToLower(split.host)
			fp.SSHHostName = host
		}
	}
	return fp, nil
}

func repoIDHash(canonical string) string {
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:16])
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
