// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/credential.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"net/http"
)

// CredentialProvider authorizes outbound requests.
//
// Implementations must be safe for concurrent use, must never log or persist a
// secret, and must FAIL CLOSED: a configured source that errors aborts the
// request rather than silently downgrading to a lower rung of the ladder. That
// is the postgres credential ladder's rule
// (internal/storage/postgres/credential.go), and it is the difference between a
// misconfigured token file and an unauthenticated request nobody noticed.
//
// The interface is declared here rather than in the store package the design
// sketches it in (engdocs/design/http-client-backend.md, D5) because this is the
// only layer that consumes it; the store package aliases it, so there is still
// one type.
type CredentialProvider interface {
	// Authorize adds credentials to req (Authorization: Bearer, DPoP, ...).
	// A no-auth deployment — the loopback-trust OSS server, which has no token
	// file at all — returns nil without modifying req.
	//
	// The error must not carry the credential: it travels into the client's own
	// error text, which reaches logs and terminals.
	Authorize(ctx context.Context, req *http.Request) error

	// Refresh is consulted after exactly one 401 on an authorized request.
	// Returning retry=true re-issues that request once, through Authorize
	// again; returning retry=false surfaces the 401 as it stands.
	//
	// This is the client half of the server's token-rotation contract
	// (internal/httpapi/auth.go: the token file is re-read on a ~1s gate, so
	// rotation is write {new,old}, roll the clients, drop old). A client that
	// rolled mid-flight 401s once and succeeds on the retry. One retry is the
	// whole window: a second 401 is a credential this server does not accept,
	// and retrying it again would only turn a clear refusal into a loop.
	Refresh(ctx context.Context) (retry bool, err error)
}
