package bdhttp

import (
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// The refusal taxonomy, re-exported so an embedder can CLASSIFY what this
// backend answers rather than match its text.
//
// Every name here is an alias of the internal one, so errors.Is and
// errors.As reach the same values across the module boundary. Two audiences
// need that: the host application deciding whether a failure is the user's,
// the server's or the wiring's, and any surface that renders it, since these
// types carry the server URL, its release and its advertised capabilities
// precisely so a renderer parses nothing.

// ErrNoTransport reports a process that registered this backend without
// installing a transport. It is a build-wiring fault, not a user's: the
// workspace and the server may both be fine. Register cannot produce it — it
// installs the transport itself — so meeting it means something reached the
// registry another way (for example a test double that calls
// backends.Register directly).
var ErrNoTransport = httpclient.ErrNoTransport

// The store's own refusals. A method this backend does not serve returns
// UnsupportedError, which unwraps to the portable storage.ErrUnsupported, so
// the classification a caller already does for a local backend holds
// unchanged.
type (
	// UnsupportedError is what every refused method of this backend returns: a
	// STRUCT, reached with errors.As, carrying the server it refused on, that
	// server's release and the capabilities it does advertise. It unwraps to
	// the portable storage.ErrUnsupported, so a caller that only wants "this
	// backend cannot do that" matches the same sentinel it would for a local
	// store and needs none of these fields.
	UnsupportedError = httpclient.ErrHTTPUnsupported
	// InexpressibleError is a request carrying a field the v0 wire has no
	// member for. It is both an unsupported-operation refusal and an encoder
	// refusal, because two audiences classify it, and errors.As reaches either
	// arm.
	InexpressibleError = httpclient.InexpressibleError
	// PartialIDSearchError answers a substring or prefix id lookup, which no
	// server operation serves. The input may be a partial id OR a full id that
	// does not exist, and the client cannot tell which, so the refusal names
	// both outcomes.
	PartialIDSearchError = httpclient.PartialIDSearchError
	// WatchRefusedError refuses a `--watch`-style polling loop, which
	// amplifies load on a shared server with no change detection to justify
	// it.
	WatchRefusedError = httpclient.WatchRefusedError
	// RedactedSettingError reports a setting whose value the server withheld
	// because its key marks it credential-bearing. It is not an unset key: one
	// says the workspace stores nothing, the other says the workspace stores
	// something this client may not see.
	RedactedSettingError = httpclient.RedactedSettingError
)

// The class sentinels behind those types, for a caller that classifies
// without reaching for fields.
var (
	ErrPartialIDSearch  = httpclient.ErrPartialIDSearch
	ErrWatchUnsupported = httpclient.ErrWatchUnsupported
	ErrSettingRedacted  = httpclient.ErrSettingRedacted
)

// The wire's own refusals, which reach a caller through Open, Handshake and
// every dispatch a store makes.
type (
	// ProjectMismatchError is the wrong-server diagnostic: the server
	// answered, and it owns a different workspace. It names both project ids
	// AND the server's own database and repo root, because "the ids differ"
	// does not say WHICH wrong server answered — and on a shared host that is
	// the question.
	ProjectMismatchError = wire.ProjectMismatchError
	// APIVersionError reports a server on a path major this client cannot
	// address. It names both versions, because neither alone says which side
	// to move.
	APIVersionError = wire.APIVersionError
	// CapabilityError reports an operation the server does not advertise. It
	// is how version skew is told apart from a missing entity: an unrouted
	// path on an older server answers a bare 404, indistinguishable from
	// not_found, so the advertised list is consulted before the dial.
	CapabilityError = wire.CapabilityError
	// ProblemError is a refusal the server described: its status, its
	// problem code, and the typed discriminators it carries. Unwrap reaches
	// the sentinel below that the code maps to.
	ProblemError = wire.ProblemError
)

var (
	ErrProjectMismatch  = wire.ErrProjectMismatch
	ErrAPIVersion       = wire.ErrAPIVersion
	ErrCapabilityAbsent = wire.ErrCapabilityAbsent

	// ErrUnauthenticated is the credential the server refused. The error
	// names which ladder rung held it — a provenance label, never the
	// credential — so an operator knows what to rotate.
	ErrUnauthenticated = wire.ErrUnauthenticated
	ErrBadRequest      = wire.ErrBadRequest
	ErrInvalidCursor   = wire.ErrInvalidCursor
	ErrBusy            = wire.ErrBusy
	ErrDBUnavailable   = wire.ErrDBUnavailable
	ErrServerFault     = wire.ErrServerFault
)

// ProjectMismatchRecovery is the one action that resolves a wrong-server
// mismatch: point the workspace back at the server that owns it (re-run
// `bd connect`). There is no local database to reconcile, so the repair
// tooling a local backend would name does not apply.
const ProjectMismatchRecovery = wire.ProjectMismatchRecovery
