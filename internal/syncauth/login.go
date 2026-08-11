package syncauth

import "context"

// NewWithKeyringOrDefault returns an Auth using kr, falling back to the OS
// keyring when kr is nil.
func NewWithKeyringOrDefault(cfg Config, kr Keyring) (Auth, error) {
	if kr == nil {
		kr = DefaultKeyring()
	}
	return NewWithKeyring(cfg, kr)
}

// OAuthLogin runs the OAuth device flow for cfg.Host and stores the token.
func OAuthLogin(ctx context.Context, cfg Config, prompt func(code, uri string) error) (Token, error) {
	oa := newOAuthAuth(cfg, DefaultKeyring())
	return oa.Login(ctx, prompt)
}

// OAuthLogout removes the stored OAuth token for host.
func OAuthLogout(_ context.Context, host string, kr Keyring) error {
	if kr == nil {
		kr = DefaultKeyring()
	}
	oa := newOAuthAuth(Config{Host: host}, kr)
	return oa.Logout()
}
