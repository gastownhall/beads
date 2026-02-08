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
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	output, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("git", "rev-parse", "--show-toplevel")
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("not a git repository")
		}

		repoPath := strings.TrimSpace(string(output))
		absPath, err := filepath.Abs(repoPath)
		if err != nil {
			absPath = repoPath
		}

		evalPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			evalPath = absPath
		}

		normalized := filepath.ToSlash(evalPath)
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
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w", err)
	}

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	repoRoot := strings.TrimSpace(string(output))
	absPath, err := filepath.Abs(repoRoot)
	if err != nil {
		absPath = repoRoot
	}

	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		evalPath = absPath
	}

	normalizedPath := filepath.ToSlash(evalPath)
	hash := sha256.Sum256([]byte(hostname + ":" + normalizedPath))
	return hex.EncodeToString(hash[:8]), nil
}
