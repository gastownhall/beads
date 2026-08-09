package issueops

import "context"

// ReadyCountResult is the size of a ready set.
type ReadyCountResult struct {
	// Total is how many rows the request's ready predicate admits. It is an
	// int64 for the reason CountResult.Total is one — the counting seam counts
	// in one — and not because a ready front is expected to be large.
	Total int64
}

// ReadyCounter describes sizing the ready set: the question `bd ready`'s
// pagination answers when it reports "showing 100 of 412", and — like Reader,
// ReadyClaimer and Counter — a role with its own accessor. A new capability
// gets a new role interface and its own accessor; never append a method here.
//
// IT IS A DIFFERENT QUESTION FROM BOTH OF ITS NEIGHBORS, which is why it is
// neither a Reader method nor a Counter one.
//
//   - Reader.Ready answers with a PAGE, and the verdict it carries is
//     HasMore: "did the limit hide anything". This role answers "how many",
//     which is strictly larger — no page can be truncated into it — and it is
//     the number one surface has published for years, so collapsing the two
//     would change shipped output rather than tidy a duplicate.
//   - Counter answers a predicate over one table. The ready predicate is
//     BLOCKER-AWARE: it reads the dependency graph and the wisp tier, and no
//     CountRequest can describe it. Counter's own doc says so in as many words
//     ("The count of READY work is likewise a different question ... and is
//     its own role for the same reason").
//
// IT TAKES ReadyRequest VERBATIM — the same type Reader.Ready takes and the
// same type ClaimNextRequest.Filter carries, not a parallel one shaped like
// it. A listing, a claim and a count then ask ONE question of ONE set, and the
// three cannot drift apart at the predicate: every implementation builds the
// filter through internal/workapi.BuildReadyCountFilter, which is
// BuildReadyFilter with the page removed.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply normalization only to attempt-local
// clones. That promise is load-bearing rather than conventional here:
// ReadyRequest carries four label slices and a metadata map, and normalization
// is the step that would otherwise write through them.
//
// COUNTING IS A READ. Nothing here records a history entry, fires a completion
// hook or changes a row, and a refusal changes nothing either. Deterministic
// request-validation failures match ErrValidation; result values are
// unspecified when error is non-nil.
//
// WHAT COST THIS HAS IS NOT PROMISED, only what it answers. The store-backed
// implementation sizes the set with indexed COUNT(*)s over the ready
// predicate; the unit-of-work implementation has no such seam and materializes
// the unbounded ready page to count it. Both answer the same number for the
// same request — that IS promised, by CountReady — and a caller that must
// bound the work asks a narrower question rather than a bounded one, because
// there is no bounded count to ask for (see CountReady's refusals).
type ReadyCounter interface {
	// CountReady returns how many issues the request's ready predicate admits.
	//
	// THE ANSWER IS AN IDENTITY, not a similar number:
	//
	//	CountReady(r).Total == len(Reader.Ready(r with Limit=0).Items)
	//
	// for every request r this method accepts, at every implementation. That
	// is the whole contract of this role — a total the page it sizes does not
	// agree with is worse than no total at all, because it is read as "how
	// much work is left" — and it is why the two surfaces share a request type
	// rather than merely similar filters. It holds for the wisp tier too: a
	// request that admits ephemeral rows counts them exactly as the listing
	// lists them.
	//
	// LIMIT AND OFFSET MUST BE UNSET (ErrValidation), mirroring
	// ClaimNextRequest for a reason of the same shape. A cardinality has no
	// page: a Limit would answer "how many of the first N", which no caller
	// wants and which the identity above would stop being true of; an Offset
	// would silently subtract the rows it skipped from the size of a set that
	// still contains them. Rejecting both says that out loud rather than
	// accepting two knobs and dropping them. An explicit zero Limit is refused
	// with the rest: Limit is a pointer so "unset" and "explicitly unlimited"
	// stay distinguishable (see ReadyRequest.Limit), and only the first is
	// what this request permits — an unlimited count is the only kind there
	// is, so asking for one is asking for the default.
	//
	// A PREDICATE THAT MATCHES NOTHING IS 0 AND A NIL ERROR. That is the whole
	// of the "not found" story here, as it is for Counter: an empty ready
	// front is the steady state of a drained queue, and a poller that had to
	// classify an error to read a zero would be pattern-matching prose.
	//
	// IT IS NOT ONE SNAPSHOT WITH A PAGE. A caller that lists a page and then
	// counts has issued two queries, and a write between them can leave the
	// total disagreeing with the page by that write. Nothing here is
	// transactional across the two calls, and no front door reconciles them:
	// `bd ready --json` publishes both and says only what each is.
	CountReady(ctx context.Context, req ReadyRequest) (ReadyCountResult, error)
}
