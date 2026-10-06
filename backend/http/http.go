// Package bdhttp is the public door to the http client store that ships
// inside this module (internal/httpclient) — the storage backend that speaks
// the v0 `bd serve` wire to a remote server — for embedders that link beads
// as a library rather than running cmd/bd.
//
// Such an embedder cannot reach the store any other way, for the reason its
// (hypothetical) postgres sibling would document: the registry facade's
// Register takes a backends.Backend value whose constructors live in an
// internal package, and the only other production wiring is cmd/bd's. Without
// this package a workspace whose .beads/metadata.json names "http" is a
// workspace an embedder can only tombstone.
//
// # Registration is two-step, and that is why Register takes Options
//
// Putting "http" in the registry is not enough to open anything: the store
// dials through a process-wide transport constructor that registration does
// not install on its own, so a binary that registered and stopped there would
// fail every open with httpclient.ErrNoTransport — a build-wiring fault, not a
// workspace or server problem. Register does both: it adds "http" to the
// registry AND installs the default dialer, and it installs a per-open
// OpenWith hook so a multi-tenant embedder (for example gc, Gas City — one
// process serving many workspaces, called "cities") can route a distinct
// credential through backends.OpenOptions / beads.OpenBestAvailableWith for
// each workspace it opens, without the process-wide dialer being asked to
// serve two tenants with one fixed credential.
//
// # Stability
//
// EXPERIMENTAL, on the same terms as the backend package this sits beside.
// Pin an exact beads version.
package bdhttp

import (
	"context"
	"net/http"

	"github.com/steveyegge/beads/backend"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// UserAgentSuffix identifies this backend inside a stamped User-Agent, so a
// server log line says both which binary and which client spoke to it. An
// embedder that stamps its own build joins the two the way cmd/bd does:
// "myapp/1.4.2 " + bdhttp.UserAgentSuffix.
const UserAgentSuffix = httpclient.WireUserAgentSuffix

// Options configures the transport Register installs and Open/Handshake dial
// with.
//
// The zero value is usable and is what the CLI's own wiring amounts to: the
// built-in bearer credential ladder, this module's default User-Agent, and
// the standard http transport.
type Options struct {
	// UserAgent identifies the embedding build on every request. Empty sends
	// UserAgentSuffix alone, which names the backend but not the binary.
	UserAgent string
	// HTTPClient replaces the transport — a tuned timeout, a proxy, a custom
	// TLS config, an httptest server in a test. Nil builds one. The client is
	// used as given except for redirect handling, which the underlying wire
	// client always forces off: a followed 30x would replay the Authorization
	// header at whatever host the Location named.
	HTTPClient *http.Client
	// RequireCredential makes the registered backend's OpenWith seam refuse a
	// registered open with no OpenOptions.Credential, rather than silently
	// falling back to the ambient bearer ladder (BEADS_HTTP_TOKEN,
	// BEADS_HTTP_TOKEN_COMMAND, the credentials file). It has no effect on
	// Open/Handshake below, or on the plain Open/OpenReadOnly path a
	// single-tenant CLI process uses: both keep the ambient ladder as their
	// documented, legitimate posture.
	//
	// Set this when every caller through beads.OpenBestAvailableWith is a
	// multi-tenant embedder for which ambient, process-global credential
	// state can never safely stand in for one workspace's own — see
	// httpclient.ResolveCredential and backends.OpenOptions.Credential's own
	// doc comment.
	RequireCredential bool
}

// Register adds the http store to the backend registry under the name "http"
// AND installs the transport it dials with, so a workspace whose
// metadata.json selects the backend opens instead of failing with
// httpclient.ErrNoTransport.
//
// The EMBEDDER calls this explicitly at process start, on the same terms as
// cmd/bd's own wiring: registration is a property of the distribution being
// built, not of the import graph. A plain `go build` of an OSS binary that
// imports this package only transitively, and never calls Register, gains no
// selectable "http" backend — metadata.json naming it still hard-fails with
// the registry's UnknownBackendError, exactly as before this package existed.
//
// Register panics on a duplicate registration, like any process-start wiring
// error, so it must have exactly one production call site. Call it once,
// before any concurrent store access.
func Register(opts Options) {
	base := httpclient.DialOptions{UserAgent: opts.UserAgent, HTTPClient: opts.HTTPClient}
	backends.Register(httpclient.Backend, backends.Backend{
		Open:                httpclient.NewFromConfig,
		OpenReadOnly:        httpclient.NewReadOnlyFromConfig,
		WorkspaceIsBeadsDir: true,
		Remote:              true,
		OpenWith: func(ctx context.Context, beadsDir string, bopts backends.OpenOptions) (backend.DoltStorage, error) {
			return httpclient.OpenWith(ctx, beadsDir, bopts, base, opts.RequireCredential)
		},
	})
	httpclient.RegisterDefaultDialer(base)
}

// Open dials target and returns a store for it, with no workspace on disk.
//
// It is the door for an embedder that already knows its server — from its own
// configuration, or minted per tenant — and would otherwise have to write a
// .beads directory for the sole benefit of a registry lookup. Registration is
// not required and does not affect it: Open dials fresh from the Options it is
// handed, using the same built-in bearer ladder Open/OpenReadOnly use (see
// Options.RequireCredential's doc for why a per-tenant credential belongs on
// the registry's OpenWith seam instead, via beads.OpenBestAvailableWith).
//
// The store carries no local metadata file, which is benign: the two
// per-user keys it would hold read as unset and write nowhere.
func Open(ctx context.Context, target Target, opts Options) (backend.DoltStorage, error) {
	dialOpts := httpclient.DialOptionsForTarget(target, httpclient.DialOptions{UserAgent: opts.UserAgent, HTTPClient: opts.HTTPClient})
	creds := httpclient.NewBearerProvider(target.BaseURL)
	conn, err := httpclient.DialWith(target, creds, dialOpts)
	if err != nil {
		return nil, err
	}
	return httpclient.New(target, conn, nil), nil
}

// Handshake dials target once and returns its startup snapshot, applying the
// two gates a store applies: api_version equality and, when the target pins
// one, project identity.
//
// It is the pre-connect probe: `bd connect` verifies a server before it
// writes anything, and it has to do that without opening a store, because the
// workspace it is about to describe may not select this backend yet. A
// mismatch comes back as *ProjectMismatchError, which names both ids plus the
// server's own database and repo root.
//
// It dials with the same credentials Open would, so a probe can never verify
// a server the store then cannot reach.
func Handshake(ctx context.Context, target Target, opts Options) (*ServerSnapshot, error) {
	dialOpts := httpclient.DialOptionsForTarget(target, httpclient.DialOptions{UserAgent: opts.UserAgent, HTTPClient: opts.HTTPClient})
	// httpclient.Handshake dials via httpclient.Dial, which binds the same
	// built-in bearer ladder (httpclient.NewBearerProvider) Open uses above —
	// "the same credentials Open would use" holds without restating the dial
	// here.
	body, err := httpclient.Handshake(ctx, target, dialOpts)
	if err != nil {
		return nil, err
	}
	return newServerSnapshot(body), nil
}

// ServerSnapshot is what a server says about itself at handshake, curated for
// this door.
//
// It is a struct of its own rather than an alias of the wire document, and
// the omissions are the point. The wire type is GENERATED from the OpenAPI
// spec, so aliasing it would make a codegen bump — a field renamed, a member
// added — a breaking change to a published Go API, decided by a document
// nobody edits with that in mind. And the document carries facts an embedder
// has no business depending on: the server's own filesystem paths, its
// storage mode, its logical database name, the CLI's JSON schema version.
// Those are the operator's, and several are host paths a multi-tenant caller
// should not be holding at all.
//
// What remains is what a client actually decides with: Capabilities is the
// field it decides with (an operation is available because the server
// advertises its token, never because a version string looked new enough),
// and WireRevision is what `bd connect` checks against the client's own
// compiled revision before recording a server.
type ServerSnapshot struct {
	// APIVersion is the path major the server serves. The handshake already
	// refused anything this client cannot address, so on a returned snapshot
	// this is confirmation rather than a branch.
	APIVersion string
	// BdVersion is the release of the serving binary. Diagnostic and
	// human-facing: branch on Capabilities, not on this.
	BdVersion string
	// WireRevision is the server's own wire_revision counter (see
	// ContextResponse.WireRevision). `bd connect` compares it against the
	// client's compiled MinClientWireRevision expectations before recording
	// a server as this workspace's target.
	WireRevision int
	// ProjectID is the workspace identity the server owns. When the Target
	// pinned one, the handshake has already proved they match.
	ProjectID string
	// Capabilities are the operation tokens the server implements. The list
	// grows additively and an operation never appears unless it is fully
	// implemented, which is what makes membership the right question.
	Capabilities []string
}

// newServerSnapshot maps the wire document onto the curated one.
//
// The capability slice is copied because the document it comes from is a
// pointer INTO the wire client's cached handshake, which every later dispatch
// reads to decide what the server can serve, and whose documented contract is
// that the caller treats it as read-only.
func newServerSnapshot(body *apigen.ContextResponse) *ServerSnapshot {
	if body == nil {
		return nil
	}
	return &ServerSnapshot{
		APIVersion:   body.ApiVersion,
		BdVersion:    body.BdVersion,
		WireRevision: body.WireRevision,
		ProjectID:    body.ProjectId,
		Capabilities: append([]string(nil), body.Capabilities...),
	}
}
