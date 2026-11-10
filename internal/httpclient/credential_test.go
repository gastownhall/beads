// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/credential_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// clearCredentialEnvironment puts every rung of the ladder into its
// not-configured state, so a test names only the rung it is about and an
// operator's own exported token cannot make a test pass.
func clearCredentialEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{TokenEnv, TokenCommandEnv, "BEADS_CREDENTIALS_FILE"} {
		t.Setenv(key, "")
	}
	// An unset variable and an empty one are the same to every rung, but
	// os.Getenv on an inherited empty value is not the same as an absent file:
	// point the credentials file at a path that cannot exist.
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
}

func writeCredentialsFile(t *testing.T, host string, port int, token string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	body := fmt.Sprintf("[%s:%d]\npassword=%s\n", host, port, token)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", path)
	return path
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// authorizeWith runs one Authorize and returns the header it set.
func authorizeWith(t *testing.T, p *BearerProvider) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v0/beads/context", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := p.Authorize(context.Background(), req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return req.Header.Get("Authorization")
}

// TestNoCredentialConfiguredIsNotAnError pins the loopback-trust posture: the
// tip OSS server has no authentication at all, so an empty ladder must produce a
// plain request rather than a refusal.
func TestNoCredentialConfiguredIsNotAnError(t *testing.T) {
	clearCredentialEnvironment(t)
	p := NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080"))
	if got := authorizeWith(t, p); got != "" {
		t.Errorf("Authorization = %q, want no header", got)
	}
}

func TestCredentialLadderPrecedence(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")

	t.Run("env is the top rung", func(t *testing.T) {
		clearCredentialEnvironment(t)
		writeCredentialsFile(t, "127.0.0.1", 8080, "from-file", 0o600)
		t.Setenv(TokenEnv, "127.0.0.1=from-env")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "Bearer from-env" {
			t.Errorf("Authorization = %q, want the env rung", got)
		}
	})

	t.Run("the credentials file is the bottom rung", func(t *testing.T) {
		clearCredentialEnvironment(t)
		writeCredentialsFile(t, "127.0.0.1", 8080, "from-file", 0o600)
		if got := authorizeWith(t, NewBearerProvider(base)); got != "Bearer from-file" {
			t.Errorf("Authorization = %q, want the credentials-file rung", got)
		}
	})

	t.Run("a section for another endpoint is not consulted", func(t *testing.T) {
		clearCredentialEnvironment(t)
		writeCredentialsFile(t, "10.0.0.9", 8080, "someone-elses", 0o600)
		if got := authorizeWith(t, NewBearerProvider(base)); got != "" {
			t.Errorf("Authorization = %q, want no header: the file has no entry for this server", got)
		}
	})

	t.Run("an https URL without a port keys on 443", func(t *testing.T) {
		clearCredentialEnvironment(t)
		writeCredentialsFile(t, "beads.example.com", 443, "tls-token", 0o600)
		p := NewBearerProvider(mustParseURL(t, "https://beads.example.com/"))
		if got := authorizeWith(t, p); got != "Bearer tls-token" {
			t.Errorf("Authorization = %q, want the default-port section", got)
		}
	})
}

// TestCredentialLadderFailsClosed is the rule the postgres ladder wrote down: a
// rung the operator configured that then errors aborts the request. Silently
// dropping to a lower rung is how a misconfigured helper becomes an
// unauthenticated request nobody noticed.
func TestCredentialLadderFailsClosed(t *testing.T) {
	clearCredentialEnvironment(t)
	writeCredentialsFile(t, "127.0.0.1", 8080, "lower-rung", 0o600)
	t.Setenv(TokenCommandEnv, "127.0.0.1=exit 7")

	p := NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080"))
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v0/beads/context", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	err = p.Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("Authorize = nil; a failing token command must abort the request")
	}
	if !strings.Contains(err.Error(), TokenCommandEnv) {
		t.Errorf("error %q does not name the rung that failed", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("a failed ladder still set an Authorization header")
	}
}

// TestTokenEnvRungsAreHostScoped keeps an operator's token on the server it was
// issued for. The server a provider authorizes against comes from the
// workspace's http_target.json, which a cloned repo can supply, so a token
// scoped to one server must never reach another, and a token command scoped
// elsewhere must not even run.
func TestTokenEnvRungsAreHostScoped(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")

	t.Run("a token scoped to another host is not sent", func(t *testing.T) {
		clearCredentialEnvironment(t)
		t.Setenv(TokenEnv, "serve.example.com=not-for-you")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "" {
			t.Errorf("Authorization = %q, want no header: the token names another server", got)
		}
	})

	t.Run("a token scoped to another host falls through to the lower rungs", func(t *testing.T) {
		clearCredentialEnvironment(t)
		writeCredentialsFile(t, "127.0.0.1", 8080, "from-file", 0o600)
		t.Setenv(TokenEnv, "serve.example.com=not-for-you")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "Bearer from-file" {
			t.Errorf("Authorization = %q, want the credentials-file rung", got)
		}
	})

	t.Run("a token command scoped to another host is not run", func(t *testing.T) {
		clearCredentialEnvironment(t)
		// Run, `exit 7` would fail the ladder closed and Authorize with it.
		t.Setenv(TokenCommandEnv, "serve.example.com=exit 7")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "" {
			t.Errorf("Authorization = %q, want no header", got)
		}
	})

	t.Run("a pattern with a port names that port alone", func(t *testing.T) {
		clearCredentialEnvironment(t)
		t.Setenv(TokenEnv, "127.0.0.1:9999=wrong-port")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "" {
			t.Errorf("Authorization = %q, want no header: the pattern names another port", got)
		}
		t.Setenv(TokenEnv, "127.0.0.1:8080=right-port")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "Bearer right-port" {
			t.Errorf("Authorization = %q, want the port-scoped token", got)
		}
	})

	t.Run("host case and an https default port normalize", func(t *testing.T) {
		clearCredentialEnvironment(t)
		t.Setenv(TokenEnv, "Beads.Example.com:443=tls-token")
		if got := authorizeWith(t, NewBearerProvider(mustParseURL(t, "https://beads.example.com/"))); got != "Bearer tls-token" {
			t.Errorf("Authorization = %q, want the token", got)
		}
	})

	t.Run("the split is on the first =", func(t *testing.T) {
		clearCredentialEnvironment(t)
		t.Setenv(TokenEnv, "127.0.0.1=padded==")
		if got := authorizeWith(t, NewBearerProvider(base)); got != "Bearer padded==" {
			t.Errorf("Authorization = %q, want the token with its padding", got)
		}
	})
}

// TestMalformedTokenEnvIsRefusedWithoutEchoingIt: an unscoped value, the form
// these rungs took before they were scoped, is refused rather than applied to
// whatever server the workspace names, and so is any other value that does not
// parse. The refusal names the variable but no part of its value, since the
// value is the credential.
func TestMalformedTokenEnvIsRefusedWithoutEchoingIt(t *testing.T) {
	for _, tc := range []struct{ name, env, value string }{
		{"a bare token", TokenEnv, "bare-s3cr3t"},
		{"a bare token whose padding splits it", TokenEnv, "s3cr3t+b64/x=="},
		{"a bare token command", TokenCommandEnv, "printf s3cr3t"},
		{"nothing after the =", TokenEnv, "127.0.0.1="},
		{"a port-less colon", TokenEnv, "127.0.0.1:=s3cr3t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearCredentialEnvironment(t)
			writeCredentialsFile(t, "127.0.0.1", 8080, "lower-rung", 0o600)
			t.Setenv(tc.env, tc.value)

			req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v0/beads/context", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			err = NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080")).Authorize(context.Background(), req)
			if err == nil {
				t.Fatal("Authorize = nil; a malformed token variable must abort the request")
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error %q does not name the variable", err)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Fatalf("the refusal echoed the value: %v", err)
			}
			if req.Header.Get("Authorization") != "" {
				t.Error("a refused ladder still set an Authorization header")
			}
		})
	}
}

// TestRefreshRereadsTheCredentialSource is the client half of the server's
// rotation contract. The server re-reads its token file on a ~1s gate, so a
// rolled client 401s once and succeeds on the retry — but only if Refresh
// actually re-reads rather than replaying what it cached.
func TestRefreshRereadsTheCredentialSource(t *testing.T) {
	clearCredentialEnvironment(t)
	path := writeCredentialsFile(t, "127.0.0.1", 8080, "old-token", 0o600)
	p := NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080"))

	if got := authorizeWith(t, p); got != "Bearer old-token" {
		t.Fatalf("Authorization = %q, want the pre-rotation token", got)
	}

	t.Run("an unchanged credential is not retried", func(t *testing.T) {
		retry, err := p.Refresh(context.Background())
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if retry {
			t.Error("Refresh asked for a retry with the same token the server just refused")
		}
	})

	t.Run("a rotated credential is retried with the new value", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("[127.0.0.1:8080]\npassword=new-token\n"), 0o600); err != nil {
			t.Fatalf("rotate credentials file: %v", err)
		}
		retry, err := p.Refresh(context.Background())
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if !retry {
			t.Fatal("Refresh did not ask for a retry after the credential rolled")
		}
		if got := authorizeWith(t, p); got != "Bearer new-token" {
			t.Errorf("Authorization = %q, want the rotated token", got)
		}
	})

	t.Run("a credential that went away is not retried", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("[127.0.0.1:8080]\n"), 0o600); err != nil {
			t.Fatalf("empty the credentials file: %v", err)
		}
		retry, err := p.Refresh(context.Background())
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if retry {
			t.Error("Refresh asked to retry unauthenticated against a server that answered 401")
		}
	})
}

// TestPlainHTTPBearerWarnsOnceAndLeaksNothing: bd serve has no TLS of its own,
// so a bearer bound past loopback crosses the network in the clear. The warning
// is once per provider — a paged read would otherwise print it per request — and
// it names the server, never the token.
func TestPlainHTTPBearerWarnsOnceAndLeaksNothing(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "beads.example.com=s3cr3t-token")

	var sink strings.Builder
	p := NewBearerProvider(mustParseURL(t, "http://beads.example.com:8080"))
	p.warnTo = &sink

	for range 3 {
		if got := authorizeWith(t, p); got != "Bearer s3cr3t-token" {
			t.Fatalf("Authorization = %q", got)
		}
	}

	warning := sink.String()
	if strings.Count(warning, "Warning:") != 1 {
		t.Errorf("warning fired %d times, want once:\n%s", strings.Count(warning, "Warning:"), warning)
	}
	if !strings.Contains(warning, "beads.example.com:8080") {
		t.Errorf("warning does not name the server:\n%s", warning)
	}
	if strings.Contains(warning, "s3cr3t-token") {
		t.Fatal("the insecure-transport warning printed the token")
	}
}

// TestPlainHTTPWarningDropsUserinfo: the warning names the server, and a url
// handed in with userinfo must not bring it along — the username included,
// which url.URL.Redacted would keep and which a token sometimes rides as.
func TestPlainHTTPWarningDropsUserinfo(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "beads.example.com=bearer-token")

	var sink strings.Builder
	p := NewBearerProvider(mustParseURL(t, "http://tok3n:pa55@beads.example.com:8080"))
	p.warnTo = &sink
	authorizeWith(t, p)

	warning := sink.String()
	if !strings.Contains(warning, "http://beads.example.com:8080") {
		t.Errorf("warning does not name the server:\n%s", warning)
	}
	if strings.Contains(warning, "tok3n") || strings.Contains(warning, "pa55") {
		t.Fatalf("the warning printed the url's userinfo:\n%s", warning)
	}
}

func TestLoopbackBearerDoesNotWarn(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "127.0.0.1=local-token")

	var sink strings.Builder
	p := NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080"))
	p.warnTo = &sink
	authorizeWith(t, p)

	if sink.String() != "" {
		t.Errorf("loopback warned about plain http:\n%s", sink.String())
	}
}

// TestCredentialsFilePostureIsChecked proves the file rung goes through the
// shared credentials reader rather than reading the file itself: the 0600
// posture warning is that reader's, and a rung that bypassed it would silently
// accept a world-readable token file.
func TestCredentialsFilePostureIsChecked(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits do not gate root")
	}
	clearCredentialEnvironment(t)
	writeCredentialsFile(t, "127.0.0.1", 8080, "loose-token", 0o644)

	stderr := captureStderr(t, func() {
		if got := authorizeWith(t, NewBearerProvider(mustParseURL(t, "http://127.0.0.1:8080"))); got != "Bearer loose-token" {
			t.Errorf("Authorization = %q", got)
		}
	})
	if !strings.Contains(stderr, "overly permissive") {
		t.Errorf("no posture warning for a 0644 credentials file:\n%s", stderr)
	}
	if strings.Contains(stderr, "loose-token") {
		t.Fatal("the posture warning printed the token")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(r)
		done <- string(body)
	}()

	fn()

	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// contextServer answers the handshake and records what it was asked with.
type contextServer struct {
	body    apigen.ContextResponse
	require string // when set, a request without this bearer gets a 401
	// onUnauthorized runs just after a 401 is written. It is how the rotation
	// test lands the rewrite in the one place it happens in production: between
	// the refused attempt and the client's re-read.
	onUnauthorized func()
	// seenMu guards seen: a burst test drives this handler from several
	// server goroutines at once. Readers look at seen only after their
	// requests have returned.
	seenMu sync.Mutex
	seen   []string
}

func (s *contextServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.seenMu.Lock()
		s.seen = append(s.seen, r.Header.Get("Authorization"))
		s.seenMu.Unlock()
		if s.require != "" && r.Header.Get("Authorization") != "Bearer "+s.require {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"about:blank","title":"unauthorized","status":401,"code":"invalid_argument"}`)
			if s.onUnauthorized != nil {
				s.onUnauthorized()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func v0Context(projectID string) apigen.ContextResponse {
	return apigen.ContextResponse{
		ApiVersion:   "v0",
		BdVersion:    "1.1.0",
		Backend:      "dolt",
		Database:     "beads",
		RepoRoot:     ptr("/srv/repo"),
		ProjectId:    projectID,
		Capabilities: []string{"issues.list", "ready.list"},
	}
}
