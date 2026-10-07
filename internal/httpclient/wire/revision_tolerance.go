// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"bytes"
	"encoding/json"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// revisionKey is the one JSON member this file ever rewrites. It never touches
// `expected_version`: that member is request-side only (the client sends it,
// never decodes it), so the response-decode gap this file closes does not
// reach it.
const revisionKey = "revision"

// itemsKey is ApplyBatchResponse's one array of nested revision-bearing
// objects (apigen.ApplyItemResult). No other covered type below carries a
// nested object with a `revision` of its own: every other member here is a
// plain, top-level one, and tolerateLegacyRevisionNumbers rewrites nothing
// that is not reached through exactly this key or the top level.
const itemsKey = "items"

// revisionBearingResponse reports whether out is one of the apigen response
// types carrying the optimistic-concurrency `revision` member — the member
// this client always decodes as a JSON string (the post-#6053 decimal-token
// shape, upstream's own fix for the fact that the token spans the full 64-bit
// range and a JSON number would round past 2^53). A server that predates
// #6053 answers the same member as a bare JSON integer under the same
// `api_version: "v0"`, and the handshake has no signal for that move — a
// pre-#6053 server's `ContextResponse` omits `wire_revision` exactly the way
// every other old server does, so the one place left to tolerate the shape is
// here, at the reader, on the small, explicit set of types that carry it.
//
// The set is exactly the schemas internal/httpapi/wireshape's digest records
// with a `revision` member, less the ones revision_tolerance_test.go's
// revisionSchemaExempt documents as unreachable by a legacy server
// (TestRevisionBearingResponseCoversEveryWireShapeSchemaWithARevisionMember
// pins this against the same golden.json TestWireShapeDigest guards, so a new
// revision-bearing schema cannot slip past both without the coverage test
// failing first):
//
//   - apigen.ApplyBatchResponse: no top-level `revision` of its own, but each
//     of its `items` is an apigen.ApplyItemResult, which does carry one — see
//     tolerateLegacyRevisionNumbers' items handling below.
//   - apigen.CloseIssueResponse, apigen.ReleaseIssueResponse,
//     apigen.ReopenIssueResponse, apigen.UpdateIssueResponse: a top-level
//     `revision` each.
//   - apigen.IssueDetails (an alias for types.IssueDetails: GET
//     /v0/beads/issues/{id}'s body): also a top-level `revision`. getIssue is
//     a BASELINE operation (dispatched before any post-baseline call would
//     have forced the handshake that populates serverPredatesRevisionStrings'
//     cache), which is why that gate treats "no cached handshake at all" as
//     "tolerate" rather than "skip" — see its doc comment.
//
// Keeping the set explicit (rather than walking every response body looking
// for a stray "revision" key) means a type added later that happens to reuse
// the word for something else — a schema version, a cache generation — is not
// silently coerced; a new revision-bearing response type must be added here
// deliberately, the same discipline problem.go's legacyRevisionFields already
// keeps on the error path.
func revisionBearingResponse(out any) bool {
	switch out.(type) {
	case *apigen.ApplyBatchResponse,
		*apigen.CloseIssueResponse,
		*apigen.ReleaseIssueResponse,
		*apigen.ReopenIssueResponse,
		*apigen.UpdateIssueResponse,
		*apigen.IssueDetails:
		return true
	default:
		return false
	}
}

// serverPredatesRevisionStrings reports whether the cached handshake saw a
// server old enough that its `revision`/`expected_version` tokens may still
// be bare JSON integers: one that omitted `ContextResponse.wire_revision`
// entirely, which ClientMinWireRevision's doc pins as meaning exactly that (0
// and 1 are permanently retired values no server implementing the field will
// ever legitimately send).
//
// No cached handshake at all answers true too (review follow-up: it used to
// answer false, on the reasoning that every revision-bearing response type
// belongs to a non-baseline write, so the handshake forced ahead of it would
// already be cached — true for every one of them EXCEPT apigen.IssueDetails,
// whose operation, getIssue, is itself a baseline op that can dispatch with no
// handshake ever cached). Answering true unconditionally here is safe exactly
// because tolerateLegacyRevisionNumbers' rewrite is scoped to the few
// documented positions a legacy integer can actually appear at (see its doc
// comment) rather than a generic walk: running it against a modern server's
// already-string-shaped response, or with no handshake evidence either way,
// costs one structural no-op walk and touches nothing.
func (c *Client) serverPredatesRevisionStrings() bool {
	c.handshake.mu.Lock()
	defer c.handshake.mu.Unlock()
	return c.handshake.snap == nil || c.handshake.snap.Context.WireRevision == 0
}

// tolerateLegacyRevisionNumbers rewrites a bare-JSON-number value held at
// body's top-level "revision" key, and — only when out is
// *apigen.ApplyBatchResponse — at the "revision" key of each element of its
// top-level "items" array, into that number's decimal-string spelling: the
// shape every apigen response type above declares the member as. It returns
// body unchanged (the same slice) when nothing needed rewriting, so a modern
// server's already-string-shaped response pays one no-op decode and nothing
// else.
//
// IT NEVER RECURSES BEYOND THOSE TWO DOCUMENTED POSITIONS (review HIGH:
// data corruption). An earlier version of this file walked every nested
// object and array looking for a stray "revision" key, which reached
// CloseIssueResponse.issue.metadata and every other caller-owned JSON blob
// this client passes through opaquely — a user's own metadata shaped like
// `{"revision":3}` came back with that value silently turned into a string,
// and serverPredatesRevisionStrings' gate (WireRevision == 0, or now no
// handshake at all) covers every legacy AND unhandshaked request, not just
// the narrow pre-#6053 population the rewrite exists for. Scoping the rewrite
// to exactly the documented wire positions — never descending into `issue`,
// `metadata`, or any other nested object the response carries — is what makes
// answering true unconditionally above safe: there is no longer a nested
// object this function would touch by accident.
func tolerateLegacyRevisionNumbers(body []byte, out any) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	changed := false
	if v, ok := obj[revisionKey]; ok && isBareJSONNumber(v) {
		obj[revisionKey] = quoteJSONNumber(v)
		changed = true
	}
	if _, ok := out.(*apigen.ApplyBatchResponse); ok {
		if rewritten, ok := rewriteItemRevisions(obj[itemsKey]); ok {
			obj[itemsKey] = rewritten
			changed = true
		}
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return rewritten
}

// rewriteItemRevisions rewrites the top-level "revision" key of each element
// of items (ApplyBatchResponse.items, each an apigen.ApplyItemResult) that
// holds a bare JSON number. It reports ok == false when items is absent,
// malformed, or needed no rewriting at all, in which case the caller must
// leave the original bytes alone.
//
// Like tolerateLegacyRevisionNumbers itself, this never looks past each
// item's own top-level members: ApplyItemResult carries no nested object of
// its own a legacy integer could hide inside, but even if a future field
// added one, only "revision" at this exact level is ever a candidate.
func rewriteItemRevisions(raw json.RawMessage) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	changed := false
	for _, item := range items {
		if v, ok := item[revisionKey]; ok && isBareJSONNumber(v) {
			item[revisionKey] = quoteJSONNumber(v)
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	rewritten, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}

// isBareJSONNumber reports whether val's first non-space byte starts a JSON
// number rather than a string, object, array, boolean or null.
func isBareJSONNumber(val json.RawMessage) bool {
	trimmed := bytes.TrimSpace(val)
	if len(trimmed) == 0 {
		return false
	}
	c := trimmed[0]
	return c == '-' || (c >= '0' && c <= '9')
}

// quoteJSONNumber renders a raw JSON number token as a quoted JSON string
// holding its exact decimal digits — byte-for-byte, never round-tripped
// through an int64 or float64, so a token outside either range still survives
// unchanged.
func quoteJSONNumber(val json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(val)
	quoted, err := json.Marshal(string(trimmed))
	if err != nil {
		// trimmed is already a well-formed JSON number token (isBareJSONNumber's
		// caller only reaches here on that path), so a plain string of its bytes
		// can never fail to marshal.
		return val
	}
	return quoted
}
