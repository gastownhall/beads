package notion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/notion/mcpclient"
	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
	"golang.org/x/oauth2"
)

const (
	loginClientName    = "bdnotion"
	loginClientURI     = "https://github.com/osamu2001/bdnotion"
	loginUserAgent     = "bdnotion/0.1.0"
	callbackServerAddr = "127.0.0.1:0"
)

var (
	loginMCPServerURL    = mcpclient.NotionMCPURL
	loginHTTPClient      = func() *http.Client { return &http.Client{Timeout: 20 * time.Second} }
	loginListen          = func(network, address string) (net.Listener, error) { return net.Listen(network, address) }
	loginOpenBrowser     = openBrowser
	loginCallbackTimeout = 2 * time.Minute
	loginLiveAuthProbe   = verifyLiveAuth
)

type oauthProtectedResourceMetadata struct {
	AuthorizationServers []string `json:"authorization_servers"`
}

type oauthServerMetadata struct {
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

type oauthClientRegistration struct {
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
}

//nolint:gosec // OAuth field names must match the upstream wire format.
type oauthRegisteredClient struct {
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret,omitempty"`
	ClientIDIssuedAt      int64  `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt int64  `json:"client_secret_expires_at,omitempty"`
}

//nolint:gosec // OAuth field names must match the upstream wire format.
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

type oauthCallbackResult struct {
	Code string
	Err  error
}

func Login(ctx context.Context, ioo *output.IO, store *state.AuthStore) error {
	if store == nil {
		return output.NewError("Login failed", "bdnotion auth store is not configured", "Retry the command after reinitializing bdnotion", 1)
	}

	client := loginHTTPClient()
	listener, err := loginListen("tcp", callbackServerAddr)
	if err != nil {
		return output.Wrap(err, "failed to start OAuth callback listener")
	}
	defer func() { _ = listener.Close() }()

	redirectURI := fmt.Sprintf("http://%s/callback", listener.Addr().String())
	success := false
	defer func() {
		if !success {
			_ = store.DeleteAuthFilesOnly()
		}
	}()

	metadata, err := discoverOAuthMetadata(ctx, client, loginMCPServerURL)
	if err != nil {
		return output.Wrap(err, "failed to discover Notion OAuth metadata")
	}

	registeredClient, rawClient, err := registerOAuthClient(ctx, client, metadata, redirectURI)
	if err != nil {
		return output.Wrap(err, "failed to register bdnotion as an OAuth client")
	}
	if err := store.SaveClientInfo(rawClient); err != nil {
		return output.Wrap(err, "failed to persist OAuth client credentials")
	}

	codeVerifier, codeChallenge, err := generatePKCEPair()
	if err != nil {
		return output.Wrap(err, "failed to generate PKCE verifier")
	}
	callbackState, err := randomHex(32)
	if err != nil {
		return output.Wrap(err, "failed to generate OAuth state")
	}
	if err := store.SaveAuthState(map[string]any{"codeVerifier": codeVerifier}); err != nil {
		return output.Wrap(err, "failed to persist OAuth state")
	}

	authURL := buildAuthorizationURL(metadata, registeredClient.ClientID, redirectURI, codeChallenge, callbackState)

	callbackCh := make(chan oauthCallbackResult, 1)
	serverErrCh := make(chan error, 1)
	server := &http.Server{
		Handler:           loginCallbackHandler(callbackState, callbackCh),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	if err := loginOpenBrowser(authURL); err != nil {
		_ = ioo.Progress("Could not open a browser automatically. Open this URL to finish signing in: %s", authURL)
	} else {
		_ = ioo.Progress("Finish Notion authentication in your browser: %s", authURL)
	}

	code, err := waitForAuthorizationCode(ctx, serverErrCh, callbackCh)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if err != nil {
		return err
	}

	tokenResponse, err := exchangeAuthorizationCode(ctx, client, metadata, registeredClient, redirectURI, code, codeVerifier)
	if err != nil {
		return output.Wrap(err, "failed to exchange authorization code for tokens")
	}
	if err := store.SaveTokens(state.StoredTokens{
		AccessToken:  tokenResponse.AccessToken,
		TokenType:    tokenResponse.TokenType,
		RefreshToken: tokenResponse.RefreshToken,
		ExpiresIn:    tokenResponse.ExpiresIn,
		Scope:        tokenResponse.Scope,
	}); err != nil {
		return output.Wrap(err, "failed to persist OAuth tokens")
	}

	result, err := loginLiveAuthProbe(ctx, store)
	if err != nil {
		return err
	}
	payload, err := normalizeLiveAuthPayload(result)
	if err != nil {
		return output.Wrap(err, "failed to normalize live auth payload")
	}

	success = true
	return ioo.JSON(payload)
}

func discoverOAuthMetadata(ctx context.Context, client *http.Client, serverURL string) (*oauthServerMetadata, error) {
	var resource oauthProtectedResourceMetadata
	var lastErr error
	for _, candidate := range protectedResourceMetadataURLs(serverURL) {
		if err := getJSON(ctx, client, candidate, &resource); err != nil {
			lastErr = err
			continue
		}
		if len(resource.AuthorizationServers) > 0 {
			break
		}
	}
	if len(resource.AuthorizationServers) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("authorization servers were missing from protected resource metadata")
		}
		return nil, lastErr
	}

	metadataURL, err := wellKnownAuthorizationServerURL(resource.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	var metadata oauthServerMetadata
	if err := getJSON(ctx, client, metadataURL, &metadata); err != nil {
		return nil, err
	}
	if metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("authorization server metadata is missing required endpoints")
	}
	return &metadata, nil
}

func protectedResourceMetadataURLs(serverURL string) []string {
	base, err := url.Parse(serverURL)
	if err != nil {
		return nil
	}
	relative := base.ResolveReference(&url.URL{Path: strings.TrimSuffix(base.Path, "/") + "/.well-known/oauth-protected-resource"})
	root := base.ResolveReference(&url.URL{Path: "/.well-known/oauth-protected-resource"})
	urls := []string{relative.String()}
	if root.String() != relative.String() {
		urls = append(urls, root.String())
	}
	return urls
}

func wellKnownAuthorizationServerURL(server string) (string, error) {
	base, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parse authorization server URL: %w", err)
	}
	return base.ResolveReference(&url.URL{Path: "/.well-known/oauth-authorization-server"}).String(), nil
}

func registerOAuthClient(ctx context.Context, client *http.Client, metadata *oauthServerMetadata, redirectURI string) (*oauthRegisteredClient, map[string]any, error) {
	registration := oauthClientRegistration{
		ClientName:              loginClientName,
		ClientURI:               loginClientURI,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}
	if len(metadata.ScopesSupported) > 0 {
		registration.Scope = strings.Join(metadata.ScopesSupported, " ")
	}
	body, err := json.Marshal(registration)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	//nolint:gosec // OAuth endpoint comes from discovered server metadata and remains injectable for tests.
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("client registration failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var registered oauthRegisteredClient
	if err := json.Unmarshal(data, &registered); err != nil {
		return nil, nil, err
	}
	if registered.ClientID == "" {
		return nil, nil, fmt.Errorf("client registration response did not include client_id")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, err
	}
	return &registered, raw, nil
}

func buildAuthorizationURL(metadata *oauthServerMetadata, clientID, redirectURI, codeChallenge, state string) string {
	config := oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirectURI,
		Endpoint: oauth2.Endpoint{
			AuthURL: metadata.AuthorizationEndpoint,
		},
		Scopes: nil,
	}
	return config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
}

func loginCallbackHandler(expectedState string, results chan<- oauthCallbackResult) http.Handler {
	var once bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}

		params := r.URL.Query()
		result := oauthCallbackResult{}
		switch {
		case params.Get("error") != "":
			result.Err = output.NewError("Login failed", fmt.Sprintf("Notion returned an OAuth error: %s", params.Get("error_description")), "Retry \"bdnotion login\" and approve the requested access", 1)
		case params.Get("state") != expectedState:
			result.Err = output.NewError("Login failed", "OAuth callback state did not match the original request", "Retry \"bdnotion login\" to start a fresh browser flow", 1)
		case params.Get("code") == "":
			result.Err = output.NewError("Login failed", "OAuth callback did not include an authorization code", "Retry \"bdnotion login\" and complete the browser flow again", 1)
		default:
			result.Code = params.Get("code")
		}
		if !once {
			once = true
			results <- result
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if result.Err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "Notion authorization failed. You can close this window and try bdnotion login again.\n")
			return
		}
		_, _ = io.WriteString(w, "Notion authorization received. You can return to bdnotion.\n")
	})
}

func waitForAuthorizationCode(ctx context.Context, serverErrCh <-chan error, callbackCh <-chan oauthCallbackResult) (string, error) {
	timer := time.NewTimer(loginCallbackTimeout)
	defer timer.Stop()

	for {
		select {
		case result := <-callbackCh:
			if result.Err != nil {
				return "", result.Err
			}
			return result.Code, nil
		case err := <-serverErrCh:
			if err != nil {
				return "", output.Wrap(err, "oauth callback server failed")
			}
		case <-timer.C:
			return "", output.NewError("Authorization timed out", "bdnotion did not receive the Notion OAuth callback before the timeout", "Retry \"bdnotion login\" and complete the browser flow in your browser", 1)
		case <-ctx.Done():
			return "", output.Wrap(ctx.Err(), "login canceled")
		}
	}
}

func exchangeAuthorizationCode(ctx context.Context, client *http.Client, metadata *oauthServerMetadata, registeredClient *oauthRegisteredClient, redirectURI, code, codeVerifier string) (*oauthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {registeredClient.ClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {codeVerifier},
	}
	if registeredClient.ClientSecret != "" {
		values.Set("client_secret", registeredClient.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.TokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", loginUserAgent)

	//nolint:gosec // OAuth endpoint comes from discovered server metadata and remains injectable for tests.
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token exchange failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var tokens oauthTokenResponse
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("token exchange response did not include access_token")
	}
	return &tokens, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	//nolint:gosec // OAuth endpoint comes from discovered server metadata and remains injectable for tests.
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s failed: status=%d body=%s", endpoint, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func generatePKCEPair() (string, string, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge, nil
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
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
