// Written fresh for OSS beads S6 (no bd-enterprise source copied).
package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/backends"
)

// literalSecretProvider is a CredentialProvider whose unexported field holds
// the literal secret value, the same shape a real token-bearing provider
// takes. It exists only so a test can grep for that literal in an error
// string: fmt's %+v prints unexported struct fields by value, so if OpenWith
// (or anything it calls) ever formats opts.Credential directly into an error
// — rather than formatting only the target and the wrapped dial error, as
// open_with.go's "dialing %s: %w" does today — this field's value leaks into
// logs and terminals, exactly what wire.CredentialProvider's own doc comment
// on Authorize forbids ("The error must not carry the credential").
type literalSecretProvider struct {
	secret string
}

func (literalSecretProvider) Authorize(context.Context, *http.Request) error { return nil }
func (literalSecretProvider) Refresh(context.Context) (bool, error)          { return false, nil }

// TestOpenWithDialErrorNeverFormatsTheCredential kills the mutation that
// widens open_with.go's dial-failure wrap from
// `fmt.Errorf("dialing %s: %w", target, err)` to one that also interpolates
// opts.Credential (e.g. "dialing %s cred=%+v: %w"). A credential a caller
// supplies through OpenOptions.Credential must never surface in an error this
// package hands back — the error travels into the embedder's own logs and
// terminals, same as the doc comment on CredentialProvider.Authorize says.
//
// The dial target names a CA file that does not exist, which DialWith (via
// dialTransport) refuses before opening any socket: a config-time error, not
// a network one, so the test stays fast and never reaches any real network
// — the standing safety rule for this suite — while still giving OpenWith a
// real, non-nil error from the exact call it wraps.
func TestOpenWithDialErrorNeverFormatsTheCredential(t *testing.T) {
	beadsDir := t.TempDir()
	base, err := url.Parse("http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	target := Target{BaseURL: base, CAFile: filepath.Join(beadsDir, "no-such-ca.pem")}
	if err := SaveTarget(beadsDir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	const secret = "KILL-ME-SECRET-1234"
	opts := backends.OpenOptions{
		Credential: ProvidedCredential{Provider: literalSecretProvider{secret: secret}},
	}

	_, err = OpenWith(context.Background(), beadsDir, opts, DialOptions{}, false)
	if err == nil {
		t.Fatal("OpenWith against an unreachable loopback port returned no error; cannot assert what its error does, or does not, carry")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("OpenWith's error carries the literal credential secret %q: %v", secret, err)
	}
}
