package linear

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/debug"
)

const (
	// DefaultOAuthTokenURL is the Linear OAuth 2.0 token endpoint.
	DefaultOAuthTokenURL = "https://api.linear.app/oauth/token" //nolint:gosec // G101 false positive: URL path component, not a credential
	DefaultOAuthScopes   = "read,write"
	DefaultOAuthActor    = "application"

	// tokenExpiryBuffer is how far before actual expiry we refresh.
	tokenExpiryBuffer = 5 * time.Minute
)

// OAuthConfig holds client-credentials configuration.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     string // default: https://api.linear.app/oauth/token
	Scopes       string // default: "read,write"
	Actor        string // default: "application"
}

// OAuthTokenManager handles token acquisition and refresh for
// Linear's client_credentials OAuth flow.
type OAuthTokenManager struct {
	config    OAuthConfig
	token     string
	expiresAt time.Time
	mu        sync.RWMutex
	client    *http.Client
	nowFunc   func() time.Time // injectable for testing
}

// oauthTokenResponse is the JSON response from Linear's token endpoint.
type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

// oauthErrorResponse is the JSON error response from the token endpoint.
type oauthErrorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// NewOAuthTokenManager creates a new token manager with the given config.
// Missing fields are filled with defaults.
func NewOAuthTokenManager(config OAuthConfig) *OAuthTokenManager {
	if config.TokenURL == "" {
		config.TokenURL = DefaultOAuthTokenURL
	}
	if config.Scopes == "" {
		config.Scopes = DefaultOAuthScopes
	}
	if config.Actor == "" {
		config.Actor = DefaultOAuthActor
	}
	return &OAuthTokenManager{
		config:  config,
		client:  &http.Client{Timeout: 30 * time.Second},
		nowFunc: time.Now,
	}
}

// Token returns a valid access token, acquiring or refreshing as needed.
// Thread-safe for concurrent use.
func (m *OAuthTokenManager) Token() (string, error) {
	m.mu.RLock()
	if m.token != "" && m.nowFunc().Before(m.expiresAt.Add(-tokenExpiryBuffer)) {
		token := m.token
		m.mu.RUnlock()
		return token, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have refreshed).
	if m.token != "" && m.nowFunc().Before(m.expiresAt.Add(-tokenExpiryBuffer)) {
		return m.token, nil
	}

	if err := m.acquireToken(); err != nil {
		return "", err
	}
	return m.token, nil
}

// Invalidate forces the next Token() call to re-acquire. Use after a 401.
func (m *OAuthTokenManager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = ""
	m.expiresAt = time.Time{}
	debug.Logf("oauth: token invalidated, will re-acquire on next request")
}

// acquireToken performs the client_credentials grant. Caller must hold m.mu write lock.
func (m *OAuthTokenManager) acquireToken() error {
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {m.config.ClientID},
		"client_secret": {m.config.ClientSecret},
		"scope":         {m.config.Scopes},
		"actor":         {m.config.Actor},
	}

	req, err := http.NewRequest("POST", m.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("oauth: failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("oauth: token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return fmt.Errorf("oauth: failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp oauthErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("oauth: token request failed (%s): %s", errResp.Error, errResp.Description)
		}
		return fmt.Errorf("oauth: token request returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("oauth: failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return fmt.Errorf("oauth: token response missing access_token")
	}

	m.token = tokenResp.AccessToken
	m.expiresAt = m.nowFunc().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	debug.Logf("oauth: acquired token (expires in %ds)", tokenResp.ExpiresIn)
	return nil
}
