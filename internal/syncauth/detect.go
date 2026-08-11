package syncauth

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// findExecutable returns the path to name or an error if it is not in PATH.
func findExecutable(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH: %w", name, err)
	}
	return p, nil
}

// HostFromRemoteURL extracts a host from a git remote URL.
// It accepts https://host, ssh://git@host, and scp-like git@host:path forms.
func HostFromRemoteURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty remote URL")
	}

	// Add a scheme if missing so url.Parse can handle scp-like forms.
	u := raw
	if !strings.Contains(u, "://") {
		// scp-like: git@github.com:owner/repo.git
		if strings.Contains(u, "@") {
			u = "ssh://" + strings.Replace(u, ":", "/", 1)
		} else {
			u = "https://" + u
		}
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return "", fmt.Errorf("parse remote URL %q: %w", raw, err)
	}

	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("could not determine host from remote URL %q", raw)
	}
	return host, nil
}

// IsGitRemoteURL reports whether raw looks like a git remote URL.
// Non-git Dolt remotes such as dolthub://, s3://, az://, gs://, or mem://
// return false.
func IsGitRemoteURL(raw string) bool {
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		// scp-like git@host:path or a bare host; treat as git remote.
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git", "git+ssh":
		return true
	}
	return false
}

// hostProvider guesses the provider from a host name.
func hostProvider(host string) Provider {
	switch {
	case strings.EqualFold(host, "github.com") || strings.HasSuffix(host, ".github.com"):
		return ProviderGH
	case strings.EqualFold(host, "gitlab.com") || strings.HasSuffix(host, ".gitlab.com"):
		return ProviderGLab
	default:
		// Self-managed GitLab is the more common generic git host.
		return ProviderGLab
	}
}

// providerForHost returns a provider and its default OAuth endpoint host.
func providerForHost(host string) Provider {
	return hostProvider(host)
}

// normalizeHost returns a lower-case host without a trailing slash/port.
func normalizeHost(host string) string {
	if host == "" {
		return host
	}
	// url.Parse already strips port if present; just lower-case.
	return strings.ToLower(host)
}

// detectCLI runs `name auth status` to confirm authentication. It returns
// false,nil when the binary is missing or the user is not logged in.
func detectCLI(ctx context.Context, r runner, name, host string) (bool, error) {
	path, err := findExecutable(name)
	if err != nil {
		return false, nil
	}

	args := []string{"auth", "status"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	_, _, err = r.exec(ctx, path, args...)
	if err != nil {
		return false, nil
	}
	return true, nil
}
