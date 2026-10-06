// Written fresh for OSS beads S6 (no bd-enterprise source copied): closes
// the S6 review's MED-4 finding. BearerProvider.warnInsecure (credential.go)
// only ever WARNED that a bearer was bound for a non-loopback http:// target;
// `bd connect` (connect.go) separately REFUSES that combination, but only at
// connect time, and only for the ambient ladder it dials with there. Every
// ordinary open after that — Open, OpenReadOnly (the registered dialer) and
// OpenWith (the per-open seam backend/http's embedder door uses) — had no
// refusal at all: a sidecar or an embedder-supplied Target pinned at a
// non-loopback http:// host, however that happened, would have every later
// `bd` command send its bearer in the clear with nothing stopping it.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// AllowInsecureCredentialEnv opts an entire process into sending a credential
// over plain http to a non-loopback bd serve.
//
// It exists because Open/OpenReadOnly/OpenWith's callers — every bd command
// but connect — have no flag of their own to thread an opt-in through, unlike
// `bd connect --allow-plaintext`. Setting it is a deliberate, informed
// decision (the same one --allow-plaintext records at connect time): the
// operator trusts the network path, typically a reverse proxy terminating TLS
// in front of bd serve.
//
// #nosec G101 -- the NAME of an environment variable, not a credential.
const AllowInsecureCredentialEnv = "BEADS_HTTP_ALLOW_INSECURE"

func allowInsecureCredentialFromEnv() bool {
	v := strings.TrimSpace(os.Getenv(AllowInsecureCredentialEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// guardInsecureCredential wraps creds so that, once bound for a plain-http,
// non-loopback target, it refuses to let ANY credential header it adds reach
// the wire — unless allowed, which is true when EITHER opts.AllowInsecureCredential
// (DialOptions, which connect.go's own Handshake probe sets from
// --allow-plaintext before any sidecar exists) OR target.AllowInsecureCredential
// (the sidecar's persisted record of that same grant, read back on every
// LATER dial for this workspace — see Target.AllowInsecureCredential's own
// doc) is true, or BEADS_HTTP_ALLOW_INSECURE=1 is set in the environment.
//
// It wraps the resolved wire.CredentialProvider itself, never switching on
// its concrete type, so the refusal is uniform across the ambient
// BearerProvider ladder (Dial, Open, OpenReadOnly) AND an embedder-supplied
// ProvidedCredential (backends.OpenOptions.Credential, OpenWith's seam) — any
// implementation at all, including third-party ones this package has never
// heard of.
//
// A nil creds (the tip server's loopback-trust posture: no Authorization
// header is ever sent for an unauthenticated open) passes through untouched —
// there is nothing to guard, and refusing an unauthenticated plaintext dial
// would be a different, broader policy than the one this finding asks for
// ("refuse SENDING CREDENTIALS over http:// to non-loopback").
func guardInsecureCredential(target Target, creds CredentialProvider, allowed bool) CredentialProvider {
	if creds == nil {
		return nil
	}
	base := target.BaseURL
	if base == nil || base.Scheme != "http" || configfile.IsLocalHostString(base.Hostname()) {
		return creds
	}
	if allowed || allowInsecureCredentialFromEnv() {
		return creds
	}
	return &insecureCredentialGuard{inner: creds, endpoint: base.Redacted()}
}

// insecureCredentialGuard is guardInsecureCredential's refusal. It lets the
// inner provider run first (so a provider that attaches nothing — an
// unconfigured ambient ladder — is never refused) and only turns the request
// into an error when Authorize actually added a header: the diff is taken on
// the header count rather than on any named header, deliberately, since
// CredentialProvider's own doc names more than one scheme ("Authorization:
// Bearer, DPoP, ...") and a third-party provider may add a header this
// package has never heard of either.
type insecureCredentialGuard struct {
	inner    CredentialProvider
	endpoint string
}

func (g *insecureCredentialGuard) Authorize(ctx context.Context, req *http.Request) error {
	before := len(req.Header)
	if err := g.inner.Authorize(ctx, req); err != nil {
		return err
	}
	if len(req.Header) > before {
		return fmt.Errorf(
			"refusing to send a credential to %s over plain http: %s is not loopback, and the credential would cross the network unencrypted; use https://, run `bd connect --allow-plaintext` to accept the risk knowingly for this server, or set %s=1 to override",
			g.endpoint, req.URL.Hostname(), AllowInsecureCredentialEnv)
	}
	return nil
}

// Refresh delegates unchanged: a refused request never reaches a 401, so this
// guard has nothing of its own to add here.
func (g *insecureCredentialGuard) Refresh(ctx context.Context) (bool, error) {
	return g.inner.Refresh(ctx)
}

// Source forwards the inner provider's provenance report, structurally
// satisfying wire's unexported credentialSourceReporter without importing it,
// so a 401 after an ALLOWED insecure dial still names the right ladder rung.
func (g *insecureCredentialGuard) Source() string {
	if r, ok := g.inner.(interface{ Source() string }); ok {
		return r.Source()
	}
	return ""
}
