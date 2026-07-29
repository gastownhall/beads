package linear

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthTokenAcquisition(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("expected form content-type, got %s", ct)
		}

		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.FormValue("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		if got := r.FormValue("client_id"); got != "test-client-id" {
			t.Errorf("client_id = %q, want test-client-id", got)
		}
		if got := r.FormValue("client_secret"); got != "test-secret" {
			t.Errorf("client_secret = %q, want test-secret", got)
		}
		if got := r.FormValue("scope"); got != "read,write" {
			t.Errorf("scope = %q, want read,write", got)
		}
		if got := r.FormValue("actor"); got != "application" {
			t.Errorf("actor = %q, want application", got)
		}

		resp := oauthTokenResponse{
			AccessToken: "lin_oauth_test_token_123",
			TokenType:   "Bearer",
			ExpiresIn:   2591999,
			Scope:       "read write",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		TokenURL:     server.URL,
	})

	token, err := mgr.Token()
	if err != nil {
		t.Fatalf("Token() error: %v", err)
	}
	if token != "lin_oauth_test_token_123" {
		t.Errorf("token = %q, want lin_oauth_test_token_123", token)
	}
	if requestCount != 1 {
		t.Errorf("expected 1 request, got %d", requestCount)
	}
}

func TestOAuthTokenCaching(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		resp := oauthTokenResponse{
			AccessToken: "cached_token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       "read write",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     server.URL,
	})

	token1, err := mgr.Token()
	if err != nil {
		t.Fatal(err)
	}

	token2, err := mgr.Token()
	if err != nil {
		t.Fatal(err)
	}

	if token1 != token2 {
		t.Errorf("tokens differ: %q vs %q", token1, token2)
	}
	if requestCount.Load() != 1 {
		t.Errorf("expected 1 HTTP request (cached), got %d", requestCount.Load())
	}
}

func TestOAuthTokenRefreshOnExpiry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := oauthTokenResponse{
			AccessToken: "token_v" + string(rune('0'+callCount)),
			TokenType:   "Bearer",
			ExpiresIn:   600, // 10 minutes
			Scope:       "read write",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	now := time.Now()
	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     server.URL,
	})
	mgr.nowFunc = func() time.Time { return now }

	// First call acquires token.
	_, err := mgr.Token()
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	// Advance time to within 5 minutes of expiry (600s - 5min buffer = 300s).
	// At 301s from now the token is still valid.
	now = now.Add(301 * time.Second)
	_, err = mgr.Token()
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("expected refresh at near-expiry, got %d calls", callCount)
	}
}

func TestOAuthTokenInvalidation(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := oauthTokenResponse{
			AccessToken: "token_" + string(rune('0'+callCount)),
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       "read write",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     server.URL,
	})

	_, err := mgr.Token()
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatal("unexpected call count")
	}

	mgr.Invalidate()

	_, err = mgr.Token()
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("expected re-acquisition after invalidation, got %d calls", callCount)
	}
}

func TestOAuthErrorInvalidScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(oauthErrorResponse{
			Error:       "invalid_scope",
			Description: "The requested scope is invalid",
		})
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     server.URL,
	})

	_, err := mgr.Token()
	if err == nil {
		t.Fatal("expected error for invalid_scope")
	}
	if got := err.Error(); !contains(got, "invalid_scope") {
		t.Errorf("error should mention invalid_scope: %s", got)
	}
}

func TestOAuthErrorInvalidClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(oauthErrorResponse{
			Error:       "invalid_client",
			Description: "Client authentication failed",
		})
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "bad-id",
		ClientSecret: "bad-secret",
		TokenURL:     server.URL,
	})

	_, err := mgr.Token()
	if err == nil {
		t.Fatal("expected error for invalid_client")
	}
	if got := err.Error(); !contains(got, "invalid_client") {
		t.Errorf("error should mention invalid_client: %s", got)
	}
}

func TestOAuthNetworkError(t *testing.T) {
	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     "http://127.0.0.1:1", // Connection refused
	})

	_, err := mgr.Token()
	if err == nil {
		t.Fatal("expected network error")
	}
	if got := err.Error(); !contains(got, "token request failed") {
		t.Errorf("error should mention request failure: %s", got)
	}
}

func TestOAuthThreadSafety(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(10 * time.Millisecond) // simulate latency
		resp := oauthTokenResponse{
			AccessToken: "concurrent_token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       "read write",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     server.URL,
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := mgr.Token()
			if err != nil {
				t.Errorf("concurrent Token() error: %v", err)
				return
			}
			if token != "concurrent_token" {
				t.Errorf("unexpected token: %s", token)
			}
		}()
	}
	wg.Wait()

	// Due to double-check locking, only a small number of actual requests should be made.
	if count := requestCount.Load(); count > 3 {
		t.Errorf("expected few HTTP requests with mutex, got %d", count)
	}
}

func TestOAuthBearerHeaderFormat(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oauthTokenResponse{
			AccessToken: "lin_oauth_xyz",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       "read write",
		})
	}))
	defer tokenServer.Close()

	var capturedAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{},
		})
	}))
	defer apiServer.Close()

	client := NewOAuthClient(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     tokenServer.URL,
	}, "team-id")
	client = client.WithEndpoint(apiServer.URL)

	_, err := client.Execute(t.Context(), &GraphQLRequest{Query: "{ viewer { id } }"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if capturedAuth != "Bearer lin_oauth_xyz" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "Bearer lin_oauth_xyz")
	}
}

func TestAPIKeyHeaderFormat(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{},
		})
	}))
	defer server.Close()

	client := NewClient("lin_api_mykey123", "team-id")
	client = client.WithEndpoint(server.URL)

	_, err := client.Execute(t.Context(), &GraphQLRequest{Query: "{ viewer { id } }"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if capturedAuth != "lin_api_mykey123" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "lin_api_mykey123")
	}
}

func TestOAuth401RetryWithInvalidation(t *testing.T) {
	tokenCallCount := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCallCount++
		token := "expired_token"
		if tokenCallCount > 1 {
			token = "fresh_token"
		}
		json.NewEncoder(w).Encode(oauthTokenResponse{
			AccessToken: token,
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       "read write",
		})
	}))
	defer tokenServer.Close()

	apiCallCount := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount++
		auth := r.Header.Get("Authorization")
		if auth == "Bearer expired_token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"viewer": map[string]interface{}{"id": "123"}},
		})
	}))
	defer apiServer.Close()

	client := NewOAuthClient(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     tokenServer.URL,
	}, "team-id")
	client = client.WithEndpoint(apiServer.URL)

	data, err := client.Execute(t.Context(), &GraphQLRequest{Query: "{ viewer { id } }"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if data == nil {
		t.Fatal("expected non-nil data after 401 retry")
	}

	if tokenCallCount != 2 {
		t.Errorf("expected 2 token requests (initial + retry), got %d", tokenCallCount)
	}
	if apiCallCount != 2 {
		t.Errorf("expected 2 API requests (401 + retry), got %d", apiCallCount)
	}
}

func TestOAuthDefaultConfig(t *testing.T) {
	mgr := NewOAuthTokenManager(OAuthConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	})

	if mgr.config.TokenURL != DefaultOAuthTokenURL {
		t.Errorf("TokenURL = %q, want %q", mgr.config.TokenURL, DefaultOAuthTokenURL)
	}
	if mgr.config.Scopes != DefaultOAuthScopes {
		t.Errorf("Scopes = %q, want %q", mgr.config.Scopes, DefaultOAuthScopes)
	}
	if mgr.config.Actor != DefaultOAuthActor {
		t.Errorf("Actor = %q, want %q", mgr.config.Actor, DefaultOAuthActor)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
