// Written fresh for OSS beads S2 (no bd-enterprise source copied). See
// internal/storage/backends/backends.go's OpenOptions.Credential doc: "S1
// does not add a RequireCredential knob to enforce the multi-tenant rule
// mechanically — that is deferred to S2." This file is that knob, plus the
// wrapper type a caller uses to carry a CredentialProvider through
// backends.OpenOptions in the first place.
package httpclient

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/steveyegge/beads/internal/storage/backends"
)

// ProvidedCredential adapts an embedder-supplied CredentialProvider into a
// backends.Credential — the opaque per-open marker interface OpenOptions
// carries — so a multi-tenant embedder (for example gc, Gas City: one process
// serving many workspaces, called "cities") can route a per-workspace
// credential through backends.OpenOptions without the backends package, or
// this package's own OpenWith wiring, needing to agree on anything but this
// one wrapper type.
//
// Provider is required. A ProvidedCredential with a nil Provider is refused
// by ResolveCredential exactly as a missing credential is in REQUIRE mode: a
// present-but-empty wrapper would otherwise look like "a credential was
// supplied" to a caller checking opts.Credential != nil, while actually
// authorizing nothing — the same silent-downgrade risk
// OpenOptions.Credential's own doc comment warns about for the ambient
// fallback case.
type ProvidedCredential struct {
	Provider CredentialProvider
}

// BackendCredential satisfies backends.Credential. It is a zero-cost marker
// with no behavior; see that interface's doc comment for why it exists.
func (ProvidedCredential) BackendCredential() {}

// ErrCredentialRequired reports a REQUIRE-mode resolution with no usable
// credential: either opts.Credential was nil, or it was a ProvidedCredential
// whose Provider was nil. See ResolveCredential.
var ErrCredentialRequired = errors.New("httpclient: a credential is required for this open, but none was supplied; the ambient bearer ladder is disabled in REQUIRE mode")

// ResolveCredential turns opts.Credential into the CredentialProvider a Dial
// authorizes requests with.
//
// Four cases:
//
//   - opts.Credential is nil and require is false: the default single-tenant
//     CLI posture. Returns NewBearerProvider(base), the ambient bearer ladder
//     (BEADS_HTTP_TOKEN, then BEADS_HTTP_TOKEN_COMMAND, then the credentials
//     file, then no credential at all).
//   - opts.Credential is nil and require is true: ErrCredentialRequired. This
//     is the knob a multi-tenant embedder sets so a missing per-workspace
//     credential is a loud refusal instead of a silent fall-through to
//     process-global ambient environment — which cannot distinguish one
//     tenant's workspace from another's, exactly the risk
//     OpenOptions.Credential's doc comment names.
//   - opts.Credential is a ProvidedCredential with a non-nil Provider: that
//     Provider is returned directly, regardless of require. An embedder that
//     took the trouble to supply one has already satisfied REQUIRE mode's
//     purpose.
//   - opts.Credential is a ProvidedCredential with a nil Provider, or any
//     other non-nil backends.Credential this package does not recognize:
//     refused. A nil Provider is ErrCredentialRequired (see its doc); any
//     other type is backends.ErrUnsupportedCredential, wrapped with the
//     concrete type so the refusal is legible, per the contract
//     Backend.OpenWith implementations must follow (see
//     internal/storage/backends/backends.go and
//     TestBackendOpenWithRefusesUnsupportedCredentialType).
func ResolveCredential(opts backends.OpenOptions, base *url.URL, require bool) (CredentialProvider, error) {
	if opts.Credential == nil {
		if require {
			return nil, ErrCredentialRequired
		}
		return NewBearerProvider(base), nil
	}
	pc, ok := opts.Credential.(ProvidedCredential)
	if !ok {
		return nil, fmt.Errorf("httpclient: credential %T: %w", opts.Credential, backends.ErrUnsupportedCredential)
	}
	if pc.Provider == nil {
		return nil, fmt.Errorf("httpclient: ProvidedCredential.Provider is nil: %w", ErrCredentialRequired)
	}
	return pc.Provider, nil
}
