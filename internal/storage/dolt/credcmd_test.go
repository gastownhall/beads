package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantTok string
		wantExp bool // expect a non-zero expiry
		wantErr bool
	}{
		{"bare token (EIA-shaped)", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig\n", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig", false, false},
		{"execcredential token+exp", `{"token":"abc","expirationTimestamp":"2099-01-02T15:04:05Z"}`, "abc", true, false},
		{"gasworks access_token+expires_in", `{"access_token":"xyz","expires_in":90,"token_type":"DPoP"}`, "xyz", true, false},
		{"json without token", `{"foo":"bar"}`, "", false, true},
		{"unparseable json", `{not json`, "", false, true},
		{"empty output", "   \n", "", false, true},
		{"bare with whitespace (error message)", "access denied: nope", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, exp, err := parseCredential([]byte(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got token=%q", tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != c.wantTok {
				t.Fatalf("token = %q, want %q", tok, c.wantTok)
			}
			if c.wantExp && exp.IsZero() {
				t.Fatal("expected a non-zero expiry")
			}
			if !c.wantExp && !exp.IsZero() {
				t.Fatalf("expected zero expiry, got %v", exp)
			}
		})
	}
}

// resolveCredentialToken caches by command until near expiry, then re-runs the helper.
func TestResolveCredentialTokenCachesUntilExpiry(t *testing.T) {
	// Isolate the package cache + runner for this test.
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })

	var calls int
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		calls++
		// Long-lived expiry so the cache holds across the second call.
		return []byte(fmt.Sprintf(`{"token":"tok-%d","expirationTimestamp":%q}`, calls,
			time.Now().Add(time.Hour).Format(time.RFC3339))), nil
	}

	tok1, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	tok2, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if tok1 != tok2 || calls != 1 {
		t.Fatalf("expected one cached helper call, got calls=%d tok1=%q tok2=%q", calls, tok1, tok2)
	}

	// A different command is a different cache key → a fresh run.
	if _, err := resolveCredentialToken(context.Background(), "helper --y"); err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a fresh run for a new command, got calls=%d", calls)
	}
}

func TestResolveCredentialTokenPropagatesHelperError(t *testing.T) {
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	if _, err := resolveCredentialToken(context.Background(), "broken-helper"); err == nil {
		t.Fatal("expected an error when the helper fails")
	}
}

// TestResolveCredentialTokenHonorsCallerContext is the follow-up-A teeth test: the
// mint must abort when the caller's (dial) context deadline fires, instead of
// blocking for the full credCommandTimeout. Reverting resolveCredentialToken to
// context.Background() makes the blocking helper wait the full 30s and this fails.
func TestResolveCredentialTokenHonorsCallerContext(t *testing.T) {
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
	// Models a slow/hung helper: blocks until its (derived) context is cancelled.
	credRunner = func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := resolveCredentialToken(ctx, "slow-helper")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error when the caller context deadline fires")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("resolve took %v — caller context was not honored (would block up to credCommandTimeout=%v)", elapsed, credCommandTimeout)
	}
}

// TestInvalidateCredentialToken is the follow-up-B teeth test: after invalidation a
// still-unexpired cached token is dropped so the next resolve re-mints. If
// invalidateCredentialToken failed to delete, the third resolve would be a cache
// hit and calls would stay at 1.
func TestInvalidateCredentialToken(t *testing.T) {
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
	var calls int
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		calls++
		return []byte(`{"access_token":"tok","expires_in":3600}`), nil
	}

	const cmd = "rotating-helper"
	if _, err := resolveCredentialToken(context.Background(), cmd); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := resolveCredentialToken(context.Background(), cmd); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 helper call before invalidation (long expiry cached), got %d", calls)
	}

	invalidateCredentialToken(cmd)

	if _, err := resolveCredentialToken(context.Background(), cmd); err != nil {
		t.Fatalf("post-invalidation resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a re-mint after invalidation, got calls=%d", calls)
	}
}

// TestIsAuthError classifies the MySQL access-denied (1045) signal that drives
// credential-cache invalidation, and rejects non-auth errors.
func TestIsAuthError(t *testing.T) {
	if !isAuthError(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"}) {
		t.Fatal("MySQL 1045 must be an auth error")
	}
	if isAuthError(&mysql.MySQLError{Number: 1213, Message: "deadlock found"}) {
		t.Fatal("MySQL 1213 (deadlock) is not an auth error")
	}
	if !isAuthError(fmt.Errorf("Error 1045: Access denied for user 'x'@'y'")) {
		t.Fatal("an access-denied string must match")
	}
	if isAuthError(fmt.Errorf("dial tcp: connection refused")) {
		t.Fatal("connection refused is not an auth error")
	}
	if isAuthError(nil) {
		t.Fatal("nil is not an auth error")
	}
}
