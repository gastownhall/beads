package bdhttp

import (
	"net/url"

	"github.com/steveyegge/beads/internal/httpclient"
)

// CredentialProvider authorizes outbound requests to a bd serve.
//
// An implementation must be safe for concurrent use, must never log or
// persist a secret, and must FAIL CLOSED: a configured source that errors
// aborts the request rather than silently downgrading to an unauthenticated
// one.
type CredentialProvider = httpclient.CredentialProvider

const (
	// TokenEnv carries the bearer token itself, the highest rung of the
	// built-in ladder.
	// #nosec G101 -- the NAME of an environment variable, not a credential.
	TokenEnv = httpclient.TokenEnv
	// TokenCommandEnv names a helper that prints a token, either bare or in
	// the kubectl ExecCredential envelope {"token","expirationTimestamp"}.
	// #nosec G101 -- the NAME of an environment variable, not a credential.
	TokenCommandEnv = httpclient.TokenCommandEnv
)

// BearerProvider is the built-in credential ladder: TokenEnv, then
// TokenCommandEnv, then the credentials file's [host:port] section, then no
// credential at all (the tip OSS server's loopback-trust posture, a
// legitimate answer and not a failure).
type BearerProvider = httpclient.BearerProvider

// NewBearerProvider builds the built-in ladder for a server. Open and
// Handshake already dial with it; it is exported so an embedder that wants to
// WRAP the default (decorate it for one target, delegate to it for the rest)
// does not have to reimplement it.
func NewBearerProvider(base *url.URL) *BearerProvider {
	return httpclient.NewBearerProvider(base)
}

// ProvidedCredential adapts a CredentialProvider into the opaque
// backends.Credential marker interface, so it can be carried through
// beads.OpenOptions / beads.OpenBestAvailableWith (and, directly, through
// backends.OpenOptions) to the registered "http" backend's OpenWith hook
// Register installs.
//
// A multi-tenant embedder (for example gc, Gas City: one process serving many
// workspaces, called "cities") that wants a distinct credential per workspace
// wraps that workspace's own provider in a ProvidedCredential when it opens
// through the registry (the direct door takes the provider itself, as
// Options.Credential): the registered dialer Register installs is
// process-wide and cannot hold one credential per tenant, but the per-open
// OpenWith seam can, and this is the opaque wrapper type that seam
// recognizes. Either way the supplied provider is the ONLY credential the
// open uses, and no ambient credential, CA or plaintext grant is read; see the
// package doc's "Credentials" section. See httpclient.ResolveCredential and
// backends.OpenOptions.Credential's own doc comment for the single-tenant
// ambient-ladder default this exists to opt out of.
type ProvidedCredential = httpclient.ProvidedCredential
