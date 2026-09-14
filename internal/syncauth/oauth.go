package syncauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/gitlab"
)

// storedOAuthToken is the JSON shape kept in the keyring.
//
//nolint:gosec
type storedOAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

// oauthAuth performs an OAuth 2.0 device flow and stores tokens in the OS keyring.
type oauthAuth struct {
	cfg      Config
	kr       Keyring
	endpoint oauth2.Endpoint
	scopes   []string
}

const defaultGitHubHost = "github.com"

func newOAuthAuth(cfg Config, kr Keyring) *oauthAuth {
	host := cfg.Host
	if host == "" {
		host = defaultGitHubHost
	}
	host = normalizeHost(host)

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = defaultOAuthScopes(host)
	}

	return &oauthAuth{
		cfg:      cfg,
		kr:       kr,
		endpoint: oauth2Endpoint(host),
		scopes:   scopes,
	}
}

func (o *oauthAuth) Name() Provider { return ProviderOAuth }

func (o *oauthAuth) GitConfigParameter(host string) (string, error) {
	if host == "" {
		host = o.cfg.Host
	}
	exe := o.cfg.Exe
	if exe == "" {
		exe = CurrentExecutable()
	}
	return fmt.Sprintf("!%s github-sync git-credential", exe), nil
}

func (o *oauthAuth) Detect(ctx context.Context) (bool, error) {
	if o.cfg.ClientID == "" {
		return false, nil
	}
	tok, err := o.loadToken(ctx)
	if err != nil {
		// Missing keyring entry means the user is not logged in.
		return false, nil
	}
	return tok.Valid(), nil
}

func (o *oauthAuth) Token(ctx context.Context) (Token, error) {
	tok, err := o.loadToken(ctx)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Token{}, fmt.Errorf("%w: no OAuth token stored for %s; run 'bd github-sync login --provider oauth --host %s'", ErrNoAuth, o.cfg.Host, o.cfg.Host)
		}
		return Token{}, err
	}
	if tok == nil {
		return Token{}, fmt.Errorf("%w: no OAuth token stored for %s; run 'bd github-sync login --provider oauth --host %s'", ErrNoAuth, o.cfg.Host, o.cfg.Host)
	}

	if tok.Valid() {
		return o.tokenToToken(tok), nil
	}

	if tok.RefreshToken == "" {
		return Token{}, fmt.Errorf("OAuth token for %s expired and no refresh token is available; run 'bd github-sync login --provider oauth --host %s'", o.cfg.Host, o.cfg.Host)
	}

	refreshed, err := o.refresh(ctx, tok)
	if err != nil {
		return Token{}, fmt.Errorf("refresh OAuth token for %s: %w", o.cfg.Host, err)
	}
	if err := o.storeToken(refreshed); err != nil {
		return Token{}, fmt.Errorf("store refreshed token: %w", err)
	}
	return o.tokenToToken(refreshed), nil
}

// Login runs the OAuth device flow and stores the resulting token.
func (o *oauthAuth) Login(ctx context.Context, prompt func(code, uri string) error) (Token, error) {
	if o.cfg.ClientID == "" {
		return Token{}, fmt.Errorf("%w: github.client_id / gitlab.client_id is required for OAuth", ErrNotConfigured)
	}

	cfg := o.config()
	resp, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("start device flow: %w", err)
	}

	if prompt != nil {
		uri := resp.VerificationURI
		if resp.VerificationURIComplete != "" {
			uri = resp.VerificationURIComplete
		}
		if err := prompt(resp.UserCode, uri); err != nil {
			return Token{}, err
		}
	}

	tok, err := cfg.DeviceAccessToken(ctx, resp)
	if err != nil {
		return Token{}, fmt.Errorf("complete device flow: %w", err)
	}

	if err := o.storeToken(tok); err != nil {
		return Token{}, fmt.Errorf("store token: %w", err)
	}
	return o.tokenToToken(tok), nil
}

// Logout removes the stored OAuth token from the keyring.
func (o *oauthAuth) Logout() error {
	return o.kr.Delete(keyringService, keyringUser(ProviderOAuth, o.cfg.Host))
}

func (o *oauthAuth) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		Endpoint:     o.endpoint,
		Scopes:       o.scopes,
	}
}

func (o *oauthAuth) refresh(ctx context.Context, tok *oauth2.Token) (*oauth2.Token, error) {
	src := o.config().TokenSource(ctx, tok)
	return src.Token()
}

func (o *oauthAuth) loadToken(context.Context) (*oauth2.Token, error) {
	data, err := o.kr.Get(keyringService, keyringUser(ProviderOAuth, o.cfg.Host))
	if err != nil {
		return nil, err
	}
	var s storedOAuthToken
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return nil, fmt.Errorf("parse stored OAuth token: %w", err)
	}
	return &oauth2.Token{
		AccessToken:  s.AccessToken,
		TokenType:    s.TokenType,
		RefreshToken: s.RefreshToken,
		Expiry:       s.Expiry,
	}, nil
}

func (o *oauthAuth) storeToken(tok *oauth2.Token) error {
	s := storedOAuthToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.Type(),
		Expiry:       tok.Expiry,
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return o.kr.Set(keyringService, keyringUser(ProviderOAuth, o.cfg.Host), string(data))
}

func (o *oauthAuth) tokenToToken(tok *oauth2.Token) Token {
	return Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
		Provider:     ProviderOAuth,
		Host:         o.cfg.Host,
	}
}

func oauth2Endpoint(host string) oauth2.Endpoint {
	switch host {
	case defaultGitHubHost:
		return github.Endpoint
	case "gitlab.com":
		return gitlab.Endpoint
	default:
		// Treat unknown hosts as GitLab self-managed.
		base := "https://" + host
		return oauth2.Endpoint{
			AuthURL:       base + "/oauth/authorize",
			TokenURL:      base + "/oauth/token",
			DeviceAuthURL: base + "/oauth/device_authorization",
		}
	}
}

// DefaultScopes returns sensible OAuth scopes for host.
func DefaultScopes(host string) []string {
	return defaultOAuthScopes(host)
}

func defaultOAuthScopes(host string) []string {
	if strings.EqualFold(host, defaultGitHubHost) {
		return []string{"repo"}
	}
	return []string{"read_repository", "write_repository"}
}
