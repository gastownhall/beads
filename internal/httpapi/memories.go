package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/memoryops"
)

// The memory operations. Each one decodes its parameters, hands the whole
// request to the persistent-memory role, and shapes the answer onto the wire.
//
// WHAT IS NOT HERE, as in settings.go: no storage key is assembled, no key is
// derived from content, no search term is folded, no plane is filtered out of
// the config table this one shares. All of that is memoryops.Memories'
// implementation, which `bd remember` and `bd memories` reach through the same
// accessor — and the `kv.memory.` encoding in particular is deliberately
// invisible from here, because a second place that spelled the prefix would be
// a second place that could spell it wrong.
//
// THERE IS NO REDACTION ON THIS PLANE, and that is a decision rather than an
// omission. The settings surface withholds a value when the KEY NAME marks it
// credential-bearing; memory keys are derived from the content, so the same
// rule would withhold a memory about tokens and serve one that contains a token
// under an innocuous slug. Each operation's description states the exposure
// instead of making a promise this surface cannot keep.

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME, the same
// posture every other body-carrying operation here takes.
const (
	rememberContentMember = "content"
	rememberKeyMember     = "key"
)

// rememberMembers is the whole vocabulary, in one place, so the unknown-member
// refusal and the decoding below cannot come to disagree about what this
// operation accepts.
var rememberMembers = []string{
	rememberContentMember,
	rememberKeyMember,
}

// handleRememberMemory answers POST /v0/beads/memories.
func (s *Server) handleRememberMemory(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.rememberRequest(w, r)
	if !ok {
		return
	}

	memories, err := s.memories(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := memories.Remember(r.Context(), request)
	if err != nil {
		s.failMemoryErr(w, r, err)
		return
	}
	writeJSON(w, apigen.RememberedMemory{
		Key:      result.Key,
		Value:    result.Value,
		Replaced: result.Replaced,
	})
}

// rememberRequest decodes the body into the role's request, member by member,
// so that every refusal can NAME the member it is about.
//
// It validates the SHAPE and nothing else. Whether the content is empty, and
// whether a key can be derived from it, are the role's two refusals — routing
// them through the role is what keeps one definition of what a memory is, and
// it is what makes `bd remember` and this endpoint refuse the same inputs with
// the same sentences.
func (s *Server) rememberRequest(w http.ResponseWriter, r *http.Request) (memoryops.RememberRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return memoryops.RememberRequest{}, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(rememberMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// One offender, chosen deterministically so a client dispatching on
		// `param` never sees it depend on map order.
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries "+rememberMemberList()+" and nothing else"))
		return memoryops.RememberRequest{}, false
	}

	var request memoryops.RememberRequest

	raw, ok := members[rememberContentMember]
	if !ok {
		s.fail(w, r, InvalidArgument(rememberContentMember, ReasonInvalidValue,
			"`"+rememberContentMember+"` is required"))
		return memoryops.RememberRequest{}, false
	}
	// Through a POINTER, so that `null` reaches the type-mismatch branch rather
	// than unmarshaling as a no-op and being reported downstream as empty
	// content — the right refusal attached to prose that misdescribes what the
	// client sent.
	var content *string
	if err := json.Unmarshal(raw, &content); err != nil || content == nil {
		s.fail(w, r, InvalidArgument(rememberContentMember, ReasonInvalidValue,
			"`"+rememberContentMember+"` must be a string"))
		return memoryops.RememberRequest{}, false
	}
	request.Content = *content

	if raw, ok := members[rememberKeyMember]; ok {
		var key *string
		if err := json.Unmarshal(raw, &key); err != nil || key == nil {
			s.fail(w, r, InvalidArgument(rememberKeyMember, ReasonInvalidValue,
				"`"+rememberKeyMember+"` must be a string"))
			return memoryops.RememberRequest{}, false
		}
		// VERBATIM, deliberately: the role stores the bytes it is given, and a
		// trim here would put a memory under a key the client cannot name. An
		// absent member is the empty string, which is what tells the role to
		// derive one.
		request.Key = *key
	}

	return request, true
}

func rememberMemberList() string {
	quoted := make([]string, len(rememberMembers))
	for i, name := range rememberMembers {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

// failMemoryErr answers a failed memory operation.
//
// It draws the ErrValidation-is-a-400 line the sweep, delete, tree, edges and
// batch-create handlers each draw in their own handler, deliberately in the
// same shape: this role performs request validation the handler does not
// duplicate, and widening ClassifyError instead would change what every other
// operation returns for an error it has never produced.
//
// memoryops.ErrValidation is an ALIAS of issueops.ErrValidation rather than a
// second sentinel, so this match is the same identity every other handler here
// tests. Naming it through the memory package is what lets this file classify a
// refusal from the role it actually calls.
func (s *Server) failMemoryErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, memoryops.ErrValidation) {
		// No `param`: the role's two refusals are about the request as a whole
		// — content that cannot be remembered, and content no key can be
		// derived from — and the second one's recovery is a member the client
		// did not send. The detail carries the role's own sentence, which names
		// what to send instead.
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue, err.Error()))
		return
	}
	s.failErr(w, r, err)
}
