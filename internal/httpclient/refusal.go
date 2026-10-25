// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/refusal.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/httpclient/encode"
)

// InexpressibleError is what a caller gets when a request carries a field the
// v0 wire has no member for: the encoder's divergence-ledger row, joined to the
// portable refusal sentinel.
//
// It is two errors rather than one because two audiences classify it. A caller
// holding an issueops role reaches for *storage.ErrUnsupported — that is what
// the role contracts assert on a knob a backend cannot serve, and it is the
// same arm a local backend's refusal answers. The refusal taxonomy (D7) reaches
// for the LEDGER ROW, because case 3's text names the flag that produced the
// parameter and the reason it cannot cross. Neither is derivable from the
// other, so the error carries both and errors.Is/errors.As reach each.
//
// The Op it names is the ROLE METHOD, not a storage method: nothing on the
// unsupported allowlist refuses this way, and a refusal that named `IssueReader`
// would say the accessor is unserved when the accessor is exactly what answered.
type InexpressibleError struct {
	// Unsup is the portable sentinel, decorated with the server context D7
	// renders (see (*Store).unsupported).
	Unsup *ErrHTTPUnsupported
	// Refused is the encoder's refusal, carrying the ledger row: the flag or
	// field, the reason, the design decision and its pin.
	Refused *encode.RefusedError
	// detail is the ORIGINAL error's own Error() text, preserved so a refusal
	// a caller decorated with its own context before it reached inexpressible
	// (a batch item's index, a dependency's position, a patch member's name)
	// keeps naming that context. Empty when the original error IS the bare
	// refusal, which is reads' own shape today — see inexpressible's doc.
	detail string
}

func (e *InexpressibleError) Error() string {
	msg := e.detail
	if msg == "" {
		msg = e.Refused.Error()
	}
	if e.Unsup == nil || e.Unsup.ServerURL == "" {
		return msg
	}
	return fmt.Sprintf("%s (bd serve at %s)", msg, e.Unsup.ServerURL)
}

// Unwrap returns both arms, so errors.As reaches *storage.ErrUnsupported and
// errors.Is reaches encode.ErrRefused off the same value.
func (e *InexpressibleError) Unwrap() []error { return []error{e.Unsup, e.Refused} }

// inexpressible decorates an encoder refusal for op, or passes any other error
// through untouched.
//
// The pass-through matters: an encoder can also fail for reasons that are NOT
// divergences — a table lookup for an operation it does not encode, a source
// value of the wrong type — and dressing one of those as an unsupported
// capability would tell a user to upgrade their server over a client bug.
//
// A WRITE ROLE calls this at its own exported method's single defer (never
// at the raw refuse() call inside a nested encoder helper), so a refusal that
// a deeper helper already decorated with its own "items[%d]" or "Issue.%s"
// context arrives here as err's own composite message — preserved in detail
// — rather than as the bare *encode.RefusedError the ledger row alone would
// render. This is how a write-side refusal comes to satisfy
// errors.As(*issueops.ErrUnsupported) exactly as a read-side one already
// does, without rewriting every encoder that builds one.
func (s *Store) inexpressible(op string, err error) error {
	var refused *encode.RefusedError
	if !errors.As(err, &refused) {
		return err
	}
	unsup, ok := s.unsupported(op).(*ErrHTTPUnsupported)
	if !ok {
		return err
	}
	detail := err.Error()
	if detail == refused.Error() {
		detail = ""
	}
	return &InexpressibleError{Unsup: unsup, Refused: refused, detail: detail}
}
