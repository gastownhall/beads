// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/storage/backends"
)

// stubProvider is a minimal CredentialProvider for tests: it stamps a fixed
// header value so a test can prove WHICH provider ResolveCredential returned,
// without pulling in the bearer ladder's env/file machinery.
type stubProvider struct{ tag string }

func (s stubProvider) Authorize(_ context.Context, req *http.Request) error {
	req.Header.Set("X-Stub-Provider", s.tag)
	return nil
}

func (s stubProvider) Refresh(context.Context) (bool, error) { return false, nil }

// fixtureBackendCredential is a backends.Credential this package does not
// recognise — the same role fixtureNarrowCredential plays in
// internal/storage/backends/open_with_test.go, reused here so this test does
// not depend on that package's unexported fixture.
type fixtureBackendCredential struct{}

func (fixtureBackendCredential) BackendCredential() {}

func TestResolveCredentialNilNotRequiredFallsBackToTheAmbientLadder(t *testing.T) {
	clearCredentialEnvironment(t)
	t.Setenv(TokenEnv, "ambient-token")
	base := mustParseURL(t, "http://127.0.0.1:8080")

	cp, err := ResolveCredential(backends.OpenOptions{}, base, false)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if _, ok := cp.(*BearerProvider); !ok {
		t.Fatalf("ResolveCredential returned %T, want *BearerProvider", cp)
	}
	if got := authorizeWith(t, cp.(*BearerProvider)); got != "Bearer ambient-token" {
		t.Errorf("Authorization = %q, want the ambient ladder's token", got)
	}
}

func TestResolveCredentialNilAndRequiredRefuses(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")

	cp, err := ResolveCredential(backends.OpenOptions{}, base, true)
	if cp != nil {
		t.Fatal("ResolveCredential returned a non-nil provider alongside the refusal")
	}
	if !errors.Is(err, ErrCredentialRequired) {
		t.Fatalf("ResolveCredential error = %v, want %v", err, ErrCredentialRequired)
	}
}

func TestResolveCredentialProvidedCredentialIsReturnedEvenWhenRequired(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")
	want := stubProvider{tag: "tenant-7"}

	for _, require := range []bool{false, true} {
		cp, err := ResolveCredential(backends.OpenOptions{Credential: ProvidedCredential{Provider: want}}, base, require)
		if err != nil {
			t.Fatalf("ResolveCredential(require=%v): %v", require, err)
		}
		if cp != CredentialProvider(want) {
			t.Fatalf("ResolveCredential(require=%v) = %v, want the supplied provider", require, cp)
		}
	}
}

func TestResolveCredentialProvidedCredentialWithNilProviderRefuses(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")

	cp, err := ResolveCredential(backends.OpenOptions{Credential: ProvidedCredential{}}, base, false)
	if cp != nil {
		t.Fatal("ResolveCredential returned a non-nil provider alongside the refusal")
	}
	if !errors.Is(err, ErrCredentialRequired) {
		t.Fatalf("ResolveCredential error = %v, want %v", err, ErrCredentialRequired)
	}
}

func TestResolveCredentialUnrecognisedCredentialTypeRefuses(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:8080")

	cp, err := ResolveCredential(backends.OpenOptions{Credential: fixtureBackendCredential{}}, base, false)
	if cp != nil {
		t.Fatal("ResolveCredential returned a non-nil provider alongside the refusal")
	}
	if !errors.Is(err, backends.ErrUnsupportedCredential) {
		t.Fatalf("ResolveCredential error = %v, want it to wrap %v", err, backends.ErrUnsupportedCredential)
	}
	// require must not change this outcome: an unrecognised type is refused
	// either way, since returning the ambient ladder here would silently
	// ignore a credential the caller clearly intended to supply.
	if _, err := ResolveCredential(backends.OpenOptions{Credential: fixtureBackendCredential{}}, base, true); !errors.Is(err, backends.ErrUnsupportedCredential) {
		t.Fatalf("ResolveCredential(require=true) error = %v, want it to wrap %v", err, backends.ErrUnsupportedCredential)
	}
}

func TestProvidedCredentialSatisfiesBackendsCredential(t *testing.T) {
	var _ backends.Credential = ProvidedCredential{}
}
