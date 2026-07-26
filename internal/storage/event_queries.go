package storage

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// EventCursor is a keyset position in the durable events stream, ordered by
// (created_at, id). The zero value (zero time, empty id) means "from the
// beginning".
type EventCursor struct {
	CreatedAt time.Time
	ID        string
}

// EventQueryStore provides keyset paging over the durable event log, beyond
// the base Storage interface's time-only GetAllEventsSince. Callers that need
// it type-assert to this interface.
type EventQueryStore interface {
	// EventsSince returns durable events strictly after cursor, ordered by
	// (created_at ASC, id ASC) and bounded by limit (0 = a store default,
	// capped). It reads the durable events table only — wisp events are not
	// included — so a change feed built on it stays durable-only.
	//
	// issueID scopes the feed to a single bead's history ("" = all issues),
	// serving a per-bead drawer read off the same keyset primitive. The scan
	// seeks idx_events_created_at on the created_at lower bound; with issueID
	// set the planner may instead seek idx_events_issue and filter on the
	// cursor (a composite (issue_id, created_at, id) index does not exist).
	//
	// COMMIT-VISIBILITY-LAG CAVEAT: created_at is the event's logical timestamp,
	// but on a version-controlled backend a committed row can become visible to
	// readers slightly after its created_at. A feed that resumes from the last
	// row's cursor must therefore re-scan a slack window behind it (a resuming
	// consumer should overlap ~45s) and de-duplicate by id, or it can skip an
	// event whose created_at preceded the cursor but which committed after the
	// previous read.
	EventsSince(ctx context.Context, cursor EventCursor, issueID string, limit int) ([]*types.Event, error)
}
