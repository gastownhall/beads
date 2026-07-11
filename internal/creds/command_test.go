package creds

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantTok  string
		wantUser string
		wantExp  bool // expect a non-zero expiry
		wantErr  bool
	}{
		{"bare token (JWT-shaped)", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig\n", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig", "", false, false},
		{"execcredential token+exp", `{"token":"abc","expirationTimestamp":"2099-01-02T15:04:05Z"}`, "abc", "", true, false},
		{"OAuth access_token+expires_in", `{"access_token":"xyz","expires_in":90,"token_type":"Bearer"}`, "xyz", "", true, false},
		{"vault dynamic user+pass pair", `{"username":"v-app-beads-xyz","password":"s3cr3t","expires_in":3600}`, "", "", true, true}, // token/access_token absent -> error
		{"pair via access_token+username", `{"access_token":"pw","username":"dynuser","expires_in":900}`, "pw", "dynuser", true, false},
		{"json without token", `{"foo":"bar"}`, "", "", false, true},
		{"unparseable json", `{not json`, "", "", false, true},
		{"empty output", "   \n", "", "", false, true},
		{"bare with whitespace (error message)", "access denied: nope", "", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, user, exp, err := parseCredential([]byte(c.in))
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
			if user != c.wantUser {
				t.Fatalf("username = %q, want %q", user, c.wantUser)
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

// resetCache isolates the process-level command cache and runner for a test.
func resetCache(t *testing.T) {
	t.Helper()
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
}

// resolveCredentialToken caches by command until near expiry, then re-runs the helper.
func TestResolveCredentialTokenCachesUntilExpiry(t *testing.T) {
	resetCache(t)

	var calls int
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		calls++
		// Long-lived expiry so the cache holds across the second call.
		return []byte(fmt.Sprintf(`{"token":"tok-%d","expirationTimestamp":%q}`, calls,
			time.Now().Add(time.Hour).Format(time.RFC3339))), nil
	}

	tok1, _, _, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	tok2, _, _, err := resolveCredentialToken(context.Background(), "helper --x")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if tok1 != tok2 || calls != 1 {
		t.Fatalf("expected one cached helper call, got calls=%d tok1=%q tok2=%q", calls, tok1, tok2)
	}

	// A different command is a different cache key -> a fresh run.
	if _, _, _, err := resolveCredentialToken(context.Background(), "helper --y"); err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a fresh run for a new command, got calls=%d", calls)
	}
}

func TestResolveCredentialTokenPropagatesHelperError(t *testing.T) {
	resetCache(t)
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	if _, _, _, err := resolveCredentialToken(context.Background(), "broken-helper"); err == nil {
		t.Fatal("expected an error when the helper fails")
	}
}

// A real shell command flows end-to-end through CommandSource (no stub), proving the
// sh -c runner and the bare-token path work together.
func TestCommandSourceRealShell(t *testing.T) {
	resetCache(t)
	src := CommandSource{Command: "printf s3cr3t", Kind: KindSecret, Label: "TEST_CMD"}
	cred, ok, err := src.Resolve(context.Background())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if cred.Value != "s3cr3t" {
		t.Fatalf("value = %q, want s3cr3t", cred.Value)
	}
	if cred.Kind != KindSecret {
		t.Fatalf("kind = %v, want KindSecret", cred.Kind)
	}
	if cred.Source != "TEST_CMD" {
		t.Fatalf("source = %q, want TEST_CMD", cred.Source)
	}
}
