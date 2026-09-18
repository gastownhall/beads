package syncauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
	case ProviderNone:
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown syncauth provider %q", cfg.Provider)
	}
}

// ResolveAuto picks the best available provider for the host.
// Priority: gh for GitHub hosts, glab for GitLab hosts, then OAuth if configured.
// It returns (nil, nil) when no provider is detected; callers should run the
// git operation unmodified so git's own configured credential helpers apply.
// Auto never fails closed — an explicit provider is what makes auth required.
func ResolveAuto(ctx context.Context, host string, cfg Config, kr Keyring) (Auth, error) {
	if kr == nil {
		kr = DefaultKeyring()
	}
	host = normalizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("%w: remote host is empty", ErrNotConfigured)
	}

	hp := hostProvider(host)
	hostCfg := Config{Host: host}

	var candidates []func(context.Context) (Auth, error)

	// Unknown hosts are self-managed GitHub or GitLab — indistinguishable by
	// name — so auto tries both CLIs there, not just the GitLab guess.
	if hp != ProviderGLab {
		candidates = append(candidates, func(ctx context.Context) (Auth, error) {
			return tryDetect(ctx, newGHAuth(hostCfg))
		})
	}

	candidates = append(candidates, func(ctx context.Context) (Auth, error) {
		return tryDetect(ctx, newGLabAuth(hostCfg))
	})

	if cfg.ClientID != "" {
		oauthCfg := Config{Host: host, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Scopes: cfg.Scopes, Exe: cfg.Exe}
		candidates = append(candidates, func(ctx context.Context) (Auth, error) {
			return tryDetect(ctx, newOAuthAuth(oauthCfg, kr))
		})
	}

	for _, candidate := range candidates {
		a, err := candidate(ctx)
		if err != nil {
			return nil, err
		}
		if a != nil {
			return a, nil
		}
	}

	// No provider detected. This is not an error: the remote may be
	// public, or the user's own git credential setup (osxkeychain,
	// `gh auth setup-git`, credential-manager) may already cover it.
	return nil, nil
}

func tryDetect(ctx context.Context, a Auth) (Auth, error) {
	ok, err := a.Detect(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		return a, nil
	}
	return nil, nil
}

// GitConfigParameters builds a GIT_CONFIG_PARAMETERS value that configures git
// to use the selected auth provider as a credential helper for host. scheme is
// the remote's transport ("https" or "http"); git only consults credential
// helpers for HTTP(S) remotes, so callers should not call this for other
// transports (see CredentialScheme).
func GitConfigParameters(scheme, host string, a Auth) (string, error) {
	host = normalizeHost(host)
	if host == "" {
		return "", fmt.Errorf("remote host is empty")
	}
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("credential helpers only apply to http/https remotes, got scheme %q", scheme)
	}

	value, err := a.GitConfigParameter(host)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf("credential.%s://%s.helper", scheme, host)
	// Reset any existing helper for this host, then append our helper.
	// This mirrors `gh auth setup-git` and prevents stale cached credentials
	// from shadowing the CLI-managed token. Key and value are sq-escaped so a
	// host or path containing a single quote cannot corrupt the parameter —
	// an unescaped ' makes git reject the whole GIT_CONFIG_PARAMETERS value.
	reset := "'" + sqEscape(key) + "='"
	set := "'" + sqEscape(key) + "=" + sqEscape(value) + "'"

	params := githooksenv.AppendParameter(reset, set)

	// glab and OAuth often need credentials sent proactively.
	if a.Name() == ProviderGLab || a.Name() == ProviderOAuth {
		httpKey := fmt.Sprintf("http.%s://%s.proactiveAuth", scheme, host)
		params = githooksenv.AppendParameter(params, "'"+sqEscape(httpKey)+"=basic'")
	}

	return params, nil
}

// CredentialScheme returns the credential-helper URL scheme for a Dolt remote
// URL: "https" or "http" when the remote uses git-over-HTTP(S) transport — the
// only transports where git consults credential helpers. It returns "" for
// SSH (git+ssh://, ssh://, git@host:), git://, file, and non-git Dolt remotes
// (DoltHub, Hosted Dolt, remotesapi); those must not be wrapped.
func CredentialScheme(remoteURL string) string {
	switch {
	case strings.HasPrefix(remoteURL, "git+https://"):
		return "https"
	case strings.HasPrefix(remoteURL, "git+http://"):
		return "http"
	default:
		return ""
	}
}

// shellQuote returns s as a POSIX single-quoted string safe to embed in the
// shell command a "!" credential helper value becomes.
func shellQuote(s string) string {
	return "'" + sqEscape(s) + "'"
}

// sqEscape escapes single quotes in s for embedding inside the single quotes
// of a GIT_CONFIG_PARAMETERS entry, matching git's sq syntax.
func sqEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
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
func SetEnv(scheme, host string, a Auth) (func(), error) {
	params, err := GitConfigParameters(scheme, host, a)
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
// A nil Auth runs fn unmodified — ResolveAuto returns nil when no provider is
// detected, in which case git's own configured credential helpers still apply.
func WithAuth(ctx context.Context, scheme, host string, a Auth, fn func() error) error {
	if a == nil {
		return fn()
	}
	cleanup, err := SetEnv(scheme, host, a)
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
