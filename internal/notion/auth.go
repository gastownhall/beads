package notion

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

//nolint:gosec // These are config key names, not embedded credentials.
const (
	DefaultOAuthAuthorizeURL = DefaultBaseURL + "/oauth/authorize"
	DefaultOAuthTokenURL     = DefaultBaseURL + "/oauth/token"
	DefaultOAuthOwner        = "user"
	DefaultOAuthRedirectURI  = "http://127.0.0.1:38652/callback"
	oauthExpirySkew          = time.Minute
)

const (
	configKeyToken              = "notion.token"
	configKeyOAuthAccessToken   = "notion.oauth.access_token"  // #nosec G101 -- config key name only
	configKeyOAuthRefreshToken  = "notion.oauth.refresh_token" // #nosec G101 -- config key name only
	configKeyOAuthTokenType     = "notion.oauth.token_type"    // #nosec G101 -- config key name only
	configKeyOAuthExpiresAt     = "notion.oauth.expires_at"
	configKeyOAuthWorkspaceID   = "notion.oauth.workspace_id"
	configKeyOAuthWorkspaceName = "notion.oauth.workspace_name"
	configKeyOAuthWorkspaceIcon = "notion.oauth.workspace_icon"
	configKeyOAuthBotID         = "notion.oauth.bot_id"
	configKeyOAuthOwner         = "notion.oauth.owner"
	configKeyOAuthClientID      = "notion.oauth.client_id"
	configKeyOAuthClientSecret  = "notion.oauth.client_secret" // #nosec G101 -- config key name only
	configKeyOAuthRedirectURI   = "notion.oauth.redirect_uri"
)

type AuthSource string

const (
	AuthSourceConfigToken AuthSource = "config_token"
	AuthSourceOAuth       AuthSource = "oauth"
	AuthSourceEnv         AuthSource = "env"
)

type ResolvedAuth struct {
	Token  string
	Source AuthSource
	OAuth  *StoredOAuthToken
}

//nolint:gosec // These fields model persisted OAuth data; values come from runtime auth flow.
type StoredOAuthToken struct {
	AccessToken   string
	RefreshToken  string
	TokenType     string
	ExpiresAt     time.Time
	WorkspaceID   string
	WorkspaceName string
	WorkspaceIcon string
	BotID         string
	Owner         string
}

//nolint:gosec // These fields model OAuth client configuration supplied by the user.
type OAuthClientConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Owner        string
}

type OAuthLoginResult struct {
	Auth       *ResolvedAuth
	User       *User
	AuthURL    string
	Configured OAuthClientConfig
}

//nolint:gosec // These fields mirror the Notion OAuth response schema.
type oauthTokenResponse struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type,omitempty"`
	BotID         string `json:"bot_id,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	WorkspaceIcon string `json:"workspace_icon,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresIn     int    `json:"expires_in,omitempty"`
	Owner         struct {
		Type string `json:"type,omitempty"`
	} `json:"owner,omitempty"`
}

type oauthCallbackResult struct {
	Code string
	Err  error
}

var (
	notionOAuthAuthorizeURL = DefaultOAuthAuthorizeURL
	notionOAuthTokenURL     = DefaultOAuthTokenURL
	notionOAuthHTTPClient   = func() *http.Client { return &http.Client{Timeout: DefaultTimeout} }
	notionOAuthListen       = func(network, address string) (net.Listener, error) { return net.Listen(network, address) }
	notionOAuthOpenBrowser  = openBrowser
)

func ResolveAuth(ctx context.Context, store storage.Storage) (*ResolvedAuth, error) {
	if store != nil {
		if token, err := store.GetConfig(ctx, configKeyToken); err == nil && strings.TrimSpace(token) != "" {
			return &ResolvedAuth{
				Token:  strings.TrimSpace(token),
				Source: AuthSourceConfigToken,
			}, nil
		}

		stored, err := loadOAuthToken(ctx, store)
		if err != nil {
			return nil, err
		}
		if stored != nil {
			if oauthTokenValid(stored) {
				return &ResolvedAuth{Token: stored.AccessToken, Source: AuthSourceOAuth, OAuth: stored}, nil
			}
			refreshed, err := refreshOAuthToken(ctx, store, stored)
			if err != nil {
				return nil, err
			}
			return &ResolvedAuth{Token: refreshed.AccessToken, Source: AuthSourceOAuth, OAuth: refreshed}, nil
		}
	}

	if token := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); token != "" {
		return &ResolvedAuth{Token: token, Source: AuthSourceEnv}, nil
	}
	return nil, nil
}

func LoadOAuthClientConfig(ctx context.Context, store storage.Storage) OAuthClientConfig {
	lookup := func(key, envVar, fallback string) string {
		if store != nil {
			if value, err := store.GetConfig(ctx, key); err == nil && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		if envVar != "" {
			if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
				return value
			}
		}
		return fallback
	}
	return OAuthClientConfig{
		ClientID:     lookup(configKeyOAuthClientID, "NOTION_OAUTH_CLIENT_ID", ""),
		ClientSecret: lookup(configKeyOAuthClientSecret, "NOTION_OAUTH_CLIENT_SECRET", ""),
		RedirectURI:  lookup(configKeyOAuthRedirectURI, "NOTION_OAUTH_REDIRECT_URI", DefaultOAuthRedirectURI),
		Owner:        lookup(configKeyOAuthOwner, "NOTION_OAUTH_OWNER", DefaultOAuthOwner),
	}
}

func Login(ctx context.Context, store storage.Storage) (*OAuthLoginResult, error) {
	if store == nil {
		return nil, fmt.Errorf("database not available")
	}
	clientConfig := LoadOAuthClientConfig(ctx, store)
	if strings.TrimSpace(clientConfig.ClientID) == "" || strings.TrimSpace(clientConfig.ClientSecret) == "" {
		return nil, fmt.Errorf("Notion OAuth client is not configured. Set notion.oauth.client_id and notion.oauth.client_secret, or NOTION_OAUTH_CLIENT_ID and NOTION_OAUTH_CLIENT_SECRET")
	}
	callbackURL, err := url.Parse(clientConfig.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	listener, err := notionOAuthListen("tcp", callbackURL.Host)
	if err != nil {
		return nil, fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()

	state, err := randomBase64URL(24)
	if err != nil {
		return nil, fmt.Errorf("generate OAuth state: %w", err)
	}
	authURL := buildOAuthAuthorizeURL(clientConfig, state)
	callbackCh := make(chan oauthCallbackResult, 1)
	serverErrCh := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler:           loginCallbackHandler(callbackURL.Path, state, callbackCh),
	}
	go func() {
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	if err := notionOAuthOpenBrowser(authURL); err != nil {
		return nil, fmt.Errorf("open browser for OAuth login: %w", err)
	}

	code, err := waitForOAuthAuthorizationCode(ctx, callbackCh, serverErrCh)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if err != nil {
		return nil, err
	}

	tokens, err := exchangeOAuthCode(ctx, clientConfig, code)
	if err != nil {
		return nil, err
	}
	stored := storedOAuthTokenFromResponse(tokens, clientConfig.Owner)
	if err := saveOAuthToken(ctx, store, stored); err != nil {
		return nil, err
	}
	resolved := &ResolvedAuth{
		Token:  stored.AccessToken,
		Source: AuthSourceOAuth,
		OAuth:  stored,
	}
	user, err := NewClient(stored.AccessToken).GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	return &OAuthLoginResult{
		Auth:       resolved,
		User:       user,
		AuthURL:    authURL,
		Configured: clientConfig,
	}, nil
}

func Logout(ctx context.Context, store storage.Storage) error {
	if store == nil {
		return fmt.Errorf("database not available")
	}
	deleter, ok := store.(storage.ConfigMetadataStore)
	if !ok {
		return fmt.Errorf("store does not support config deletion")
	}
	for _, key := range oauthConfigKeys() {
		if err := deleter.DeleteConfig(ctx, key); err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

func oauthConfigKeys() []string {
	return []string{
		configKeyOAuthAccessToken,
		configKeyOAuthRefreshToken,
		configKeyOAuthTokenType,
		configKeyOAuthExpiresAt,
		configKeyOAuthWorkspaceID,
		configKeyOAuthWorkspaceName,
		configKeyOAuthWorkspaceIcon,
		configKeyOAuthBotID,
		configKeyOAuthOwner,
	}
}

func buildOAuthAuthorizeURL(cfg OAuthClientConfig, state string) string {
	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	values.Set("response_type", "code")
	values.Set("owner", firstNonEmpty(strings.TrimSpace(cfg.Owner), DefaultOAuthOwner))
	values.Set("redirect_uri", cfg.RedirectURI)
	values.Set("state", state)
	return notionOAuthAuthorizeURL + "?" + values.Encode()
}

func loginCallbackHandler(callbackPath, state string, callbackCh chan<- oauthCallbackResult) http.Handler {
	if strings.TrimSpace(callbackPath) == "" {
		callbackPath = "/callback"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		if errParam := strings.TrimSpace(r.URL.Query().Get("error")); errParam != "" {
			callbackCh <- oauthCallbackResult{Err: fmt.Errorf("Notion authorization failed: %s", errParam)}
			http.Error(w, "Authorization failed. You can close this tab.", http.StatusBadRequest)
			return
		}
		if gotState := strings.TrimSpace(r.URL.Query().Get("state")); gotState != state {
			callbackCh <- oauthCallbackResult{Err: fmt.Errorf("Notion authorization returned an unexpected state")}
			http.Error(w, "Authorization state mismatch. You can close this tab.", http.StatusBadRequest)
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			callbackCh <- oauthCallbackResult{Err: fmt.Errorf("Notion authorization did not return a code")}
			http.Error(w, "Authorization code missing. You can close this tab.", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "Notion authorization complete. You can close this tab.")
		callbackCh <- oauthCallbackResult{Code: code}
	})
}

func waitForOAuthAuthorizationCode(ctx context.Context, callbackCh <-chan oauthCallbackResult, serverErrCh <-chan error) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-serverErrCh:
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("OAuth callback server stopped before authorization completed")
	case result := <-callbackCh:
		if result.Err != nil {
			return "", result.Err
		}
		if result.Code == "" {
			return "", fmt.Errorf("Notion authorization returned an empty code")
		}
		return result.Code, nil
	}
}

func exchangeOAuthCode(ctx context.Context, cfg OAuthClientConfig, code string) (*oauthTokenResponse, error) {
	return exchangeOAuthRequest(ctx, cfg, map[string]interface{}{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": cfg.RedirectURI,
	})
}

func refreshOAuthToken(ctx context.Context, store storage.Storage, stored *StoredOAuthToken) (*StoredOAuthToken, error) {
	if stored == nil {
		return nil, fmt.Errorf("stored OAuth token is nil")
	}
	if strings.TrimSpace(stored.RefreshToken) == "" {
		return nil, fmt.Errorf("stored Notion OAuth token expired and no refresh token is available")
	}
	clientConfig := LoadOAuthClientConfig(ctx, store)
	if strings.TrimSpace(clientConfig.ClientID) == "" || strings.TrimSpace(clientConfig.ClientSecret) == "" {
		return nil, fmt.Errorf("stored Notion OAuth token expired and notion.oauth.client_id/client_secret are not configured")
	}
	response, err := exchangeOAuthRequest(ctx, clientConfig, map[string]interface{}{
		"grant_type":    "refresh_token",
		"refresh_token": stored.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	refreshed := storedOAuthTokenFromResponse(response, firstNonEmpty(response.Owner.Type, stored.Owner, clientConfig.Owner))
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = stored.RefreshToken
	}
	if refreshed.WorkspaceID == "" {
		refreshed.WorkspaceID = stored.WorkspaceID
	}
	if refreshed.WorkspaceName == "" {
		refreshed.WorkspaceName = stored.WorkspaceName
	}
	if refreshed.WorkspaceIcon == "" {
		refreshed.WorkspaceIcon = stored.WorkspaceIcon
	}
	if refreshed.BotID == "" {
		refreshed.BotID = stored.BotID
	}
	if refreshed.TokenType == "" {
		refreshed.TokenType = stored.TokenType
	}
	if err := saveOAuthToken(ctx, store, refreshed); err != nil {
		return nil, err
	}
	return refreshed, nil
}

//nolint:gosec // The OAuth token endpoint is fixed to Notion defaults or test doubles.
func exchangeOAuthRequest(ctx context.Context, cfg OAuthClientConfig, requestBody map[string]interface{}) (*oauthTokenResponse, error) {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal OAuth request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, notionOAuthTokenURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("create OAuth token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.ClientID+":"+cfg.ClientSecret)))

	resp, err := notionOAuthHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request OAuth token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read OAuth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Notion OAuth token exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed oauthTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse OAuth response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return nil, fmt.Errorf("Notion OAuth response did not include an access token")
	}
	return &parsed, nil
}

func storedOAuthTokenFromResponse(response *oauthTokenResponse, owner string) *StoredOAuthToken {
	if response == nil {
		return nil
	}
	stored := &StoredOAuthToken{
		AccessToken:   strings.TrimSpace(response.AccessToken),
		RefreshToken:  strings.TrimSpace(response.RefreshToken),
		TokenType:     strings.TrimSpace(response.TokenType),
		WorkspaceID:   strings.TrimSpace(response.WorkspaceID),
		WorkspaceName: strings.TrimSpace(response.WorkspaceName),
		WorkspaceIcon: strings.TrimSpace(response.WorkspaceIcon),
		BotID:         strings.TrimSpace(response.BotID),
		Owner:         strings.TrimSpace(firstNonEmpty(response.Owner.Type, owner)),
	}
	if response.ExpiresIn > 0 {
		stored.ExpiresAt = time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return stored
}

func loadOAuthToken(ctx context.Context, store storage.Storage) (*StoredOAuthToken, error) {
	if store == nil {
		return nil, nil
	}
	get := func(key string) string {
		value, _ := store.GetConfig(ctx, key)
		return strings.TrimSpace(value)
	}
	stored := &StoredOAuthToken{
		AccessToken:   get(configKeyOAuthAccessToken),
		RefreshToken:  get(configKeyOAuthRefreshToken),
		TokenType:     get(configKeyOAuthTokenType),
		WorkspaceID:   get(configKeyOAuthWorkspaceID),
		WorkspaceName: get(configKeyOAuthWorkspaceName),
		WorkspaceIcon: get(configKeyOAuthWorkspaceIcon),
		BotID:         get(configKeyOAuthBotID),
		Owner:         get(configKeyOAuthOwner),
	}
	if expiresAt := get(configKeyOAuthExpiresAt); expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse notion OAuth expiry: %w", err)
		}
		stored.ExpiresAt = parsed
	}
	if stored.AccessToken == "" && stored.RefreshToken == "" {
		return nil, nil
	}
	return stored, nil
}

func saveOAuthToken(ctx context.Context, store storage.Storage, stored *StoredOAuthToken) error {
	if store == nil {
		return fmt.Errorf("database not available")
	}
	if stored == nil {
		return fmt.Errorf("OAuth token is nil")
	}
	values := map[string]string{
		configKeyOAuthAccessToken:   strings.TrimSpace(stored.AccessToken),
		configKeyOAuthRefreshToken:  strings.TrimSpace(stored.RefreshToken),
		configKeyOAuthTokenType:     strings.TrimSpace(stored.TokenType),
		configKeyOAuthWorkspaceID:   strings.TrimSpace(stored.WorkspaceID),
		configKeyOAuthWorkspaceName: strings.TrimSpace(stored.WorkspaceName),
		configKeyOAuthWorkspaceIcon: strings.TrimSpace(stored.WorkspaceIcon),
		configKeyOAuthBotID:         strings.TrimSpace(stored.BotID),
		configKeyOAuthOwner:         strings.TrimSpace(stored.Owner),
	}
	if !stored.ExpiresAt.IsZero() {
		values[configKeyOAuthExpiresAt] = stored.ExpiresAt.UTC().Format(time.RFC3339)
	}
	for key, value := range values {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return fmt.Errorf("save %s: %w", key, err)
		}
	}
	return nil
}

func oauthTokenValid(stored *StoredOAuthToken) bool {
	if stored == nil || strings.TrimSpace(stored.AccessToken) == "" {
		return false
	}
	if stored.ExpiresAt.IsZero() {
		return true
	}
	return stored.ExpiresAt.After(time.Now().UTC().Add(oauthExpirySkew))
}

func randomBase64URL(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
