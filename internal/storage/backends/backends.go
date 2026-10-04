// Package backends provides the extension seam for storage backends that
// return storage.DoltStorage directly.
//
// OSS Beads registers no alternate backend. A downstream distribution adds a
// registrant package and blank-imports it from cmd/bd; shared validation,
// discovery, and store dispatch then recognize the name without modification.
// The Dolt family remains hand-wired because proxied mode returns a unit of
// work provider rather than a store.
//
// The seam covers opening and discovering an existing registered workspace, not
// provisioning one: bd init and bd bootstrap create or import Dolt only and
// reject registered names, so a downstream registrant supplies its own
// workspace-creation path.
//
// Registration is init-time wiring. A registrant calls Register once during
// process initialization, before any concurrent store access; OSS never
// deregisters, and Deregister exists only for isolated single-threaded contract
// tests. This registry and the backendnames set that config validation reads are
// updated together under this package's lock, so as long as callers honor the
// init-only rule the two never diverge. Do not Register or Deregister
// concurrently with store dispatch.
package backends

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backendnames"
)

// Credential is a per-open credential injection a backend's OpenWith may use
// to authenticate to a remote server on behalf of one specific workspace. This
// package assigns it no methods beyond the marker below and performs no type
// assertions against it: it is deliberately opaque so a generic embedder
// (for example gc, Gas City — one process serving many workspaces, called
// "cities") can carry a per-workspace credential through OpenOptions without
// this package, or the embedder, needing to know the concrete shape a future
// backend expects. A backend that needs a credential declares its own
// narrower interface in its own package and type-asserts the value its
// OpenWith receives.
//
// BackendCredential is a zero-cost marker method: every concrete credential
// type implements it (trivially, as a no-op), so passing an arbitrary value
// that was never meant to be a credential is a compile error instead of a
// silently accepted `any`.
type Credential interface {
	BackendCredential()
}

// OpenOptions carries per-open injections a backend's OpenWith may use. Open
// has no seam for any of these — it is the process-global, back-compat path.
// The zero value is the back-compat case: every field absent reproduces Open's
// existing behavior exactly, for every backend, registered or not.
type OpenOptions struct {
	// Credential authenticates this specific open to a remote backend's
	// target. Nil means "use the backend's own default authentication" —
	// which, for most backends, means whatever ambient environment (env
	// vars, a config file, a logged-in CLI session, ...) the backend's
	// default auth already reads. That ambient fallback is the right
	// default for a single-tenant CLI process, but it is almost never what
	// a multi-tenant embedder (for example gc, Gas City — one process
	// serving many workspaces, called "cities") wants: ambient env is
	// process-global, so it cannot distinguish one tenant's workspace from
	// another's. A multi-tenant embedder MUST pass a non-nil Credential for
	// every open; relying on the nil/ambient default in that setting risks
	// one tenant's request being silently authenticated as another's. A
	// backend with no OpenWith to honor a non-nil Credential refuses rather
	// than silently opening unauthenticated: see OpenWithOptions. (S1 does
	// not add a RequireCredential knob to enforce the multi-tenant rule
	// mechanically — that is deferred to S2.)
	Credential Credential

	// HTTPClient overrides the *http.Client (and therefore any RoundTripper)
	// a remote backend's OpenWith dials with. Nil means "use the backend's
	// default client." A backend with no OpenWith to honor a non-nil
	// HTTPClient refuses rather than silently ignoring the override and
	// dialing with its default client anyway: see OpenWithOptions. A caller
	// that asked for a specific transport (a proxy, a custom TLS config, a
	// test double, ...) must be told that request was not honored, not have
	// it silently dropped — the dial still "succeeding" against the wrong
	// transport can be worse than a loud refusal.
	HTTPClient *http.Client

	// UserAgent overrides the User-Agent string a remote backend's OpenWith
	// sends. Empty means "use the backend's default." Refused the same way
	// HTTPClient is when the backend has no OpenWith.
	UserAgent string
}

// ErrCredentialWithoutOpenWith is returned by Backend.OpenWithOptions when
// OpenOptions.Credential is set but the backend has no OpenWith to honor it.
// Dispatch refuses rather than silently falling back to Open and dropping the
// credential on the floor — a backend that cannot authenticate a per-open
// credential must say so, not open anonymously.
var ErrCredentialWithoutOpenWith = errors.New("backends: OpenOptions.Credential is set but the backend has no OpenWith to honor it; refusing to silently open without authenticating")

// ErrHTTPClientWithoutOpenWith is returned by Backend.OpenWithOptions when
// OpenOptions.HTTPClient is set but the backend has no OpenWith to honor it.
// Same fail-closed family as ErrCredentialWithoutOpenWith: a caller that
// asked for a specific transport must be told it was not honored, not have
// the request silently ignored in favor of the backend's default client.
var ErrHTTPClientWithoutOpenWith = errors.New("backends: OpenOptions.HTTPClient is set but the backend has no OpenWith to honor it; refusing to silently ignore the requested transport")

// ErrUserAgentWithoutOpenWith is returned by Backend.OpenWithOptions when
// OpenOptions.UserAgent is set but the backend has no OpenWith to honor it.
// Same fail-closed family as ErrCredentialWithoutOpenWith.
var ErrUserAgentWithoutOpenWith = errors.New("backends: OpenOptions.UserAgent is set but the backend has no OpenWith to honor it; refusing to silently ignore the requested User-Agent")

// ErrUnsupportedCredential is the typed refusal an OpenWith implementation
// MUST return — directly, or wrapped with backend-specific detail so that
// errors.Is(err, ErrUnsupportedCredential) still holds — when it receives a
// non-nil OpenOptions.Credential that does not type-assert to whatever
// narrower credential interface that backend's OpenWith expects. Credential
// is deliberately opaque at this package's level (see Credential's doc
// comment): this package performs no type assertion against it, so the
// assertion and the refusal-on-mismatch are entirely each OpenWith
// implementation's responsibility. Silently ignoring a Credential of the
// wrong type and opening with default/ambient auth instead would be worse
// than a refusal: it looks like the caller's credential was honored when it
// was not. A fake/test registrant's OpenWith is expected to demonstrate this
// contract the same way a production one must.
var ErrUnsupportedCredential = errors.New("backends: OpenOptions.Credential does not implement the credential type this backend's OpenWith expects")

// Backend describes a registered storage backend.
type Backend struct {
	// Open opens the workspace store read-write.
	Open func(ctx context.Context, beadsDir string) (storage.DoltStorage, error)

	// OpenReadOnly opens the workspace for a read-only command.
	OpenReadOnly func(ctx context.Context, beadsDir string) (storage.DoltStorage, error)

	// WorkspaceIsBeadsDir means metadata.json and the remote store are enough
	// to identify the workspace; no separately discoverable local DB exists.
	WorkspaceIsBeadsDir bool

	// Remote marks a backend that is a pure network client of a bd serve with
	// no database of its own (for example an http client backend). It is
	// narrower than WorkspaceIsBeadsDir, which a database-backed registrant
	// (for example Postgres) can also set: Postgres opens a database, so its
	// open failures ARE database failures, while a Remote backend's open
	// failure is a connection failure that "failed to open database" would
	// misname, and it has no local physical root for doltserver's
	// ResolvePhysicalRoots to gate. cmd/bd reads this to frame the
	// store-open error for the active backend.
	Remote bool

	// OpenWith opens the workspace store read-write, honoring per-open opts
	// (Credential, HTTPClient, UserAgent). It exists for a backend whose
	// dialer would otherwise be process-global, so it cannot support
	// multiple credentialed callers sharing one process — exactly the shape
	// an embedder like gc (Gas City, one process serving many workspaces)
	// needs for per-workspace credentials. Optional: nil means this backend
	// has no per-open seam, and OpenWithOptions falls back to Open — but
	// only for a zero OpenOptions; a non-nil Credential, non-nil HTTPClient,
	// or non-empty UserAgent is refused rather than silently dropped (see
	// OpenWithOptions). A backend that sets OpenWith should make Open behave
	// as OpenWith would with a zero OpenOptions, since OpenWithOptions calls
	// Open directly whenever OpenWith is nil, and existing callers of Open
	// never go through OpenWith at all.
	//
	// Contract for implementations: opts.Credential is typed only as the
	// opaque Credential marker interface (see its doc comment); an OpenWith
	// that expects a narrower, backend-specific credential type MUST type-
	// assert and, on a non-nil Credential that fails the assertion, refuse
	// with ErrUnsupportedCredential (directly, or wrapped so errors.Is still
	// matches) rather than silently falling back to default/ambient auth.
	// Opening unauthenticated when the caller supplied a credential — even
	// one of the wrong shape — would look like the credential was honored
	// when it was not.
	OpenWith func(ctx context.Context, beadsDir string, opts OpenOptions) (storage.DoltStorage, error)
}

// OpenWithOptions opens beadsDir through this backend, honoring opts. It
// calls OpenWith when the backend implements it. Otherwise it fails closed:
// a zero OpenOptions falls back to the required Open, but a non-nil
// Credential, non-nil HTTPClient, or non-empty UserAgent is never silently
// dropped — with no OpenWith to honor it, OpenWithOptions returns the
// matching ErrXWithoutOpenWith sentinel instead of opening with the field
// ignored. A zero OpenOptions always reaches plain Open, so this is a strict
// superset of calling Open directly.
func (b Backend) OpenWithOptions(ctx context.Context, beadsDir string, opts OpenOptions) (storage.DoltStorage, error) {
	if b.OpenWith != nil {
		return b.OpenWith(ctx, beadsDir, opts)
	}
	if opts.Credential != nil {
		return nil, ErrCredentialWithoutOpenWith
	}
	if opts.HTTPClient != nil {
		return nil, ErrHTTPClientWithoutOpenWith
	}
	if opts.UserAgent != "" {
		return nil, ErrUserAgentWithoutOpenWith
	}
	return b.Open(ctx, beadsDir)
}

var (
	mu       sync.RWMutex
	registry = make(map[string]Backend)
)

// Register adds a backend. Invalid or duplicate registrations panic because
// they are process-start wiring errors.
func Register(name string, backend Backend) {
	switch {
	case name == "":
		panic("backends: Register called with empty backend name")
	case name == "dolt":
		panic(`backends: backend name "dolt" is reserved`)
	case backend.Open == nil || backend.OpenReadOnly == nil:
		panic(fmt.Sprintf("backends: backend %q requires Open and OpenReadOnly", name))
	}

	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("backends: backend %q registered twice", name))
	}
	registry[name] = backend
	backendnames.Add(name)
}

// Deregister removes a backend. It exists for isolated contract tests;
// production registrants register once during process initialization.
func Deregister(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; !exists {
		return false
	}
	delete(registry, name)
	backendnames.Remove(name)
	return true
}

// Lookup returns the backend registered under name.
func Lookup(name string) (Backend, bool) {
	mu.RLock()
	defer mu.RUnlock()
	backend, ok := registry[name]
	return backend, ok
}

// Registered reports whether name has a backend implementation.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}

// WorkspaceIsBeadsDir reports whether name is a registered backend whose
// workspace is identified by the .beads directory alone — metadata.json plus a
// remote store — with no separately discoverable local database. It is the
// single authority for that classification so CLI and library discovery route
// such workspaces to the .beads directory instead of returning "no database".
func WorkspaceIsBeadsDir(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	backend, ok := registry[name]
	return ok && backend.WorkspaceIsBeadsDir
}

// IsRemote reports whether name is a registered backend that is a pure
// network client of a bd serve with no local database (see Backend.Remote).
// cmd/bd uses it to frame a store-open failure: a remote backend has no
// database, so the "failed to open database" prefix would name something
// that does not exist. doltserver.ResolvePhysicalRoots uses it to recognize
// that a registered remote backend has no local physical root to gate,
// instead of falling through to the embedded-Dolt default.
func IsRemote(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	backend, ok := registry[name]
	return ok && backend.Remote
}
