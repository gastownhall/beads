// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/target.go (store transport seam)@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// WireClient is the transport seam between this store and a remote bd serve.
//
// The store skeleton dispatches no operations: the wire-core bead owns
// transport and each role bead grows this interface as it wires its operation.
// ServerContext is here from the start because the store itself owns the
// handshake snapshot — the workspace-identity probe reads it (D4) and the
// refusal taxonomy renders it (D7) — and because D6 makes the fetch lazy, so a
// store can be constructed before one exists.
//
// THE TWO HALVES ARE SHAPED DIFFERENTLY, and the difference is the roles', not
// an inconsistency. The write roles declare OPERATION-SHAPED methods in an
// interface of their own (WriteWire, embedded below): each one is a distinct
// body, a distinct request document and a distinct set of typed conflicts to
// reconstruct, so a role bead states exactly the operations it needs. The read
// roles dispatch GENERICALLY — they differ only in the parameters the encoder
// produced and the envelope they decode — so what they need from a transport is
// not an operation but the two primitives every dispatch goes through, which is
// why Preflight and Do sit on this interface rather than in a ReadWire nobody
// would implement separately.
//
// They are two primitives rather than one on purpose: D6's two-speed policy has
// to be able to refuse a post-baseline operation BEFORE a request is built, and
// folding the handshake into the round trip would make that unexpressible. Both
// are satisfied verbatim by *wire.Client.
type WireClient interface {
	ServerContext(ctx context.Context) (*apigen.ContextResponse, error)

	// Preflight applies the capability policy for an operation: a baseline
	// operation returns immediately, every other one forces the handshake and
	// checks its token.
	Preflight(ctx context.Context, op string) error
	// Do issues one v0 request and decodes its 2xx body into out, which may be
	// nil for a response the caller does not read.
	Do(ctx context.Context, req wire.Request, out any) error

	// WriteWire is the write roles' half of the seam (writewire.go). A role
	// bead grows this interface by embedding its own, rather than by appending
	// methods here: the store still holds ONE transport and the compiler still
	// proves a build linked a complete one, but the operations a role needs are
	// declared beside the role that needs them.
	WriteWire

	// StreamWire is the WATCH role's half of the seam (journal_watch.go). It is
	// embedded rather than folded into the two read primitives above because a
	// stream is neither a generic Do nor a Preflight: it is one operation-shaped
	// method whose result is a live *wire.EventStream the role drains, so it
	// belongs beside the role that opens it, the same rule WriteWire follows.
	StreamWire
}

// WireDialer builds the transport for a resolved target.
type WireDialer func(ctx context.Context, target Target) (WireClient, error)

// dialer is the process-wide transport constructor. It is nil until the
// wire-core bead registers one, which is what lets this package ship the store,
// the refusal surface and the registration glue without depending on transport.
var dialer WireDialer

// RegisterWireDialer installs the transport constructor Open uses. Init-time
// wiring only, the same rule as backends.Register: it is not safe against
// concurrent Opens and there is exactly one production call site.
func RegisterWireDialer(d WireDialer) { dialer = d }
