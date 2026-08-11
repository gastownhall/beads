package syncauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/beads/internal/githooksenv"
)

// New creates an Auth for the requested provider. The default keyring is used.
func New(cfg Config) (Auth, error) {
	return NewWithKeyring(cfg, DefaultKeyring())
}

// NewWithKeyring creates an Auth using the supplied keyring.
func NewWithKeyring(cfg Config, kr Keyring) (Auth, error) {
	switch cfg.Provider {
	case ProviderGH:
		return newGHAuth(cfg), nil
	case ProviderGLab:
		return newGLabAuth(cfg), nil
	case ProviderOAuth:
		if kr == nil {
			kr = DefaultKeyring()
		}
		return newOAuthAuth(cfg, kr), nil
	case ProviderAuto:
		return nil, fmt.Errorf("use ResolveAuto to construct an auto provider")
	case ProviderPAT:
		return nil, fmt.Errorf("PAT provider is not supported; migrate to gh, glab, or OAuth")
	default:
		return nil, fmt.Errorf("unknown syncauth provider %q", cfg.Provider)
	}
}

// ResolveAuto picks the best available provider for the host.
// Priority: gh for GitHub hosts, glab for GitLab hosts, then OAuth if configured.
func ResolveAuto(ctx context.Context, host string, cfg Config, kr Keyring) (Auth, error) {
	if kr == nil {
		kr = DefaultKeyring()
	}
	host = normalizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("%w: remote host is empty", ErrNotConfigured)
	}

	hp := hostProvider(host)
	if hp == ProviderGH {
		gh := newGHAuth(Config{Host: host})
		ok, err := gh.Detect(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return gh, nil
		}
	}

	if hp == ProviderGLab || hp == ProviderGH {
		gl := newGLabAuth(Config{Host: host})
		ok, err := gl.Detect(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return gl, nil
		}
	}

	if cfg.ClientID != "" {
		oa := newOAuthAuth(Config{Host: host, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Scopes: cfg.Scopes, Exe: cfg.Exe}, kr)
		ok, err := oa.Detect(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return oa, nil
		}
	}

	return nil, fmt.Errorf("%w: no gh/glab login and no OAuth client_id configured for %s", ErrNoAuth, host)
}

// GitConfigParameters builds a GIT_CONFIG_PARAMETERS value that configures git
// to use the selected auth provider as a credential helper for host.
func GitConfigParameters(host string, a Auth) (string, error) {
	host = normalizeHost(host)
	if host == "" {
		return "", fmt.Errorf("remote host is empty")
	}

	value, err := a.GitConfigParameter(host)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf("credential.https://%s.helper", host)
	// Reset any existing helper for this host, then append our helper.
	// This mirrors `gh auth setup-git` and prevents stale cached credentials
	// from shadowing the CLI-managed token.
	reset := fmt.Sprintf("'%s='", key)
	set := fmt.Sprintf("'%s=%s'", key, value)

	params := githooksenv.AppendParameter(reset, set)

	// glab and OAuth often need credentials sent proactively.
	if a.Name() == ProviderGLab || a.Name() == ProviderOAuth {
		httpKey := fmt.Sprintf("http.https://%s.proactiveAuth", host)
		params = githooksenv.AppendParameter(params, fmt.Sprintf("'%s=basic'", httpKey))
	}

	return params, nil
}

// CurrentExecutable returns the absolute path to the current process binary.
// It falls back to "bd" in PATH if the executable path cannot be determined.
func CurrentExecutable() string {
	exe, err := os.Executable()
	if err == nil {
		return exe
	}
	p, err := exec.LookPath("bd")
	if err == nil {
		return p
	}
	return "bd"
}

// SetEnv applies the GIT_CONFIG_PARAMETERS needed for a and returns a cleanup
// function that restores the previous value. It is safe to nest.
func SetEnv(host string, a Auth) (func(), error) {
	params, err := GitConfigParameters(host, a)
	if err != nil {
		return nil, err
	}

	env := githooksenv.ParametersEnv
	prev, had := os.LookupEnv(env)
	var merged string
	if had {
		merged = githooksenv.AppendParameter(prev, params)
	} else {
		merged = params
	}

	if err := os.Setenv(env, merged); err != nil {
		return nil, fmt.Errorf("set %s: %w", env, err)
	}

	return func() {
		if had {
			_ = os.Setenv(env, prev)
		} else {
			_ = os.Unsetenv(env)
		}
	}, nil
}

// WithAuth runs fn with the git credential helper environment set for host.
func WithAuth(ctx context.Context, host string, a Auth, fn func() error) error {
	cleanup, err := SetEnv(host, a)
	if err != nil {
		return err
	}
	defer cleanup()
	return fn()
}

// IsGitHubHost reports whether host is a GitHub host.
func IsGitHubHost(host string) bool { return hostProvider(host) == ProviderGH }

// IsGitLabHost reports whether host is a GitLab host.
func IsGitLabHost(host string) bool { return hostProvider(host) == ProviderGLab }

// NormalizeHost lower-cases and strips a trailing slash/port from host.
func NormalizeHost(host string) string { return normalizeHost(host) }
