// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dial.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// WireUserAgentSuffix identifies the backend inside a stamped User-Agent, so a
// server log line says both which binary and which client spoke to it.
const WireUserAgentSuffix = wire.DefaultUserAgent

// DialOptions tunes the production transport.
type DialOptions struct {
	// UserAgent identifies the build. wire's own default names the backend only,
	// because the version stamp lives in package main and nothing under this
	// tree may import it; the activation layer passes the stamped one down.
	UserAgent string
	// HTTPClient replaces the transport. Tests use it to reach an httptest
	// server or to inject a round tripper; production leaves it nil.
	//
	// Leaving this nil is what lets DialWith apply a target's CA trust
	// (BEADS_HTTP_CA_FILE, or the sidecar's ca_file) automatically, and raise
	// MaxIdleConnsPerHost even when no CA is configured — see TransportFor and
	// baselineTransport.
	//
	// A caller that supplies an HTTPClient whose Transport is nil is not
	// silently skipped: DialWith injects the resolved CA transport (if any)
	// on a COPY of that client, leaving the caller's own *http.Client
	// untouched, exactly as if HTTPClient had been left nil except that the
	// other fields (Timeout, CheckRedirect, Jar) are the caller's.
	//
	// A caller that supplies an HTTPClient with a NON-NIL Transport has taken
	// the transport over completely. If the target has no CA configured, it
	// is used verbatim — DialWith has nothing to add. If the target DOES have
	// a CA configured, DialWith refuses rather than silently using a
	// transport that may not trust it: the caller must either leave
	// Transport nil (and let DialWith inject), or compose TransportFor(target)
	// (or TransportForFile, for the embedder door) into its own Transport
	// itself before calling.
	HTTPClient *http.Client

	// caOverride, when non-nil, replaces resolveCAFile's own env/sidecar
	// resolution entirely: DialWith uses *caOverride verbatim (which may
	// itself report "no CA configured" via its own configured() == false).
	//
	// It is set two ways, both of which need CAFileEnv taken completely out
	// of the loop rather than merely out-prioritized:
	//
	//   - DialOptionsForFile, which has already built HTTPClient's Transport
	//     from exactly one explicit file. Without this, DialWith's own
	//     env-scoped resolveCAFile would run anyway and could trip the
	//     caller-supplied-Transport refusal below the moment CAFileEnv
	//     happens to be set (and host-scoped-match) for the same target
	//     being verified.
	//   - DialOptionsForTarget, which backend/http's embedder door uses for
	//     EVERY dial: an embedder juggling several targets in one process
	//     must not have any of them silently renarrowed, or blocked, by an
	//     ambient CAFileEnv value a completely unrelated invocation
	//     exported — only Target.CAFile, passed explicitly, may ever apply
	//     on that door.
	//
	// Unexported: a caller outside this package can never set it directly, so
	// an external HTTPClient always goes through one of the two constructors
	// above, or the normal env-aware resolution.
	caOverride *resolvedCA
}

// Conn is the store's view of one wire client.
//
// It exists to answer the WireClient seam's ServerContext with the GATED
// handshake rather than the raw fetch: api_version equality and, when the
// workspace recorded one, project identity are the two checks a snapshot must
// pass before anything is served from it (design D6). GetContext would return a
// body from any server that answered.
type Conn struct {
	*wire.Client
}

func (c *Conn) ServerContext(ctx context.Context) (*apigen.ContextResponse, error) {
	snap, err := c.Handshake(ctx)
	if err != nil {
		return nil, err
	}
	body := snap.Context
	return &body, nil
}

// Dial builds the transport for one resolved target: the default credential
// ladder plus a wire client bound to it.
func Dial(target Target, opts DialOptions) (*Conn, error) {
	return DialWith(target, NewBearerProvider(target.BaseURL), opts)
}

// DialWith is Dial with the credential source chosen by the caller — the other
// half of the D5 seam, for the layers that ride it: a gateway dialer outside
// this repo and the public registrant (backend/http), each of
// which binds a provider this package does not know about.
//
// It exists so the target-to-client mapping has ONE body. The gateway dialer
// used to build the wire client itself, and the registrant would have been the
// second copy of the same six lines; what a copy really duplicates is
// ExpectProjectID, the identity pin that travels on the Target and is applied
// through the wire Options — a caller that dropped it would dial a client whose
// handshake silently skips the wrong-server gate.
//
// creds may be nil, which is the tip OSS server's loopback-trust posture: no
// Authorization header is sent and a 401 is never retried.
//
// When opts.HTTPClient is nil, or has a nil Transport, DialWith resolves
// target's CA trust (the same env-scoped resolution TransportFor does) and
// dials with a transport scoped to it — see DialOptions.HTTPClient for the
// exact rules, including the refusal when a caller-supplied HTTPClient
// already has its own Transport and the target has a CA configured. This is
// what lets `bd connect --ca-file` verify a server during its own Handshake
// call, before anything is written, and what lets the default and gateway
// dialers honor BEADS_HTTP_CA_FILE with no further plumbing. Every dial also
// gets the raised MaxIdleConnsPerHost ceiling (baselineTransport), whether or
// not a CA is configured — the RTT cost of an under-pooled host is the same
// either way.
func DialWith(target Target, creds CredentialProvider, opts DialOptions) (*Conn, error) {
	var resolved resolvedCA
	if opts.caOverride != nil {
		resolved = *opts.caOverride
	} else {
		var err error
		resolved, err = resolveCAFile(target)
		if err != nil {
			return nil, err
		}
	}

	switch {
	case opts.HTTPClient == nil:
		transport, terr := dialTransport(resolved)
		if terr != nil {
			return nil, terr
		}
		opts.HTTPClient = &http.Client{Transport: transport, Timeout: wire.DefaultTimeout}

	case opts.HTTPClient.Transport == nil:
		if resolved.configured() {
			transport, terr := transportForFile(resolved.path, resolved.label)
			if terr != nil {
				return nil, terr
			}
			clientCopy := *opts.HTTPClient
			clientCopy.Transport = transport
			opts.HTTPClient = &clientCopy
		}
		// Else: no CA configured for this target, so the caller's Transport
		// staying nil (meaning http.DefaultTransport once the *http.Client
		// runs) is already the system-roots behavior this target has never
		// asked to change; nothing to inject.

	default:
		if resolved.configured() {
			matched, verifyErr := matchesCachedTransport(opts.HTTPClient.Transport, resolved.path)
			if !matched {
				if errors.Is(verifyErr, errCAFileChangedSinceTransportBuilt) {
					return nil, fmt.Errorf(
						"%s configures a CA for %s, and the supplied HTTPClient's Transport was built by this package for the same file, but that file has changed since: %w",
						resolved.label, target, verifyErr)
				}
				if verifyErr != nil {
					return nil, fmt.Errorf(
						"%s configures a CA for %s, but verifying the supplied HTTPClient's Transport against the current file failed: %w",
						resolved.label, target, verifyErr)
				}
				return nil, fmt.Errorf(
					"%s configures a CA for %s, but the supplied HTTPClient's Transport is not the CA-scoped one this package builds for it; leave DialOptions.HTTPClient nil (or its Transport nil) to let DialWith inject it, or build the Transport from TransportFor(target) (or TransportForFile) so DialWith can recognize it as exactly that CA's transport — or, if you deliberately want a transport of your own choosing, clear the CA (Target.CAFile, and any %s pattern that matches this target) and own trust yourself",
					resolved.label, target, CAFileEnv)
			}
		}
		// Either no CA is configured for this target, or the caller's
		// Transport IS the cached CA-scoped transport TransportFor(target)
		// (or TransportForFile) builds for resolved.path — the documented
		// embedder recipe of composing that transport into HTTPClient before
		// calling. Either way there is nothing to inject and nothing to
		// refuse.
	}

	client, err := wire.New(target.BaseURL, creds, wire.Options{
		HTTPClient:      opts.HTTPClient,
		UserAgent:       opts.UserAgent,
		ExpectProjectID: target.ExpectProjectID,
	})
	if err != nil {
		return nil, err
	}
	return &Conn{Client: client}, nil
}

// dialTransport builds the transport DialWith uses when the caller left
// HTTPClient nil entirely: the CA-scoped one when resolved names a file, or
// the shared baseline (system roots, raised MaxIdleConnsPerHost) otherwise.
func dialTransport(resolved resolvedCA) (http.RoundTripper, error) {
	if resolved.configured() {
		return transportForFile(resolved.path, resolved.label)
	}
	return baselineTransport(), nil
}

var (
	baselineTransportOnce sync.Once
	baselineTransportRT   http.RoundTripper
)

// baselineTransport is the http.RoundTripper DialWith uses for a target with
// no CA configured: http.DefaultTransport's own system-roots TLS trust,
// completely unchanged, but with MaxIdleConnsPerHost raised the same way
// TransportFor raises it for a CA-scoped target (see maxIdleConnsPerHost in
// ca.go) — applied to every http-backend target, not only CA-configured ones,
// since the RTT cost of an under-pooled host is identical either way. Built
// once and reused for the life of the process: nothing about it depends on
// per-target state, so there is nothing to key a cache on and no reason to
// pay Clone() on every dial.
func baselineTransport() http.RoundTripper {
	baselineTransportOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck // http.DefaultTransport is always *http.Transport
		transport.MaxIdleConnsPerHost = maxIdleConnsPerHost
		baselineTransportRT = transport
	})
	return baselineTransportRT
}

// DefaultDialer is the WireDialer the enterprise distribution registers.
//
// It performs no handshake: design D6 makes the context fetch lazy, on the first
// post-baseline dispatch, so opening a workspace to run `bd ready` costs one
// round trip rather than two. The store owns that fetch and caches it.
func DefaultDialer(opts DialOptions) WireDialer {
	return func(_ context.Context, target Target) (WireClient, error) {
		return Dial(target, opts)
	}
}

// RegisterDefaultDialer installs the production transport. Init-time wiring
// only, the same rule as RegisterWireDialer itself.
func RegisterDefaultDialer(opts DialOptions) {
	RegisterWireDialer(DefaultDialer(opts))
}

// Handshake dials target once and returns its gated startup snapshot.
//
// It is the connect path's probe: `bd connect` verifies a server before it
// writes anything, and it must do so without opening a store, because the
// workspace it is about to describe may not select this backend yet. A project
// mismatch comes back as *wire.ProjectMismatchError, which names both ids plus
// the server's own database and repo root.
func Handshake(ctx context.Context, target Target, opts DialOptions) (*apigen.ContextResponse, error) {
	conn, err := Dial(target, opts)
	if err != nil {
		return nil, err
	}
	return conn.ServerContext(ctx)
}

// DialOptionsForFile returns base with HTTPClient set to a transport scoped
// to EXACTLY caFile, bypassing CAFileEnv and any sidecar entirely.
//
// It exists for `bd connect --ca-file X`: X must be verified for itself,
// before it is written anywhere, regardless of what BEADS_HTTP_CA_FILE
// currently resolves to for the same host — otherwise a `bd connect` run with
// the env var set to a good CA and --ca-file pointed at a wrong one would
// verify against the env's CA while writing the flag's unverified one to the
// sidecar. Passing the returned DialOptions (with Target.CAFile left "" for
// this call) to Handshake or Dial makes the verification match exactly what
// the flag names.
func DialOptionsForFile(caFile string, base DialOptions) (DialOptions, error) {
	transport, err := TransportForFile(caFile)
	if err != nil {
		return DialOptions{}, err
	}
	base.HTTPClient = &http.Client{Transport: transport, Timeout: wire.DefaultTimeout}
	// This transport was just built from exactly caFile, deliberately
	// bypassing CAFileEnv and any sidecar. DialWith must not re-resolve the
	// CA from the ambient environment for this call, nor refuse the
	// Transport just assigned above as an unrecognized caller-supplied one —
	// an empty override reports "no CA configured" to DialWith's default
	// branch, which is exactly right: this HTTPClient's Transport is already
	// final. See DialOptions.caOverride.
	empty := resolvedCA{}
	base.caOverride = &empty
	return base, nil
}

// DialOptionsForTarget returns base with CA resolution pinned to EXACTLY
// target.CAFile, bypassing CAFileEnv entirely — never consulting it, not even
// to prefer the sidecar over a non-matching pattern.
//
// It exists for backend/http's embedder door (newDialer), which must compute
// this FRESH for every target it dials: an embedder juggling several targets
// in one process must not have any of them silently renarrowed to a private
// CA, or blocked from a legitimate one, because an unrelated invocation
// exported CAFileEnv for a completely different target. Unlike
// DialOptionsForFile, this does not build a transport itself — base.HTTPClient
// is left exactly as the caller supplied it (nil, a client with a nil
// Transport for DialWith to inject into on a copy, or a client whose
// Transport the caller already built via TransportFor(target)), and DialWith
// resolves and injects, or recognizes and accepts, from this override alone.
func DialOptionsForTarget(target Target, base DialOptions) DialOptions {
	resolved := resolvedCA{}
	if caFile := strings.TrimSpace(target.CAFile); caFile != "" {
		resolved = resolvedCA{path: caFile, label: "ca_file"}
	}
	base.caOverride = &resolved
	return base
}
