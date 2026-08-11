package syncauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

func TestHostFromRemoteURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://github.com/steveyegge/beads.git", "github.com", false},
		{"https://github.com/steveyegge/beads", "github.com", false},
		{"ssh://git@github.com/steveyegge/beads.git", "github.com", false},
		{"git@github.com:steveyegge/beads.git", "github.com", false},
		{"https://gitlab.com/group/project.git", "gitlab.com", false},
		{"git@gitlab.com:group/project.git", "gitlab.com", false},
		{"", "", true},
		{"not-a-url", "not-a-url", false},
		{"://bad", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := HostFromRemoteURL(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("HostFromRemoteURL(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("HostFromRemoteURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTokenStringRedacts(t *testing.T) {
	tok := Token{
		AccessToken:  "super-secret",
		RefreshToken: "refresh-secret",
		Provider:     ProviderGH,
		Host:         "github.com",
	}
	s := tok.String()
	if strings.Contains(s, "super-secret") || strings.Contains(s, "refresh-secret") {
		t.Fatalf("Token.String() leaked secret: %s", s)
	}
	if !strings.Contains(s, "<redacted>") {
		t.Fatalf("Token.String() did not redact access token: %s", s)
	}
	if !strings.Contains(s, "provider=gh") {
		t.Fatalf("Token.String() did not include provider: %s", s)
	}
}

func TestMemoryKeyring(t *testing.T) {
	kr := NewMemoryKeyring()

	if _, err := kr.Get("svc", "user"); !isNotFound(err) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := kr.Set("svc", "user", "secret"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := kr.Get("svc", "user")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != "secret" {
		t.Fatalf("Get returned %q, want %q", got, "secret")
	}

	if err := kr.Delete("svc", "user"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := kr.Get("svc", "user"); !isNotFound(err) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func isNotFound(err error) bool {
	return err != nil && (err == keyring.ErrNotFound || strings.Contains(err.Error(), "not found"))
}

func TestGHAuthToken(t *testing.T) {
	g := newGHAuth(Config{Host: "github.com"})
	g.runner.run = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if filepath.Base(name) == "gh" && len(args) == 2 && args[0] == "auth" && args[1] == "token" {
			return []byte("ghp_token123\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}

	tok, err := g.Token(context.Background())
	if err != nil {
		t.Fatalf("Token failed: %v", err)
	}
	if tok.AccessToken != "ghp_token123" {
		t.Fatalf("AccessToken = %q, want %q", tok.AccessToken, "ghp_token123")
	}
	if tok.Provider != ProviderGH {
		t.Fatalf("Provider = %q, want %q", tok.Provider, ProviderGH)
	}
}

func TestGLabAuthToken(t *testing.T) {
	tmp := t.TempDir()
	glabPath := filepath.Join(tmp, "glab")
	if err := os.WriteFile(glabPath, []byte("#!/bin/sh\necho 'mock glab'\n"), 0755); err != nil {
		t.Fatalf("write fake glab: %v", err)
	}
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))

	g := newGLabAuth(Config{Host: "gitlab.com"})
	g.runner.run = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if filepath.Base(name) == "glab" && len(args) == 3 && args[0] == "auth" && args[1] == "status" && args[2] == "--show-token" {
			return []byte("token: glpat-abc-123\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}

	tok, err := g.Token(context.Background())
	if err != nil {
		t.Fatalf("Token failed: %v", err)
	}
	if tok.AccessToken != "glpat-abc-123" {
		t.Fatalf("AccessToken = %q, want %q", tok.AccessToken, "glpat-abc-123")
	}
	if tok.Provider != ProviderGLab {
		t.Fatalf("Provider = %q, want %q", tok.Provider, ProviderGLab)
	}
}

func TestResolveAutoPrefersCLI(t *testing.T) {
	ctx := context.Background()
	kr := NewMemoryKeyring()

	old := run
	defer func() { run = old }()

	run = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if filepath.Base(name) == "gh" && len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("not authenticated")
	}

	a, err := ResolveAuto(ctx, "github.com", Config{Host: "github.com"}, kr)
	if err != nil {
		t.Fatalf("ResolveAuto failed: %v", err)
	}
	if a.Name() != ProviderGH {
		t.Fatalf("ResolveAuto chose %q, want %q", a.Name(), ProviderGH)
	}
}

func TestResolveAutoFallsBackToOAuth(t *testing.T) {
	ctx := context.Background()
	kr := NewMemoryKeyring()

	old := run
	defer func() { run = old }()

	run = func(_ context.Context, name string, _ ...string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("not installed")
	}

	if err := kr.Set(keyringService, keyringUser(ProviderOAuth, "github.com"), `{"access_token":"abc","token_type":"bearer","expiry":"2099-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	a, err := ResolveAuto(ctx, "github.com", Config{Host: "github.com", ClientID: "client"}, kr)
	if err != nil {
		t.Fatalf("ResolveAuto failed: %v", err)
	}
	if a.Name() != ProviderOAuth {
		t.Fatalf("ResolveAuto chose %q, want %q", a.Name(), ProviderOAuth)
	}
}

func TestWithAuthSetsAndRestoresEnv(t *testing.T) {
	const env = "GIT_CONFIG_PARAMETERS"
	prev := os.Getenv(env)
	defer func() { _ = os.Setenv(env, prev) }()

	_ = os.Unsetenv(env)

	a, err := New(Config{Provider: ProviderGH, Host: "github.com"})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	err = WithAuth(context.Background(), "github.com", a, func() error {
		got := os.Getenv(env)
		if got == "" {
			return fmt.Errorf("GIT_CONFIG_PARAMETERS not set")
		}
		if !strings.Contains(got, "credential.https://github.com.helper") {
			return fmt.Errorf("GIT_CONFIG_PARAMETERS missing credential helper: %s", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithAuth failed: %v", err)
	}

	if os.Getenv(env) != "" {
		t.Fatalf("GIT_CONFIG_PARAMETERS not restored")
	}
}

func TestGitCredentialOperation(t *testing.T) {
	kr := NewMemoryKeyring()
	if err := kr.Set(keyringService, keyringUser(ProviderOAuth, "github.com"), `{"access_token":"tok","token_type":"bearer","expiry":"2099-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	input := strings.NewReader("protocol=https\nhost=github.com\n\n")
	var out strings.Builder
	if err := GitCredentialOperation(context.Background(), "get", input, &out, kr); err != nil {
		t.Fatalf("GitCredentialOperation failed: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "username=oauth2") {
		t.Fatalf("missing username=oauth2 in output: %s", got)
	}
	if !strings.Contains(got, "password=tok") {
		t.Fatalf("missing password in output: %s", got)
	}
}

func TestOAuthLoginStoresToken(t *testing.T) {
	deviceCode := "device-123"
	userCode := "user-456"

	var tokenReqCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               deviceCode,
				"user_code":                 userCode,
				"verification_uri":          "https://example.com/verify",
				"verification_uri_complete": "https://example.com/verify?code=" + userCode,
				"expires_in":                600,
				"interval":                  1,
			})
		case "/token":
			tokenReqCount++
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-123",
				"token_type":    "bearer",
				"expires_in":    3600,
				"refresh_token": "refresh-456",
			})
		default:
			t.Fatalf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	kr := NewMemoryKeyring()
	oa := &oauthAuth{
		cfg:    Config{Host: u.Host, ClientID: "client"},
		kr:     kr,
		scopes: []string{"repo"},
		endpoint: oauth2.Endpoint{
			AuthURL:       ts.URL + "/auth",
			TokenURL:      ts.URL + "/token",
			DeviceAuthURL: ts.URL + "/device",
		},
	}

	var captured struct {
		code string
		uri  string
	}
	tok, err := oa.Login(context.Background(), func(code, uri string) error {
		captured.code = code
		captured.uri = uri
		return nil
	})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if captured.code != userCode {
		t.Fatalf("prompt code = %q, want %q", captured.code, userCode)
	}
	if tok.AccessToken != "access-123" {
		t.Fatalf("AccessToken = %q, want %q", tok.AccessToken, "access-123")
	}
	if tokenReqCount < 1 {
		t.Fatalf("token endpoint was not called")
	}

	stored, err := kr.Get(keyringService, keyringUser(ProviderOAuth, u.Host))
	if err != nil {
		t.Fatalf("token not stored in keyring: %v", err)
	}
	if !strings.Contains(stored, "access-123") {
		t.Fatalf("stored token missing access token: %s", stored)
	}

	// A subsequent Token call should load from the keyring.
	tok2, err := oa.Token(context.Background())
	if err != nil {
		t.Fatalf("Token failed: %v", err)
	}
	if tok2.AccessToken != "access-123" {
		t.Fatalf("Token returned %q, want %q", tok2.AccessToken, "access-123")
	}
}

func TestOAuthLogout(t *testing.T) {
	kr := NewMemoryKeyring()
	if err := kr.Set(keyringService, keyringUser(ProviderOAuth, "github.com"), `{"access_token":"x"}`); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	oa := newOAuthAuth(Config{Host: "github.com"}, kr)
	if err := oa.Logout(); err != nil {
		t.Fatalf("Logout failed: %v", err)
	}

	if _, err := kr.Get(keyringService, keyringUser(ProviderOAuth, "github.com")); !isNotFound(err) {
		t.Fatalf("expected token to be deleted, got %v", err)
	}
}

func TestGitConfigParameters(t *testing.T) {
	a, err := New(Config{Provider: ProviderGH, Host: "github.com"})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	params, err := GitConfigParameters("github.com", a)
	if err != nil {
		t.Fatalf("GitConfigParameters failed: %v", err)
	}
	if !strings.Contains(params, "credential.https://github.com.helper") {
		t.Fatalf("params missing credential helper: %s", params)
	}
	if !strings.Contains(params, "gh auth git-credential") {
		t.Fatalf("params missing gh credential helper command: %s", params)
	}
}
