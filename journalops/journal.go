package journalops

import "context"

// Row is one raw record of the durable mutation journal.
//
// IssueJSON is empty when the op is a delete (no surviving row to snapshot);
// DepJSON is empty for non-dependency ops; CommentJSON is empty for non-comment
// ops. TS is the insert-time timestamp, stamped inside the committing
// transaction and normalized to a string.
//
// TS IS NOT MONOTONE IN SEQ, and a consumer that sorts by it is wrong. It is
// client-stamped, so two writers against one SQL server — or one writer whose
// clock steps back — can commit seq N with a later TS than seq N+1. Seq is the
// order; TS is a label. The retention floors resolve the same way, to a seq
// bound rather than a per-row age test, for exactly this reason.
//
// Actor is the acting identity that performed the mutation, as resolved for
// the audit-events table; empty when the mutation path has no actor (derived
// maintenance, actorless delete plumbing, and rows written before the column
// existed).
type Row struct {
	Seq         int64
	TS          string
	Op          string
	IssueID     string
	Actor       string
	IssueJSON   string
	DepJSON     string
	CommentJSON string
}

// Page is one journal read that also reports how far behind the caller is: the
// rows it asked for, and the head of the journal's history at the moment they
// were read.
//
// The two travel together because they are only meaningful together. Rows alone
// cannot answer "poll again now, or wait?" — a full page might be the end of the
// journal or the first thousand of a hundred thousand — and a head read
// separately could be taken from a different instant and land BELOW the last row
// served, which a consumer reads as "past the end" and stalls on.
//
// Head is the highest seq the counter has ever assigned. It never decreases
// under a prune, so it is the journal's HISTORY rather than its CONTENTS: a
// fully pruned journal reports its rows gone and its head unchanged. Deriving
// it from the rows in hand would make it a fact about the page instead, which
// reads as "caught up" at the end of every bounded read.
type Page struct {
	Rows []Row
	Head int64
}

// Journal is the workspace's durable mutation journal, read as pages from a
// caller-held checkpoint — see the package doc for what that plane is and why
// its retention and activation are not on this role.
//
// IT IS ONE ROLE WITH ONE METHOD, and being born with one is the point rather
// than an accident of what shipped first. The plane's other verbs are operator
// decisions the read holder is deliberately not entitled to, so there is no
// second shape of this question waiting to be appended: `bd events prune` and
// the per-instance activation switch answer to storage.EventsJournalAccessor
// and storage.EventsJournalConfigurer, which the workspace holds and a
// publishing surface does not.
//
// The read/write entitlement test the role rule applies (can one caller be
// entitled to the read and not the write?) comes back YES here, loudly, which
// is why the split exists at all: `bd serve` publishes GET /v0/beads/events and
// documents that it never retains, so handing it a delete would make that
// documentation the only thing standing between a consumer's checkpoint and a
// prune.
//
// Result values are unspecified when error is non-nil — in particular a
// truncation carries its window in the ERROR and not in a partial Page, so a
// caller that ignores the error and reads Rows gets nothing rather than a
// plausible-looking suffix. Implementations never mutate caller-owned values,
// having none to mutate.
type Journal interface {
	// ReadEventsJournalPage returns the journal records after the caller's
	// checkpoint, with the head of the journal's history.
	//
	// SINCE IS EXCLUSIVE. The answer is the records whose seq is strictly
	// greater than since, so a consumer stores the seq it last processed and
	// hands back exactly that, with no arithmetic and no risk of an off-by-one
	// replay. A since of 0 therefore means "from the beginning of history",
	// because seq starts at 1.
	//
	// ROWS ARE SEQ-ASCENDING, and gapless within one answer. Seq is drawn from
	// a counter inside the mutation's own transaction rather than assigned at
	// insert, so the order rows are served in is the order they COMMITTED in —
	// which is the order a replay has to apply them in, and is not what an
	// AUTO_INCREMENT would have given.
	//
	// LIMIT CAPS THE ROWS AND NOT THE HEAD. A limit of 0 means uncapped: this
	// role imposes no ceiling of its own, because the caller that pages a
	// hundred thousand records out to a file is as legitimate as the one
	// polling for ten. A FRONT DOOR may impose one — the HTTP handler behind
	// GET /v0/beads/events caps its page, and that cap is the HANDLER's promise
	// to its clients, stated on the operation — and a bounded page still
	// reports the journal's head, which is how the caller learns there is more.
	//
	// HEAD COMES FROM THE SAME TRANSACTION AS THE ROWS, and is read after them.
	// Reading it first would let a mutation commit in between and yield a head
	// BELOW the last row served; taking it from a separate transaction would
	// allow the same thing. This order can only report a head equal to or ahead
	// of the last row returned, which a poller acts on correctly either way.
	// Head never decreases across calls, prune included.
	//
	// A CHECKPOINT AT OR ABOVE HEAD IS CAUGHT UP: no rows, the head, and a nil
	// error. That is the answer a poller gets on a quiet workspace and the
	// answer it gets at the end of a fully pruned journal, and it is
	// deliberately the same answer, because both mean "there is nothing after
	// your checkpoint".
	//
	// A CHECKPOINT BELOW THE RETAINED WINDOW IS A TYPED FAILURE: the returned
	// error matches errors.As against *TruncatedError, carrying the window the
	// implementation can still serve. Resuming from that window's Floor-1
	// succeeds and yields the retained records; the caller decides between that
	// gap and a rebuild, and no implementation decides for it. See TruncatedError.
	ReadEventsJournalPage(ctx context.Context, since int64, limit int) (Page, error)
}
