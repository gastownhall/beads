package memoryops

import "context"

// RememberRequest stores one memory.
type RememberRequest struct {
	// Key names the memory to store under. EMPTY MEANS DERIVE IT from Content,
	// using the one canonical derivation (internal/memoryapi.DeriveKey — the
	// function that has minted every auto-generated memory key since the
	// command shipped). A request with no key is the NORMAL case, which is why
	// the role derives rather than making the caller learn the derivation.
	//
	// A non-empty Key is used VERBATIM: no trimming, no slugging, no charset
	// restriction. `bd remember --key` has always accepted any string, and a
	// stored key has to stay recallable under the exact bytes the caller used.
	Key string
	// Content is the memory itself.
	//
	// Empty after trimming is ErrValidation. So is content from which no key
	// can be derived when Key is empty — "!!!" derives to "" — because the
	// alternative is a row under a key nothing can name.
	//
	// Otherwise it is stored VERBATIM: newlines, surrounding space and unicode
	// all survive. Truncating it to one line is what a front door does when it
	// prints; it is not what this plane does when it stores.
	Content string
}

// RememberResult reports what landed.
type RememberResult struct {
	// Key is the key the memory now lives under: the request's, or the derived
	// one. A caller that supplied no key learns here what to recall.
	Key string
	// Value echoes the stored content, verbatim.
	Value string
	// Replaced reports whether a previous value existed under Key, OBSERVED IN
	// THE SAME TRANSACTION AS THE WRITE. It is what the CLI's "Remembered"
	// versus "Updated" verb is made of, and unlike the two-step read it
	// replaces it cannot be stale: no concurrent write can land between the
	// observation and the act it describes.
	//
	// A previous value that was the EMPTY STRING reports Replaced true. The ROW
	// existed, even though a Recall of that key would have answered Found
	// false. That divergence is the storage seam's conflation showing through
	// (see RecallResult.Found); it is stated here rather than smoothed over,
	// because smoothing it would mean this field reporting "nothing was there"
	// about a write that overwrote something.
	Replaced bool
}

// RecallRequest names one memory to read. An empty Key, or one that is empty
// after trimming, is ErrValidation: there is no row a caller could mean by it,
// and answering Found false would report "no such memory" for a question that
// was never asked.
type RecallRequest struct{ Key string }

// RecallResult is one memory's stored content.
type RecallResult struct {
	// Key echoes the request.
	Key string
	// Value is the stored content, or "" — and Found says how to read the "".
	Value string
	// Found is Value != "". A MEMORY STORED AS THE EMPTY STRING AND AN ABSENT
	// MEMORY ARE THE SAME ANSWER: Found false, nil error.
	//
	// No front door can create the first of those — Remember refuses empty
	// content — but an out-of-band config write can, and this role does not
	// invent a distinction the seam beneath it cannot see. List DOES enumerate
	// such a row, because its KEY exists, and that is the one way a caller can
	// tell the two apart: the same same-answer-different-row asymmetry the
	// settings contract pins for `bd config get`.
	Found bool
}

// ForgetRequest names one memory to remove. An empty Key is ErrValidation, for
// the reason RecallRequest's is.
type ForgetRequest struct{ Key string }

// ForgetResult reports the removal.
type ForgetResult struct {
	// Key echoes the request.
	Key string
	// Value is the content the removed memory held — what `bd forget` prints —
	// read in the SAME TRANSACTION as the delete, so it is what was actually
	// removed rather than what a previous read happened to see.
	Value string
	// Found false means nothing was stored under Key (or an empty string was:
	// the same conflation RecallResult.Found describes) and NOTHING WAS
	// DELETED.
	//
	// It is a result, not an error. The front doors own the not-found
	// presentation — the CLI has an exit-code contract for it — and a role that
	// returned an error here would force every one of them to classify a
	// refusal to learn a fact.
	Found bool
}

// ListRequest asks for the memory plane, optionally narrowed.
type ListRequest struct {
	// Search filters the answer: a memory matches when the lowercase of its key
	// or the lowercase of its value contains the lowercase of Search. "" means
	// everything.
	//
	// THE FOLDING IS THIS ROLE'S, not the caller's. A caller passes what the
	// user typed; it does not pre-lower the term. That is the difference
	// between a role that owns the matching semantics and one that documents
	// them and hopes.
	Search string
}

// ListResult is the memory plane, narrowed by the request's Search.
type ListResult struct {
	// Memories maps USER key (prefix stripped) to stored value, for every row
	// of the memory plane that matched — and ONLY the memory plane. Settings
	// rows, generic `bd kv` rows and every other row of the config table this
	// plane shares are never here, whatever they contain, INCLUDING a memory
	// whose key shadows a settings name: "kv.memory.issue_prefix" is a memory
	// called "issue_prefix" and the workspace's real prefix is not a memory.
	//
	// Empty map, never nil, so a caller can range over the answer without a
	// guard. No order and no page: callers that print sort the keys, as both
	// front doors do today.
	Memories map[string]string
}

// Memories describes the workspace's persistent memory plane — see the package
// doc for what that plane is and why it is not the settings plane.
//
// IT IS ONE PLANE, WHICH IS WHY IT IS ONE ROLE WITH FOUR METHODS. Store, read,
// remove and enumerate over a single keyed namespace are four shapes of one
// question, the same argument that made issueops.WorkspaceConfig one role with
// four. The promises are statements about each other — what Remember stores is
// what Recall answers and what Forget reports it removed — so splitting them
// would put one role's promise inside another role's result.
//
// The read/write entitlement test the role rule applies (can one caller be
// entitled to the read and not the write?) comes back NO here: bd's readonly
// mode is a front-door guard applied uniformly across every role, and no
// shipped path is forbidden to write memories the way a provisioned identity is
// forbidden to write a workspace identity. If a genuinely read-only memory
// consumer appears — a credential that may read prime context but not write it
// — that is the day to mint a MemoryReader. Being born whole is allowed;
// appending later is not, and splitting later is cheap because the four methods
// already have separate request types.
//
// PROBE AND ACT SHARE ONE TRANSACTION on every implementation. Remember's
// Replaced and Forget's Value are observations of the row the same transaction
// then writes or deletes, not the results of an earlier read. The shipped
// commands did it in two calls and papered over the gap by ignoring the first
// one's error; this role does not, which also means a database that cannot be
// read now FAILS the write rather than mis-reporting it.
//
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil. Implementations never mutate
// caller-owned request values.
type Memories interface {
	// Remember stores one memory, deriving its key from the content when the
	// request supplies none, and reports whether it replaced a previous value.
	//
	// THE REFUSALS, in the order they are applied:
	//
	//   - Content empty after trimming is ErrValidation. There is nothing to
	//     remember, and a row stored empty is a row Recall would then deny;
	//   - Content from which no key can be derived, when Key is empty, is
	//     ErrValidation. The caller's recourse is an explicit key, which the
	//     message says.
	//
	// A Remember of the value already stored still performs the write. Nothing
	// compares first: this plane converges on merge, so a write that decided it
	// was a no-op would be a write that declined to state a fact the merge
	// resolver reads.
	Remember(ctx context.Context, req RememberRequest) (RememberResult, error)

	// Recall returns one memory's content, or Found false.
	//
	// It is a READ: nothing here records a history entry, fires a completion
	// hook or changes a row. A key nothing stored is Found false with a nil
	// error, never ErrNotFound — see errors.go for why that is a decision and
	// not an omission.
	Recall(ctx context.Context, req RecallRequest) (RecallResult, error)

	// Forget removes one memory and reports what it held.
	//
	// Removing a key nothing stored is a SUCCESS with Found false, and it
	// deletes nothing — including nothing from the settings plane or the
	// generic kv namespace that share this table. Forgetting the same key twice
	// is the second call a retrying caller actually makes, and it answers the
	// same way.
	Forget(ctx context.Context, req ForgetRequest) (ForgetResult, error)

	// List returns the memory plane, narrowed by the request's Search.
	//
	// A read, on the same terms as Recall, and one that SUBSUMES the matching:
	// the caller hands over the search term the user typed and this role owns
	// what matching means. A miss answers an empty map, never nil and never an
	// error.
	List(ctx context.Context, req ListRequest) (ListResult, error)
}
