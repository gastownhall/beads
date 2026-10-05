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
		*apigen.UpdateIssueResponse:
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
// ever legitimately send). No cached handshake at all — a baseline operation
// dispatched before any post-baseline call forced one — answers false: every
// response type this file tolerates belongs to a non-baseline write, so by
// the time one of them dispatches, Preflight has already forced the
// handshake that would have populated the cache.
func (c *Client) serverPredatesRevisionStrings() bool {
	c.handshake.mu.Lock()
	defer c.handshake.mu.Unlock()
	return c.handshake.snap != nil && c.handshake.snap.Context.WireRevision == 0
}

// tolerateLegacyRevisionNumbers rewrites every bare-JSON-number value held at
// a "revision" key, anywhere in body's object/array structure, into that
// number's decimal-string spelling — the shape every apigen response type
// above declares the member as. It returns body unchanged (the same slice) when
// nothing needed rewriting, so a modern server's already-string-shaped
// response pays one no-op structural walk and nothing else.
//
// It never touches a value that is already a string, null, or any other
// shape: only a bare number at exactly this key is a pre-#6053 server's
// doing, and those are the only inputs that fail json.Unmarshal otherwise.
func tolerateLegacyRevisionNumbers(body []byte) []byte {
	var raw json.RawMessage = body
	out, changed := rewriteLegacyRevisionKeys(raw)
	if !changed {
		return body
	}
	return out
}

// rewriteLegacyRevisionKeys walks one JSON value (object, array, or scalar)
// looking for a "revision" member whose value is a bare number, rewriting it
// to a quoted decimal string in place. Every other byte is round-tripped
// through json.RawMessage rather than decoded into an interface{}, so a
// sibling field holding an integer past float64's exact range is never at
// risk of the precision loss a generic decode would introduce — this file
// only ever interprets the bytes under the one key it rewrites.
func rewriteLegacyRevisionKeys(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return raw, false
		}
		changed := false
		for key, val := range obj {
			if key == revisionKey && isBareJSONNumber(val) {
				obj[key] = quoteJSONNumber(val)
				changed = true
				continue
			}
			if nv, ch := rewriteLegacyRevisionKeys(val); ch {
				obj[key] = nv
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return raw, false
		}
		return out, true
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return raw, false
		}
		changed := false
		for i, val := range arr {
			if nv, ch := rewriteLegacyRevisionKeys(val); ch {
				arr[i] = nv
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		out, err := json.Marshal(arr)
		if err != nil {
			return raw, false
		}
		return out, true
	default:
		return raw, false
	}
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
