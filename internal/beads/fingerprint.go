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

// ComputeRepoID generates a unique identifier for this git repository
func ComputeRepoID() (string, error) {
	return ComputeRepoIDForPath("")
}

// ComputeRepoIDForPath generates a unique identifier for the git repository
// rooted at or containing repoPath. An empty repoPath uses the current cwd.
//
// GH#2867: When running from a git worktree, the path-based fallback (no remote)
// uses the main repository root instead of the worktree root. This ensures all
// worktrees sharing a database produce the same fingerprint. Without this,
// worktree operations would compute a different repo_id and bd doctor would
// report a fingerprint mismatch.
func ComputeRepoIDForPath(repoPath string) (string, error) {
	output, err := runGitInRepo(repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		// No remote configured — fall back to path-based fingerprint.
		// Use --git-common-dir to derive the main repo root so that
		// worktrees produce the same fingerprint as the main checkout.
		repoRoot, rootErr := mainRepoRootForPath(repoPath)
		if rootErr != nil {
			return "", fmt.Errorf("not a git repository")
		}

		normalized := normalizedRepoPath(repoRoot)
		hash := sha256.Sum256([]byte(normalized))
		return hex.EncodeToString(hash[:16]), nil
	}

	repoURL := strings.TrimSpace(string(output))
	canonical, err := canonicalizeGitURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("failed to canonicalize URL: %w", err)
	}

	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:16]), nil
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
