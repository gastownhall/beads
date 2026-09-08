package issueops

import "context"

// SettingKeyIssuePrefix is the workspace's id prefix, and the one key this
// role REFUSES to write. It is named here rather than spelled at each check so
// the refusal, the doc that describes it and the contract case that pins it all
// read the same string.
//
// The dashed spelling is refused too, and for a different reason than the
// underscored one: nothing reads it. `bd create` resolves the prefix from YAML
// "issue-prefix" and then from stored "issue_prefix", so a write of
// "issue-prefix" through this plane lands in a third place no reader consults
// — a write that reports success and can never be observed.
const SettingKeyIssuePrefix = "issue_prefix"

// SettingKeyStatusCustom and SettingKeyTypesCustom are the two keys whose
// values this plane does more with than store: each one BACKS a normalized
// lookup table that reads consult first, so writing one of them re-synchronizes
// its table. See WorkspaceConfig.SetSetting.
const (
	SettingKeyStatusCustom = "status.custom"
	SettingKeyTypesCustom  = "types.custom"
)

// GetSettingRequest names one stored setting.
type GetSettingRequest struct {
	// Key is the setting's name, used verbatim: there is no namespace
	// completion, no case folding and no dash/underscore equivalence. The one
	// place two spellings mean anything to this role is the refusal above, and
	// that is a refusal rather than a translation for exactly this reason.
	Key string
}

// SettingResult is one setting's stored value.
type SettingResult struct {
	// Key echoes the request, so a caller holding several results can tell
	// them apart without keeping the requests.
	Key string
	// Value is what the workspace stored, verbatim.
	//
	// AN UNSET KEY AND A KEY STORED AS THE EMPTY STRING ARE THE SAME ANSWER —
	// "" with a nil error — and this role does not distinguish them. There is
	// no ErrNotFound here: a question about a setting has an answer even when
	// nothing set it, and a caller polling for configuration would otherwise
	// have to classify an error to read "unconfigured".
	//
	// That conflation is the shipped behavior of both `bd config get` routes
	// and it is stated rather than quietly repaired, because repairing it means
	// a present/absent flag that every reader of this role would have to start
	// handling to learn nothing it does not already assume.
	Value string
}

// ListSettingsRequest asks for every stored setting. It carries no fields
// today and exists so that adding one — a prefix filter, a page — is an
// additive change to a type callers already name, rather than a signature
// change on the interface.
type ListSettingsRequest struct{}

// ListSettingsResult is the whole stored settings map.
type ListSettingsResult struct {
	// Settings maps each stored setting's key to its value. A workspace with
	// nothing stored yields an empty map, never nil, so a caller can range over
	// the result without a guard.
	//
	// Rows of the KV plane are not settings and are not here; see ListSettings.
	//
	// It is the DURABLE plane only: values that reach a running bd from
	// config.yaml, from the environment or from git config are not here and
	// cannot be, because this role reaches the workspace database and those
	// three live on the client's filesystem. `bd config show` is the
	// multi-source view and it is deliberately not on this role.
	Settings map[string]string
}

// SetSettingRequest stores one setting.
type SetSettingRequest struct {
	// Key must be non-empty after trimming and must not be a protected key.
	// It is stored verbatim — untrimmed — because a key differing from another
	// only by surrounding space is a key a reader will never match, and
	// silently trimming it would produce a write the caller cannot find again
	// under the name it used.
	Key string
	// Value is stored verbatim. Two keys' values are additionally PARSED, and
	// a value that does not parse is refused rather than stored: see
	// WorkspaceConfig.SetSetting.
	Value string
}

// SetSettingResult reports what landed.
type SetSettingResult struct {
	// Key and Value are what the workspace now holds.
	//
	// Value equals the request's value for every key this plane accepts, and
	// that is a promise rather than a coincidence: the one stored key with a
	// normalization step (issue_prefix, whose trailing hyphen is stripped) is
	// the key this role refuses, so no write through this door is transformed
	// on its way in. A caller may therefore treat a successful Set as
	// "the value I sent is the value stored" without re-reading it.
	Key   string
	Value string
}

// UnsetSettingRequest removes one setting.
type UnsetSettingRequest struct {
	// Key is the setting to remove. Removing a key nothing set is a success:
	// see WorkspaceConfig.UnsetSetting.
	Key string
}

// UnsetSettingResult reports the removal.
type UnsetSettingResult struct {
	// Key echoes the request. There is deliberately no "removed" flag: the
	// storage seam discards the affected-row count on every implementation, so
	// a flag here would be a value one of them had to invent.
	Key string
}

// WorkspaceConfig describes the workspace's DURABLE SETTINGS PLANE: the
// key-value rows stored in the workspace database, which is what `bd config
// get`, `bd config set`, `bd config unset` and `bd config list` read and write.
// Like Lifecycle, Reader, ReadyClaimer, BatchCloser, DependencyEditor,
// Commenter, Counter and Relations it is a role with its own accessor, and a
// new capability gets a new role interface; never append a method here.
//
// IT IS ONE PLANE, WHICH IS WHY IT IS ONE ROLE WITH FOUR METHODS. The
// governing rule forbids APPENDING to an existing role, not a role being born
// with every shape of one question, and read/write/remove/enumerate over a
// single keyed namespace are four shapes of the same question in the way a
// scalar count and a bucketed count are two. They also cannot be separated in
// practice: SetSetting's promise about what a later GetSetting returns, and
// UnsetSetting's about what a later ListSettings omits, are statements about
// each other, so splitting them
// would put one role's promise inside another role's result.
//
// WHAT IS NOT HERE, and this is the deliberate half of the role's scope.
// `bd config show`, `bd config drift`, `bd config apply` and `bd config
// validate` are MULTI-SOURCE diagnostics over config.yaml, environment
// variables, git config, this plane and the server's own state at once. Three
// of those five sources are files on the CLIENT's filesystem, which a server
// answering for a remote workspace can never read, so a substrate role that
// claimed to answer them would be claiming to see something it cannot. They
// stay front-door commands.
//
// Neither is the SETTLING of a value. Nothing here says which source wins when
// config.yaml and this plane both carry a key; that precedence belongs to the
// resolver the front doors share, and this role answers only for what the
// database holds. A caller that wants the EFFECTIVE value of a setting is
// asking a different question and this is not the role that answers it.
//
// KEYS THIS PLANE DOES NOT OWN. Two families of key are routed elsewhere
// before a front door reaches this role, and they are named here so that the
// absence is legible rather than surprising: the yaml-only keys (`export.*`,
// `dolt.*`, `federation.*`, `storage-class.*`, the tracker credentials and the
// rest of internal/config's list) live in config.yaml, and `beads.role` lives
// in git config. A write of one of those through this role would land a row no
// reader consults. This role does not police that routing — the front door
// that knows where the client's files are does — with the ONE exception the
// key constants above describe, which is policed here because the damage is
// not a dead row but a workspace whose ids stop agreeing with each other.
//
// THE THIRD FAMILY IS NOT ROUTED ELSEWHERE — IT RIDES IN THIS PLANE'S TABLE.
// Everything under `kv.` is USER DATA: the generic keys `bd kv set` writes, and
// nested inside them the `bd remember` memories under `kv.memory.`, which have
// their own merge semantics (a config conflict auto-resolves --theirs only when
// every conflicted key is a memory) and their own front doors. They are stored
// as config rows because there is one table, not because they are settings.
// `bd kv` and the memory surface are their views; this role is not. So NEITHER
// READ ANSWERS THEM: ListSettings omits them and GetSetting answers a `kv.` key
// exactly as it answers a key nothing ever stored — see each one's own doc.
//
// THE READS ARE A FIREWALL; THE WRITES ARE NOT. SetSetting and UnsetSetting
// still take a verbatim `kv.` key, which is the escape hatch for deleting a
// wedged memory, and a write discloses nothing.
//
// THE READ HALF USED TO STOP AT THE ENUMERATION, and stopping there was the
// bug. The disclosure is a stored memory's CONTENT — `bd config list` and an
// unauthenticated GET /v0/beads/config both handed it over, and the latter's
// redaction decides on the key NAME while a memory's secret is in the value.
// The exclusion was argued as "a caller naming one exact key is a different
// question", which holds only while the key is hard to name: `bd remember`
// derives its key from the content it stores, so the names are guessable and a
// point read walked around the wall. What a caller loses is `bd config get
// kv.foo`, which was never the door for those rows — `bd kv get` and
// `bd recall` are, and they read the store directly.
//
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil. Implementations never mutate
// caller-owned request values.
type WorkspaceConfig interface {
	// GetSetting returns one stored setting's value, or "" when nothing stored it.
	//
	// It is a READ: nothing here records a history entry, fires a completion
	// hook or changes a row. A key nothing set is "" and a nil error, never
	// ErrNotFound — see SettingResult.Value for why that conflation is stated
	// rather than fixed.
	//
	// An empty Key is ErrValidation. There is no row a caller could mean by
	// it, so answering "" would report "not set" for a question that was never
	// asked.
	//
	// A KEY UNDER `kv.` IS ANSWERED AS IF IT WERE ABSENT: the echoed key, ""
	// and a nil error, whether or not a row is there. It is deliberately
	// indistinguishable from the unset answer — an error, of any kind, would
	// confirm the key to the caller who guessed it, and this role has no
	// ErrNotFound to give. The row is NOT touched; see "KEYS THIS PLANE DOES
	// NOT OWN" above for what that plane is and which doors do read it.
	GetSetting(ctx context.Context, req GetSettingRequest) (SettingResult, error)

	// ListSettings returns every stored setting.
	//
	// EXCEPT THE KV PLANE. No key under `kv.` appears here — not the generic
	// `bd kv` keys and not the `bd remember` memories nested under them —
	// though those rows live in the same table. Naming one exactly does not get
	// it either: GetSetting answers a `kv.` key as absent. See "KEYS THIS PLANE
	// DOES NOT OWN" above for what that plane is and which doors do read it.
	//
	// A read, on the same terms as GetSetting, and one whose answer is a MAP rather
	// than a page: settings are a keyed namespace a workspace holds tens of,
	// not a collection to scan, so there is no order, no limit and no cursor
	// in the question. A caller that wants them ordered sorts the keys; every
	// front door that prints them does.
	ListSettings(ctx context.Context, req ListSettingsRequest) (ListSettingsResult, error)

	// SetSetting stores one setting, replacing any value already there, and
	// re-synchronizes the normalized table a value backs.
	//
	// THE REFUSALS, in the order they are applied:
	//
	//   - a Key that is empty after trimming is ErrValidation;
	//   - a Key naming the issue prefix, in either spelling, is ErrValidation.
	//     The prefix is owned by `bd init --prefix`, `bd bootstrap` and
	//     `bd rename-prefix`, each of which does work this plane cannot:
	//     rewriting existing ids, or seeding a workspace that has none.
	//     Storing a new prefix here instead leaves the beads created before
	//     the write and the beads created after it disagreeing about their own
	//     namespace, with nothing to reconcile them;
	//   - a status.custom value that does not parse is ErrValidation, and
	//     NOTHING is written. The parse is here rather than at a front door
	//     because the value is not merely stored: it is projected into the
	//     custom_statuses table, and a value that cannot be projected would
	//     otherwise be accepted by one door, refused by another, and half
	//     applied by whichever wrote first.
	//
	// THE SIDE EFFECT, and the reason this method is not a thin REPLACE INTO.
	// Writing status.custom rewrites the custom_statuses table from the new
	// value, and writing types.custom rewrites custom_types, IN THE SAME
	// TRANSACTION as the row. Those tables are what reads consult FIRST — a
	// row without its table is a value that has been stored and has no effect,
	// for as long as the table holds something else — so the write and the
	// projection are one durable act or neither happens. Every other key is
	// stored and nothing more.
	//
	// A SetSetting of the value already there still performs the write and its
	// projection. Nothing here compares first: a no-op detection would make
	// the repair of a table that had drifted from its row depend on the row
	// having changed, which is precisely the state that needs repairing.
	SetSetting(ctx context.Context, req SetSettingRequest) (SetSettingResult, error)

	// UnsetSetting removes one stored setting.
	//
	// Removing a key nothing set SUCCEEDS. UnsetSetting states an intended end state
	// rather than an act performed, so a caller clearing configuration it is
	// not sure was ever written does not have to classify an error to learn
	// that it was already absent.
	//
	// An empty Key is ErrValidation, for the reason GetSetting's is. The protected
	// key is NOT refused here, and that asymmetry is shipped behavior on all
	// three implementations rather than a decision this contract makes; it is
	// recorded as bd-yby99.34.
	//
	// UNSET DOES NOT UNDO SET'S PROJECTION. Removing status.custom or
	// types.custom deletes the row and LEAVES the normalized table exactly as
	// the last SetSetting left it, so the custom statuses and types keep applying
	// after the key that configured them is gone. All three implementations
	// agree, so this is the plane's behavior and not a divergence; it is
	// recorded as bd-yby99.33 and stated here so that no reader infers a
	// symmetry with SetSetting that the code does not have.
	UnsetSetting(ctx context.Context, req UnsetSettingRequest) (UnsetSettingResult, error)
}
