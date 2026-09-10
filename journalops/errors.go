package journalops

import "fmt"

// TruncatedCode is the stable machine-readable code a consumer matches on when
// its checkpoint has fallen below the retained journal window.
//
// It is the wire spelling of *TruncatedError, for the front doors that cannot
// hand back a Go type: the HTTP problem document carries it, and a client that
// speaks JSON dispatches on this string exactly as an in-process caller
// dispatches on errors.As. Both name one condition, which is why the string
// lives beside the error rather than in a handler.
const TruncatedCode = "events_journal_truncated"

// TruncatedError reports that a sequential read cannot resume from the caller's
// checkpoint because the records it needs next were pruned.
//
// Without it, `WHERE seq > since` cannot distinguish "nothing new" from "your
// prefix is gone": a consumer resuming past a prune would either see an empty
// success and stall forever, or silently skip to the current floor and lose
// every record in between. Both are silent data loss, so the read fails loudly
// instead and hands back the window it can actually serve.
//
// Floor is the lowest seq still retained, or Head+1 when the journal holds no
// rows at all. Head is the highest seq the counter has ever assigned; it never
// decreases under a prune, so Floor > Head means "fully pruned, caught up to
// Head". A consumer that receives this must decide explicitly — resume from
// Floor-1 and accept the gap, or rebuild from scratch — and the engine does not
// decide for it.
//
// It is returned as a POINTER, which is what errors.As matches against, and
// implementations return it rather than wrapping it in a message of their own
// making. A caller reads the FIELDS; the string below is for humans.
type TruncatedError struct {
	// Since is the checkpoint the reported window begins after, which is the
	// caller's own checkpoint in every case except one.
	//
	// When the rows the read can serve start above the caller's checkpoint —
	// the ordinary "your prefix was pruned" case — Since IS that checkpoint and
	// Floor is the first row still retained.
	//
	// When the prefix is intact but the retained window has an interior hole
	// (a restored or hand-edited table; bd's own prune cannot produce one),
	// Since is instead the last seq the engine could serve contiguously from
	// the caller's checkpoint, and Floor is where the next intact island
	// starts. A batch with BOTH shapes reports the prefix one; the interior
	// hole is reported on the next read, once the caller has resumed past the
	// first. Every gap is surfaced, one resume at a time.
	//
	// Since therefore never reports a value BELOW what the caller presented, so
	// echoing it back can never make a consumer re-read records it already has.
	Since int64
	// Floor is the lowest retained seq (Head+1 when nothing is retained).
	Floor int64
	// Head is the highest seq ever assigned.
	Head int64
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf(
		"events journal truncated: checkpoint %d is below the retained window [%d..%d]; records %d..%d were pruned",
		e.Since, e.Floor, e.Head, e.Since+1, e.Floor-1)
}
