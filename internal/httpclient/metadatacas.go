// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/metadatacas.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// httpMetadataCAS serves issueops.MetadataCAS from the compareAndSetMetadata
// custom method — the conditional single-key write every coordination protocol
// over the metadata plane is built from.
//
// The MAPPING IS TOTAL: all five members of the role's request have a wire
// member or a path, and both members of its result are published, so nothing
// here refuses on shape and the ledger carries no W- row for this operation.
// What made the port a decision rather than a transcription is the two places
// this operation's shape is unlike every other write on this seam.
//
// A LOST RACE IS A 200. Every other write learns a refusal from a status code;
// here `swapped: false` with the value that refused the swap is the ANSWER, and
// the role says the same thing in the same words — "a mismatch is an answer,
// not a failure". So the verdict travels through the success path and nothing
// in the problem mapper knows about it. There is no 409 to classify, and a
// client that dispatched on the status code would report a lost race as a won
// one.
//
// ABSENCE IS A VALUE, on all three of `expected`, `value` and `current`. An
// omitted member means the key is ABSENT on that side of the transition — which
// is what makes a first-writer-wins acquire (nil → value) and a release
// (value → nil) expressible — while a member present holding `null` means the
// key exists and holds null. The two states are different requests and
// different answers, so nothing on this path may collapse one into the other:
// the request members are pointers on the role's side and `omitempty` raw
// messages on the wire's, and the response's `current` is a BARE
// json.RawMessage rather than a pointer precisely so a present null survives
// the decode (see the server's metadataCASWireValue).
//
// WHAT IS NOT DECIDED HERE, deliberately: the metadata-key syntax, the
// canonical equality rule, and which transitions write. All three are the
// ROLE's, the server routes this request through the same issueops.MetadataCAS
// a local workspace uses, and a second copy of any of them in this client would
// be a second definition. They come back as issueops.ErrValidation, which is
// what the role's contract promises.
type httpMetadataCAS struct {
	store *Store
	wire  WriteWire
}

var _ issueops.MetadataCAS = (*httpMetadataCAS)(nil)

// CompareAndSetKey dials POST /v0/beads/issues/{id}:casMetadata.
func (c *httpMetadataCAS) CompareAndSetKey(ctx context.Context, req issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	if err := requireActor(req.Actor); err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	// The KEY's emptiness is checked here and its SYNTAX is not, and the split
	// is the same one requireID makes: an empty key would be sent as a body
	// member the server refuses by name, which is a round trip to be told what
	// the role's own contract already calls invalid, while the syntax is the
	// workspace's rule and belongs to the role that owns the metadata plane.
	if strings.TrimSpace(req.Key) == "" {
		return issueops.CompareAndSetKeyResult{}, invalid("metadata key is required")
	}

	expected, err := casValue("Expected", req.Expected)
	if err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	value, err := casValue("Value", req.Value)
	if err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}

	res, err := c.wire.CompareAndSetMetadata(ctx, req.IssueID, apigen.CompareAndSetMetadataRequest{
		Actor: req.Actor, Key: req.Key, Expected: expected, Value: value,
	})
	if err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	return issueops.CompareAndSetKeyResult{Swapped: res.Swapped, Current: casCurrent(res.Current)}, nil
}

// casValue projects one side of the transition onto its wire member.
//
// NIL STAYS ABSENT, which is the whole of the mapping and the reason it is a
// function: the role spells "the key is absent on this side" as a nil pointer
// and the wire spells it as an omitted member, and `omitempty` omits exactly
// the empty raw message. A present value is COPIED rather than aliased — the
// role promises implementations never write through a caller's request, and
// handing these bytes to a marshaler is the kind of borrow that becomes a write
// when a helper is added.
//
// An EMPTY value is refused rather than sent. It is not a JSON value at all, so
// `omitempty` would silently turn it into the absent member — which is the
// opposite request — and the role calls it ErrValidation too.
func casValue(member string, value *json.RawMessage) (apigen.MetadataValue, error) {
	if value == nil {
		return nil, nil
	}
	if err := requireJSON(member, *value); err != nil {
		return nil, err
	}
	return apigen.MetadataValue(append([]byte(nil), (*value)...)), nil
}

// casCurrent projects the answer's current value back onto the role's optional
// one. nil stays nil, so an absent member is an ABSENT key rather than a null
// one — the same distinction the request side makes, answered the same way.
//
// The bytes are COPIED for the reason a caller needs them to be: Current is fed
// back as the next Expected, and a window onto a decoded response body is not
// something a retry loop should be holding between requests.
func casCurrent(current apigen.MetadataValue) *json.RawMessage {
	if current == nil {
		return nil
	}
	value := json.RawMessage(append([]byte(nil), current...))
	return &value
}
