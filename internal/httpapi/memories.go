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

// memorySearchParam is the one query parameter this surface's memory plane
// takes, and it is deliberately NOT spelled `q`.
//
// `q` on GET /v0/beads/issues:query is a boolean expression over issue fields,
// with a vocabulary and a parse refusal; this is a substring match with
// neither. Spelling both `q` would let a client that assumed the other meaning
// send `status=open` and receive a literal search over memory text instead of
// an error — one surface answering two different questions to the same name.
// Under the second name that request is an unknown parameter, which is what it
// actually is.
const memorySearchParam = "search"

// handleListMemories answers GET /v0/beads/memories.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	// The one-parameter shape of the unknown-parameter rule. requireNoQuery is
	// for operations that take NONE; this one takes exactly one, so it goes
	// through the same query decoder every filtering read uses — which tracks
	// what was read and refuses what was not, so the allowlist is the parameter
	// table itself rather than a second copy of it that can drift.
	q := newQuery(r.URL.Query())
	// str, not list: a repeated `search` is refused rather than silently
	// resolved to one of its values, because a client that sent two terms asked
	// a question this operation cannot answer and must not be told it did.
	search := q.str(memorySearchParam)
	if !s.acceptQuery(w, r, q) {
		return
	}

	memories, err := s.memories(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	// The term goes in UNFOLDED and unparsed. Matching semantics are the role's
	// — case folding included — so that this surface and `bd memories` cannot
	// come to disagree about what a search means.
	result, err := memories.List(r.Context(), memoryops.ListRequest{Search: search})
	if err != nil {
		s.failMemoryErr(w, r, err)
		return
	}

	keys := make([]string, 0, len(result.Memories))
	for key := range result.Memories {
		keys = append(keys, key)
	}
	// Ordered by key, which is what makes the paginated envelope honest: the
	// order is stable across calls, so a keyset cursor over it is expressible
	// later without changing what a client already receives.
	slices.Sort(keys)

	items := make([]apigen.Memory, 0, len(keys))
	for _, key := range keys {
		items = append(items, apigen.Memory{Key: key, Value: result.Memories[key]})
	}
	writeJSON(w, apigen.MemoriesPage{Items: items, HasMore: false})
}

// handleGetMemory answers GET /v0/beads/memories/{key}.
func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	key, ok := s.memoryKey(w, r)
	if !ok {
		return
	}

	memories, err := s.memories(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := memories.Recall(r.Context(), memoryops.RecallRequest{Key: key})
	if err != nil {
		s.failMemoryErr(w, r, err)
		return
	}
	// A MISS IS A 404 HERE, and this is the one operation on this surface where
	// that diverges from its settings counterpart. The rule both follow is that
	// the status reports the distinctions the FRONT DOOR already reports, and
	// the two front doors differ: `bd config get` on an absent key prints
	// "(not set)" and exits 0, so getSetting has no 404 to give; `bd recall` on
	// a miss prints to stderr and exits 1 (printRecallResult in cmd/bd, via
	// SilentExit), and the role answers Found rather than a bare value, so the
	// status here reports a distinction that exists rather than inventing one.
	//
	// A row stored as the EMPTY STRING falls on the miss side of it, because
	// that is where the role puts it — the storage seam cannot tell it from an
	// absent row, and this handler will not claim to see what the role cannot.
	// listMemories enumerates such a row, which is the one way a client tells
	// the two apart.
	if !result.Found {
		s.fail(w, r, MemoryNotFound())
		return
	}
	writeJSON(w, apigen.Memory{Key: result.Key, Value: result.Value})
}

// handleForgetMemory answers DELETE /v0/beads/memories/{key} — the third
// DESTRUCTIVE operation on this surface, and the only one that is a DELETE.
//
// WHAT THIS HANDLER DOES NOT DO is the point, as for the sweep and the delete.
// It does not assemble a storage key, does not decide which rows belong to the
// memory plane, and does not read the value it reports: all of that is inside
// memoryops.Memories, whose implementation removes exactly the named row in the
// same transaction that reads it. The memory plane shares one table with the
// workspace's settings and with the generic `bd kv` namespace, so a delete
// written here would be one prefix-length mistake away from erasing a
// workspace's issue prefix — which is why the role's conformance contract owns
// that promise and this file does not restate it.
func (s *Server) handleForgetMemory(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	key, ok := s.memoryKey(w, r)
	if !ok {
		return
	}

	memories, err := s.memories(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := memories.Forget(r.Context(), memoryops.ForgetRequest{Key: key})
	if err != nil {
		s.failMemoryErr(w, r, err)
		return
	}
	// Found false means NOTHING WAS REMOVED, which is a 404 on the same terms
	// as the read beside it. It is a status rather than a 200 with an empty
	// body because a client that retried a forget has to be able to tell "I
	// removed this" from "there was nothing to remove", and the role already
	// answers the difference.
	if !result.Found {
		s.fail(w, r, MemoryNotFound())
		return
	}
	writeJSON(w, apigen.Memory{Key: result.Key, Value: result.Value})
}

// memoryKey validates the path parameter and reports whether the request may
// proceed. It is settingKey's rules, deliberately unchanged, so the two keyed
// planes refuse the same shapes with the same `param` and the same `reason`.
//
// A control character is refused rather than looked up, because a
// percent-escape in the path decodes to one and a key carrying a newline could
// only have come from a client assembling paths by concatenation. THE ROLE
// STAYS VERBATIM: `bd remember --key` accepts any string, so such a memory can
// exist, and it stays reachable from the CLI and from the collection read. It
// is unreachable by path, and the document says so rather than this refusal
// being discovered.
//
// The refusal is a 400 and not the 404 beside it: the 404 says this workspace
// holds no such memory, which is a claim about storage that nothing here has
// asked storage about.
func (s *Server) memoryKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	switch {
	case strings.TrimSpace(key) == "":
		s.fail(w, r, InvalidArgument("key", ReasonInvalidValue, "`key` is empty after trimming"))
		return "", false
	case strings.ContainsFunc(key, isControlChar):
		requestInfo(r.Context()).refuse(key)
		s.fail(w, r, InvalidArgument("key", ReasonInvalidValue, "`key` must not contain control characters"))
		return "", false
	}
	return key, true
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
