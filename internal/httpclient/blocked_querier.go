package httpclient

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// IsBlockedBatch serves beads.BlockedQuerier's batched read — the stored,
// transitive is_blocked flag per id, an id naming no row absent — through the
// one library entry, issueops.BlockedFlags, over this store's
// BlockingAnnotator (listBlockingAnnotations with `is_blocked`, at most
// issueops.BlockedFlagsChunk ids per request).
//
// Against a server that does not advertise dependencies.blocking.isBlocked
// the annotator refuses before dialing with *ErrUnsupported naming the token,
// so a caller's errors.As on the typed refusal (gc's ready projection latches
// its degrade on it) sees the same sentinel the generated stub answered with
// before, now with the capability that would lift it.
func (s *Store) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	return issueops.BlockedFlags(ctx, httpBlockingAnnotator{store: s}, ids)
}
