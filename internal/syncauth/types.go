// Package syncauth provides secure authentication for syncing beads Dolt
// databases with GitHub/GitLab remotes. It prefers the official gh/glab CLIs
// and falls back to OAuth device flow with OS keyring storage.
package syncauth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Provider identifies the authentication backend.
type Provider string

const (
	ProviderAuto  Provider = "auto"
	ProviderGH    Provider = "gh"
	ProviderGLab  Provider = "glab"
	ProviderOAuth Provider = "oauth"
	ProviderPAT   Provider = "pat"
)

// ValidProviders is the list of providers accepted by flags.
var ValidProviders = []Provider{ProviderGH, ProviderGLab, ProviderOAuth, ProviderPAT, ProviderAuto}

//nolint:gosec
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Provider     Provider
	Host         string
}

//nolint:gosec
type Config struct {
	Provider     Provider
	Host         string
	ClientID     string
	ClientSecret string
	Scopes       []string
	PAT          string

	// Exe is the absolute path to the bd binary, used when constructing a
	// git-credential helper invocation for the OAuth provider.
	Exe string
}

// Keyring is the storage backend for secrets. The default implementation uses
// the OS keyring (go-keyring); tests inject an in-memory implementation.
type Keyring interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// Auth resolves and applies credentials for a remote host.
type Auth interface {
	// Name returns the provider slug for logging.
	Name() Provider
	// Detect reports whether this provider is available and authenticated for Host.
	Detect(ctx context.Context) (bool, error)
	// Token returns a valid access token, acquiring or refreshing as needed.
	Token(ctx context.Context) (Token, error)
	// GitConfigParameter returns a GIT_CONFIG_PARAMETERS entry that installs a
	// credential helper for host. The value can be appended to existing params.
	GitConfigParameter(host string) (string, error)
}

// IsValidProvider reports whether p is an accepted provider value.
func IsValidProvider(p Provider) bool {
	switch p {
	case ProviderGH, ProviderGLab, ProviderOAuth, ProviderPAT, ProviderAuto:
		return true
	}
	return false
}

// String returns the provider as a string.
func (p Provider) String() string { return string(p) }

// String returns a token summary that never exposes the secret.
func (t Token) String() string {
	mask := "<unset>"
	if t.AccessToken != "" {
		mask = "<redacted>"
	}
	exp := "no expiry"
	if !t.Expiry.IsZero() {
		exp = t.Expiry.Format(time.RFC3339)
	}
	return fmt.Sprintf("Token(provider=%s host=%s access=%s expiry=%s)", t.Provider, t.Host, mask, exp)
}

var (
	// ErrNoAuth is returned when no authentication source is available.
	ErrNoAuth = errors.New("no authentication source available")
	// ErrNotConfigured is returned when a provider requires configuration that is missing.
	ErrNotConfigured = errors.New("provider not configured")
)

const (
	keyringService = "beads-syncauth"
	keyringUserFmt = "%s:%s"
)

func keyringUser(provider Provider, host string) string {
	return fmt.Sprintf(keyringUserFmt, provider, host)
}
