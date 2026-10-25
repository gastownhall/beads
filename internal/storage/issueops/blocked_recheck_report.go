package issueops

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// blockedRecheckDropped counts post-commit rechecks that failed. It is
// registered on the storage meter every write runner reports through, against
// the global delegating provider, so it forwards once telemetry.Init() runs.
var blockedRecheckDropped metric.Int64Counter

func init() {
	blockedRecheckDropped, _ = otel.Meter("github.com/steveyegge/beads/storage/dolt").Int64Counter("bd.db.blocked_recheck_dropped",
		metric.WithDescription("Post-commit blocked-state rechecks abandoned; the write landed but dependents may carry a stale is_blocked flag until `bd recompute-blocked`"),
		metric.WithUnit("{recheck}"),
	)
}

// ReportBlockedRecheckFailure is the one channel a write runner reports a
// failed post-commit recheck through, instead of returning it: the write it
// followed is committed and durable, and every caller of a store write reads
// an error as "the mutation did not land", so surfacing this one would make
// automated callers retry and double-apply. What is left behind is the stale
// is_blocked flag `bd doctor` and `bd recompute-blocked` repair.
//
// Because the failure stops here, the counter and the line are its only
// trace. The counter is what a fleet alerts on; the line names the rows to
// repair.
func ReportBlockedRecheckFailure(ctx context.Context, pending BlockedRecheck, err error) {
	if err == nil {
		return
	}
	blockedRecheckDropped.Add(ctx, 1)
	log.Printf("warning: %s", BlockedRecheckFailureMessage(pending, err))
}
