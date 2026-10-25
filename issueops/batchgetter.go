package issueops

import (
	"context"
	"fmt"
)

// MaxGetManyIDs is the largest request this role accepts in one call. It is a
// FLAT bound on the request, not a cost model over what a backend does with
// it: GetMany reads every named id in ONE snapshot, so the bound is really a
// bound on how long one request may hold a single read open.
//
// 1000 is the same number BatchApplier settled on (issueops.MaxApplyBatchItems)
// and the same number DeleteRequest.IDs caps at on the wire
// (openapi.v0.yaml's DeleteIssuesRequest). It is not borrowed for symmetry: a
// caller fanning out gc's ready veto or the CLI's own id-resolution passes
// already walks lists in exactly this range, and a cap an order of magnitude
// smaller would just reintroduce the per-id fan-out this role exists to
// replace.
const MaxGetManyIDs = 1000

// TooManyIDsError reports that a GetManyRequest named more ids than this role
// accepts in one call. It wraps ErrValidation, so a caller that only checks
// for a usable request classifies it correctly without knowing this type
// exists, and errors.As gets at Requested and Cap for a caller that wants to
// say how far over the line the request was.
type TooManyIDsError struct {
	// Requested is the number of ids the request named, counted BEFORE
	// deduplication: a caller that sent 1500 mentions of 3 distinct ids typed
	// a request this role still refuses, because the obligation this cap
	// bounds is reading the request apart, not reading the rows it resolves
	// to.
	Requested int
	// Cap is MaxGetManyIDs, carried on the error so a caller does not need the
	// constant in scope to report what it hit.
	Cap int
}

func (e *TooManyIDsError) Error() string {
	return fmt.Sprintf("get many accepts at most %d ids, got %d", e.Cap, e.Requested)
}

// Unwrap makes TooManyIDsError match ErrValidation.
func (e *TooManyIDsError) Unwrap() error { return ErrValidation }

// GetManyRequest names the issues to fetch in one read.
//
// Implementations never mutate caller-owned request values: IDs is read,
// never written through, and never sorted in place.
type GetManyRequest struct {
	// IDs names the issues to fetch, in either plane, exact ids only. Prefix
	// resolution and bd's other id conveniences happen at the front door,
	// above this role, the same boundary DeleteRequest.IDs draws.
	//
	// DUPLICATES COLLAPSE and the surviving order is the caller's first
	// mention of each id: a caller that asks for [a, b, a] gets back Issues
	// in the order [a, b] (whichever of those resolve) and nothing about a
	// twice.
	//
	// AN ID THAT NAMES NO STORED ROW IS NOT AN ERROR. It is reported in
	// GetManyResult.Missing beside whatever else resolved — the same
	// result-field answer EdgeCountResult.Anchors and EdgeReadResult give a
	// miss, and the reason GetMany does not return ErrNotFound the way
	// DeleteRequest's all-or-nothing id check does: a batch GET is a set
	// read, not a precondition on every member succeeding.
	//
	// MORE THAN MaxGetManyIDs IS A REFUSAL: a *TooManyIDsError, counted on
	// the request as sent, before deduplication.
	//
	// An empty slice is not an error. It answers with an empty result, the
	// same empty-request answer EdgeCountRequest.IDs documents, because "no
	// ids" is a legitimate thing to ask for 0 issues about and a caller that
	// built its list by filtering should not have that filter's empty result
	// turned into a different kind of failure than the one it already knows
	// how to handle.
	IDs []string
}

// GetManyResult answers a GetManyRequest from one snapshot.
type GetManyResult struct {
	// Issues holds the hydrated rows for every id that resolved, in the
	// REQUEST's order (the caller's first mention of each distinct id), never
	// the storage engine's natural order. Each Issue carries its current
	// RowVersion, so a caller that goes on to write one back has the
	// optimistic-concurrency token a lifecycle write wants without a second
	// round trip.
	//
	// HYDRATION IS LABELS ONLY: no dependencies, dependents or comments. A
	// caller that needs those calls Reader.Get instead, one id at a time.
	//
	// Never nil for a successful call, even when it is empty: a front door
	// that marshals this field emits [] rather than null, the same promise
	// EdgeCountResult.Anchors makes.
	Issues []*Issue
	// Missing names every requested id that resolved to no stored row, in the
	// same first-mention order as the request. An id that appears in Missing
	// does not appear in Issues, and the two slices' lengths sum to the
	// number of DISTINCT ids the request named.
	//
	// Never nil for a successful call: an all-found request answers with an
	// empty, non-nil Missing.
	Missing []string
}

// BatchGetter reads many issues by id in one snapshot.
//
// It exists because the obvious way to read N issues — one Get per id — is
// what gc's ready veto and the bd CLI both do today, and it costs N round
// trips (or N transactions, for a backend where each Get opens its own) to
// answer a single question a caller already knows the shape of: "which of
// these ids exist, and what do they look like right now." BatchGetter answers
// that in one call over one snapshot, the same trade EdgeCountRequest.IDs and
// EdgeReadRequest make for the edge-shaped versions of the same question.
//
// IT IS A FACADE-ONLY ROLE, for now: this slice wires every library leg
// (dolt, embedded dolt, the unit-of-work provider) and the
// POST /v0/beads/issues:batchGet HTTP handler, but ships no HTTP CLIENT leg.
// A caller on the wrong side of that door reaches this role through the raw
// HTTP request the handler answers, not through a typed client method, until
// the client generator lands (see the TODO beside the CLI call sites this
// slice did not migrate, and PR #7247). A later slice updates this doc and the
// client-side TODOs together once that leg exists.
//
// It differs from Reader.Get in arity, in hydration and in what a miss means:
// Get answers ONE id with *IssueDetails or ErrNotFound, labels, dependencies,
// dependents and comments included; this answers MANY ids with a result
// whose Missing field is how a caller tells "this one does not exist" from
// "the call failed," and whose Issues are hydrated LABELS ONLY — no
// dependencies, dependents or comments. A caller that needs those calls
// Reader.Get instead, one id at a time. It differs from EdgeReader and
// GraphCounter in what it is reading: those answer questions about the EDGES
// a set of anchors carries; this answers the ISSUES themselves.
type BatchGetter interface {
	// GetMany reads every id GetManyRequest.IDs names, in ONE snapshot. The
	// existence check and the hydration share that snapshot for the reason
	// ExecuteEdgeCount's probe-and-tally share one: a probe in its own
	// transaction could report an id missing while a second transaction
	// created it, contradicting itself in one response body.
	//
	// Refuses with:
	//   - *TooManyIDsError, wrapping ErrValidation, when IDs carries more than
	//     MaxGetManyIDs entries (counted before deduplication);
	//   - ErrValidation for an empty-string entry in IDs, the same per-entry
	//     check EdgeCountRequest.IDs applies.
	//
	// Never returns ErrNotFound: an id naming no stored row is reported in
	// GetManyResult.Missing, not refused.
	GetMany(ctx context.Context, request GetManyRequest) (GetManyResult, error)
}
