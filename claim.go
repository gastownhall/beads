package beads

import (
	"errors"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// ClaimConflict describes why a claim failed, recovered from the claim error.
//
// The engine's conditional-UPDATE claim path returns no typed conflict result:
// on ErrAlreadyClaimed it embeds the current assignee in the message
// ("issue already claimed by <assignee>"), and on ErrNotClaimable it embeds the
// current status ("issue not claimable: status <status>"). ClaimConflict
// carries whichever of those was recoverable; the other stays empty. This is a
// deliberately string-coupled shim — a typed conflict result would require
// changing the internal claim signature, which this surface deliberately avoids.
type ClaimConflict struct {
	// CurrentAssignee is the actor currently holding the issue. Set when the
	// error wraps ErrAlreadyClaimed and the assignee was parseable.
	CurrentAssignee string
	// CurrentStatus is the issue's status that made it unclaimable. Set when
	// the error wraps ErrNotClaimable and the status was parseable.
	CurrentStatus string
}

// alreadyClaimedMarker and notClaimableMarker are derived at package-init time
// from the storage layer's sentinel + format fragment, NOT hardcoded literals,
// so the producer (issueops/claim.go) and this parser cannot drift: both spell
// the conflict message as "<sentinel><fragment><token>". A change to either the
// sentinel text or the fragment moves both ends in lockstep, and the producer-
// tied round-trip test (claim_roundtrip_test.go / the dolt suite) is the
// tripwire if the producer stops using the fragment at all.
var (
	alreadyClaimedMarker = storage.ErrAlreadyClaimed.Error() + storage.ClaimedByFragment
	notClaimableMarker   = storage.ErrNotClaimable.Error() + storage.NotClaimableStatusFragment
)

// ParseClaimConflict inspects a claim error and, when it wraps ErrAlreadyClaimed
// or ErrNotClaimable, returns the recovered conflict detail and true. For any
// other error (including nil) it returns the zero ClaimConflict and false.
//
// Parsing keys on the message fragment the engine appends after the sentinel,
// located with LastIndex so that outer "context: %w" wrapping (which prepends)
// does not defeat it. Fields are best-effort: an Is-match with an unparseable
// message still returns true with the corresponding field empty.
func ParseClaimConflict(err error) (ClaimConflict, bool) {
	switch {
	case errors.Is(err, ErrAlreadyClaimed):
		return ClaimConflict{CurrentAssignee: tailAfter(err.Error(), alreadyClaimedMarker)}, true
	case errors.Is(err, ErrNotClaimable):
		return ClaimConflict{CurrentStatus: tailAfter(err.Error(), notClaimableMarker)}, true
	default:
		return ClaimConflict{}, false
	}
}

// tailAfter returns the bounded token following the last occurrence of marker in
// s, or "" when marker is absent or the token cannot be recovered unambiguously.
//
// The recovered token (assignee/status) is a single whitespace-free run by the
// producer's construction: the fragment is the last thing before the bare token,
// and claim-error wraps PREPEND context ("caller ctx: %w"), never append it, so
// in the well-formed case the tail is exactly the token. If the tail carries
// trailing content — whitespace, an appended "(...)" wrap, or a further embedded
// marker (all of which contain a space or a paren) — we cannot separate the
// token from the wrap, so we return "". A recovered-empty field is the
// documented best-effort failure mode (ParseClaimConflict still reports ok=true
// on the errors.Is match); a status never contains whitespace and an assignee is
// an actor id that by convention does not either, so a clean conflict is
// unaffected. This bound also stops a plain appended suffix — which the old
// "contains another full marker" guard missed — from leaking a garbage token.
func tailAfter(s, marker string) string {
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return ""
	}
	tail := s[i+len(marker):]
	if strings.ContainsAny(tail, " \t\r\n(") {
		return ""
	}
	return tail
}
