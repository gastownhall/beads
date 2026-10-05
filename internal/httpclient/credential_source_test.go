// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/credential_source_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// resolveProvider drives the token resolution the way an outbound request does,
// so Source() reflects the rung Authorize actually walked.
func resolveProvider(t *testing.T, p *BearerProvider) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Authorize(context.Background(), req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
}

func TestSourceNamesTheEnvRung(t *testing.T) {
	t.Setenv(TokenEnv, "tok-env")
	p := NewBearerProvider(mustURL(t, "http://127.0.0.1:8080"))
	resolveProvider(t, p)
	if got := p.Source(); got != TokenEnv {
		t.Errorf("Source() = %q, want the env var name %q", got, TokenEnv)
	}
}

func TestSourceNamesTheTokenCommandRung(t *testing.T) {
	// The env rung must be empty so the ladder falls to the command rung.
	t.Setenv(TokenEnv, "")
	t.Setenv(TokenCommandEnv, "printf tok-cmd")
	p := NewBearerProvider(mustURL(t, "http://127.0.0.1:8080"))
	resolveProvider(t, p)
	if got := p.Source(); got != TokenCommandEnv {
		t.Errorf("Source() = %q, want the command env var name %q", got, TokenCommandEnv)
	}
}

func TestSourceRendersTheCredentialsFileRungWithHostPort(t *testing.T) {
	// The file rung's slug is host-agnostic, so Source renders it with the
	// [host:port] section key an operator would grep for. The slug is captured on
	// resolve (proven by the env rung); this pins the rendering the operator sees.
	p := NewBearerProvider(mustURL(t, "http://127.0.0.1:8080"))
	p.resolved = true
	p.source = credentialsFileSourceName
	if got, want := p.Source(), "the credentials file [127.0.0.1:8080]"; got != want {
		t.Errorf("Source() = %q, want %q", got, want)
	}
}

func TestSourceIsEmptyWithNoCredentialConfigured(t *testing.T) {
	// The loopback no-auth posture: the ladder yields nothing, so there is no
	// source to name and a 401 stays unadorned.
	t.Setenv(TokenEnv, "")
	t.Setenv(TokenCommandEnv, "")
	p := NewBearerProvider(mustURL(t, "http://127.0.0.1:8080"))
	resolveProvider(t, p)
	if got := p.Source(); got != "" {
		t.Errorf("Source() = %q, want empty for the no-auth posture", got)
	}
}
