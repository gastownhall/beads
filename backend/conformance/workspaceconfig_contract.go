package conformance

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of
// publicops.WorkspaceConfig must satisfy. Each case asserts what
// issueops/workspaceconfig.go PROMISES, cited by line, rather than what any one
// backend happens to do today; a backend that disagrees is parked at its own
// wiring site with skipKnownDivergence so the case still runs on the ones that
// agree.
//
// THIS CONTRACT HAS A DIFFERENT SHAPE FROM THE ISSUE CONTRACTS: there are no
// rows to seed. What this plane promises is PRECEDENCE and SIDE EFFECTS — two
// of its keys are PROJECTED into normalized lookup tables that reads consult
// before the key itself, so "the write succeeded" and "the write took effect"
// are separate facts. Every projection case therefore reads the TABLE through
// QueryScalar; reading the setting back through the role is exactly the check
// that passed on a backend where nothing took effect.
//
// There are three wirings — the server-backed store, the embedded store and the
// unit-of-work provider — and only TWO independent bodies between them: dolt
// and embeddeddolt both hand back internal/workapi/storeworkspaceconfig, so
// they are one vote plus an engine check. All three share the refusals, which
// come from workapi.ValidateSettingWrite, so what these cases catch below that
// validator is the EXECUTION half.
//
// KEYS ARE NAMESPACED WITH THE FIXTURE PREFIX wherever they can be, because
// config keys are global to a workspace. status.custom and types.custom cannot
// be: their whole point is that those exact names are projected, so those cases
// write the real keys and assert the EXACT resulting table content rather than
// a delta — safe because a write rewrites the table outright.
//
// NOT here: which SOURCE owns a key, and the multi-source views over it
// (`bd config show`, `drift`, `apply`, `validate`). That is front-door routing
// over files on the client's machine; cmd/bd/config_test.go pins it.

// WorkspaceConfigFixture supplies adapter-specific storage access for the
// settings assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type WorkspaceConfigFixture struct {
	// IssuePrefix namespaces the keys each assertion writes, so several of them
	// can share one database.
	IssuePrefix     string
	WorkspaceConfig publicops.WorkspaceConfig
	// SetConfig writes one workspace config key OUT OF BAND, past the role —
	// the same seam a workspace's own initialization uses. It is how the case
	// that removes the protected key puts it back.
	SetConfig func(context.Context, string, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. It is how the projection cases read the
	// normalized tables, which the role deliberately gives no way to read.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
	// Vocabulary reads the workspace vocabulary back. A nil hook means "this
	// backend cannot read the vocabulary its own writes configure", and the
	// cases that need it SKIP with that reason rather than passing quietly.
	Vocabulary *WorkspaceVocabularyReader
}

// WorkspaceVocabularyReader is the READ half of the two projected keys, which
// this role deliberately has no verb for: SetSetting rewrites custom_statuses
// and custom_types and nothing on publicops.WorkspaceConfig reads them back.
//
// IT IS SHAPED LIKE THE CONSUMER, NOT LIKE ANY ONE BACKEND. The three methods
// are workapi.ConfigSource method for method, because that is the seam
// workapi.LoadListConfig reads through, and what LoadListConfig does with an
// error is the whole reason these cases exist: it loads all three UP FRONT and
// wraps ANY failure, with no degraded path. `bd list`, `bd ready` and every
// other list-shaped command call it before they can render a single row, so a
// vocabulary read that answers an ERROR where the workspace has simply not
// configured anything does not degrade — it takes out the entire family of
// commands, on the first one a fresh workspace runs.
//
// THE THREE LEGS FILL IT FROM GENUINELY DIFFERENT BODIES. The two stores answer
// from GetCustomStatusesDetailed / GetCustomTypes / GetInfraTypes, which read
// through a per-store cache that SetConfig drops by key; the unit-of-work
// backend answers from its ConfigUseCase, which has no cache, unions custom
// types with the workspace YAML and resolves the infra DEFAULT in its own code
// (internal/storage/domain/config.go) rather than in
// issueops.ResolveInfraTypesInTx. So these cases are two votes on the value and
// three on the shape of the answer.
//
// DoltStorage.GetCustomStatuses — the names-only spelling — is deliberately
// absent. It is types.CustomStatusNames of the detailed slice off the same
// cached load, so its order cannot differ, and the unit-of-work role has no
// counterpart to it at all.
type WorkspaceVocabularyReader struct {
	// CustomStatuses reads the statuses status.custom projects, WITH their
	// categories. Store legs: GetCustomStatusesDetailed.
	CustomStatuses func(context.Context) ([]types.CustomStatus, error)
	// CustomTypes reads the types types.custom projects.
	CustomTypes func(context.Context) ([]string, error)
	// InfraTypes reads the resolved infrastructure-type set — the types whose
	// issues route to the wisps plane instead of the versioned one.
	InfraTypes func(context.Context) (map[string]bool, error)
}

// workspaceConfigDefaultInfraTypes is the infra set a workspace that has
// configured none resolves to.
//
// SPELLED OUT rather than read from the production constant: this suite is the
// contract, and asserting the answer against the same place the implementation
// reads it from would assert nothing.
var workspaceConfigDefaultInfraTypes = map[string]bool{"agent": true, "role": true, "message": true}

// workspaceConfigVocabulary returns the vocabulary reader or skips loudly.
func workspaceConfigVocabulary(t *testing.T, fixture WorkspaceConfigFixture) *WorkspaceVocabularyReader {
	t.Helper()
	if fixture.Vocabulary == nil {
		t.Skip("fixture.Vocabulary is nil: this backend cannot read the vocabulary its own writes project, " +
			"so the three reads workapi.LoadListConfig makes before any list-shaped command renders a row are unobservable here")
	}
	return fixture.Vocabulary
}

func readWorkspaceConfigCustomStatuses(t *testing.T, ctx context.Context, vocabulary *WorkspaceVocabularyReader, when string) []types.CustomStatus {
	t.Helper()
	statuses, err := vocabulary.CustomStatuses(ctx)
	if err != nil {
		t.Fatalf("read the custom statuses %s: %v — LoadListConfig wraps this error and every list-shaped command fails with it", when, err)
	}
	return statuses
}

func readWorkspaceConfigCustomTypes(t *testing.T, ctx context.Context, vocabulary *WorkspaceVocabularyReader, when string) []string {
	t.Helper()
	custom, err := vocabulary.CustomTypes(ctx)
	if err != nil {
		t.Fatalf("read the custom types %s: %v — LoadListConfig wraps this error and every list-shaped command fails with it", when, err)
	}
	return custom
}

func readWorkspaceConfigInfraTypes(t *testing.T, ctx context.Context, vocabulary *WorkspaceVocabularyReader, when string) map[string]bool {
	t.Helper()
	infra, err := vocabulary.InfraTypes(ctx)
	if err != nil {
		t.Fatalf("read the infra types %s: %v — LoadListConfig wraps this error and every list-shaped command fails with it", when, err)
	}
	return infra
}

// workspaceConfigStatusPairs renders statuses as "name:category" IN ORDER, for
// a failure message that reads. It does NOT sort: order is what two of these
// cases are about.
func workspaceConfigStatusPairs(statuses []types.CustomStatus) []string {
	out := make([]string, len(statuses))
	for i, status := range statuses {
		out[i] = status.Name + ":" + string(status.Category)
	}
	return out
}

// RunWorkspaceConfigStoresAValueVerbatim pins workspaceconfig.go:93-100: a
// successful write stores the value as given, and the result says so.
//
// The value carries surrounding space and an inner comma on purpose: the comma
// separates entries in status.custom and types.custom, so a body that reached
// for a splitter or a trimmer on the general path is caught here.
func RunWorkspaceConfigStoresAValueVerbatim(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	key := workspaceConfigKey(fixture, "verbatim")
	const value = "  a, b  "

	result := setWorkspaceConfigSetting(t, ctx, fixture, key, value)
	if result.Key != key || result.Value != value {
		t.Fatalf("SetSetting result = %q=%q, want %q=%q", result.Key, result.Value, key, value)
	}
	assertWorkspaceConfigValue(t, ctx, fixture, key, value)
}

// RunWorkspaceConfigReplacesAnExistingValue pins workspaceconfig.go's "Set
// stores one setting, REPLACING any value already there": one value per key, so
// a second write is not an append and not a refusal.
func RunWorkspaceConfigReplacesAnExistingValue(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	key := workspaceConfigKey(fixture, "replace")

	setWorkspaceConfigSetting(t, ctx, fixture, key, "first")
	setWorkspaceConfigSetting(t, ctx, fixture, key, "second")
	assertWorkspaceConfigValue(t, ctx, fixture, key, "second")
}

// RunWorkspaceConfigConflatesAnUnsetKeyWithAnEmptyValue pins
// workspaceconfig.go's SettingResult.Value: "" with a nil error is the answer
// for BOTH a key nothing ever wrote and a key written as the empty string, and
// there is no ErrNotFound on this role.
//
// The conflation is what lets `bd config get` print "(not set)" for a key that
// was in fact set.
func RunWorkspaceConfigConflatesAnUnsetKeyWithAnEmptyValue(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	never := workspaceConfigKey(fixture, "never-written")
	emptied := workspaceConfigKey(fixture, "written-empty")

	assertWorkspaceConfigValue(t, ctx, fixture, never, "")
	setWorkspaceConfigSetting(t, ctx, fixture, emptied, "")
	assertWorkspaceConfigValue(t, ctx, fixture, emptied, "")

	// The two are the same ANSWER, but not the same row: the written one is
	// present in the enumeration and the never-written one is not. That is the
	// only way a caller can tell them apart on this role.
	settings := listWorkspaceConfigSettings(t, ctx, fixture)
	if _, ok := settings[emptied]; !ok {
		t.Fatalf("ListSettings omits %q, which was written as the empty string", emptied)
	}
	if _, ok := settings[never]; ok {
		t.Fatalf("ListSettings carries %q, which nothing wrote", never)
	}
}

// RunWorkspaceConfigListsEveryStoredSetting pins
// workspaceconfig.go's ListSettingsResult.Settings: every stored key with its
// value, and an empty map rather than nil.
func RunWorkspaceConfigListsEveryStoredSetting(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	first := workspaceConfigKey(fixture, "list-one")
	second := workspaceConfigKey(fixture, "list-two")
	setWorkspaceConfigSetting(t, ctx, fixture, first, "1")
	setWorkspaceConfigSetting(t, ctx, fixture, second, "2")

	settings := listWorkspaceConfigSettings(t, ctx, fixture)
	if settings == nil {
		t.Fatal("ListSettings returned a nil map; the contract promises an empty one")
	}
	for key, want := range map[string]string{first: "1", second: "2"} {
		if got, ok := settings[key]; !ok || got != want {
			t.Fatalf("ListSettings[%q] = %q (present=%v), want %q", key, got, ok, want)
		}
	}
}

// RunWorkspaceConfigListExcludesTheKVPlane pins workspaceconfig.go's
// ListSettings: no key under `kv.` is enumerated.
//
// The two planes share one table, so this is the case that tells a body
// filtering the SETTINGS plane from one that just happens to hold no memories.
// Both probes are written THROUGH the role, because SetSetting still accepts
// them: a write that lands and an enumeration that omits it is the whole claim,
// and seeding out of band would leave the write half untested.
//
// THE OTHER HALF OF THE FIREWALL IS ITS OWN CASE.
// RunWorkspaceConfigPointReadRefusesTheKVPlane pins what GetSetting answers for
// these keys, which is what says the rows this case cannot see are still there.
//
// The generic key and the memory key are both here because the exclusion is the
// whole prefix. A body that reached for kvkeys.MemoryConfigKeyPrefix — the
// narrower constant, and the tempting one — passes on the memory and fails on
// the generic row.
//
// The value on the memory probe is shaped like a credential on purpose. That is
// what the exclusion is for: the HTTP surface's redaction decides on the KEY
// name, so a memory whose content is a secret under an innocuous slug was
// served verbatim by an unauthenticated GET /v0/beads/config.
func RunWorkspaceConfigListExcludesTheKVPlane(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	setting := workspaceConfigKey(fixture, "kv-neighbor")
	generic := kvkeys.Prefix + fixture.IssuePrefix + "-generic"
	memory := kvkeys.MemoryConfigKeyPrefix + fixture.IssuePrefix + "-slug"

	setWorkspaceConfigSetting(t, ctx, fixture, setting, "settings row")
	setWorkspaceConfigSetting(t, ctx, fixture, generic, "kv row")
	setWorkspaceConfigSetting(t, ctx, fixture, memory, "the deploy token is sk-live-000")

	settings := listWorkspaceConfigSettings(t, ctx, fixture)
	// The settings row FIRST: an enumeration that dropped everything would
	// satisfy the exclusion and mean nothing.
	if got, ok := settings[setting]; !ok || got != "settings row" {
		t.Fatalf("ListSettings[%q] = %q (present=%v), want %q — the settings plane must survive the exclusion",
			setting, got, ok, "settings row")
	}
	for key := range settings {
		if strings.HasPrefix(key, kvkeys.Prefix) {
			t.Fatalf("ListSettings carries %q: the kv plane is user data riding in the settings table, not a setting", key)
		}
	}

	// The rows the enumeration did not carry are still THERE. Without this the
	// case passes against a SetSetting that dropped the writes on the floor,
	// which is an exclusion of nothing.
	assertWorkspaceConfigRowCount(t, ctx, fixture, generic, 1)
	assertWorkspaceConfigRowCount(t, ctx, fixture, memory, 1)
}

// RunWorkspaceConfigPointReadRefusesTheKVPlane pins the half of the read
// firewall a caller reaches by NAMING a key: GetSetting answers a `kv.` key
// exactly as it answers a key nothing ever stored, whether or not the row is
// there.
//
// This half is the one that matters. The enumeration exclusion assumed a caller
// who knew the exact key was a different class of caller, and `bd remember`
// derives its key from the content it stores, so the keys are guessable and
// GET /v0/beads/config/kv.memory.<slug> walked around the wall — on a surface
// whose only credential is an optional shared bearer, and whose redaction
// decides on the key NAME while a memory's secret is in the VALUE.
//
// WHAT IT ASSERTS IS AN INDISTINGUISHABILITY, and it takes four probes to say
// so:
//
//   - A stored kv row and a stored memory both answer "" with a NIL ERROR. An
//     error of any kind — including one wrapping ErrValidation, which this role
//     already uses for an empty key — would confirm to the caller who guessed
//     the key that it named something.
//   - A `kv.` key nothing ever wrote answers the SAME THING, asserted through
//     the same helper, so the refusal and the absence are one answer rather
//     than two that happen to render alike.
//   - The ROWS SURVIVE, read out of band. A body that satisfied the read by
//     deleting the row would pass every assertion above and destroy the user's
//     memories; and the rows are what the write half of this plane still owns.
//   - A SETTINGS key still reads back its value. A GetSetting that answered ""
//     for everything is the other way to pass this case for the wrong reason,
//     and it would take out `bd config get` entirely.
//
// AND THE WRITES ARE UNTOUCHED, asserted last: UnsetSetting still removes a
// `kv.` row. That is the escape hatch for a wedged memory, it is the only thing
// left on this role that can reach the plane, and it leaves the workspace as
// this case found it.
func RunWorkspaceConfigPointReadRefusesTheKVPlane(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	setting := workspaceConfigKey(fixture, "point-read-neighbor")
	generic := kvkeys.Prefix + fixture.IssuePrefix + "-point-read"
	memory := kvkeys.MemoryConfigKeyPrefix + fixture.IssuePrefix + "-point-read-slug"
	absent := kvkeys.MemoryConfigKeyPrefix + fixture.IssuePrefix + "-never-remembered"

	setWorkspaceConfigSetting(t, ctx, fixture, setting, "settings row")
	setWorkspaceConfigSetting(t, ctx, fixture, generic, "kv row")
	setWorkspaceConfigSetting(t, ctx, fixture, memory, "the deploy token is sk-live-000")
	// The writes landed, so the empty answers below are a refusal rather than a
	// pair of keys that were never stored.
	assertWorkspaceConfigRowCount(t, ctx, fixture, generic, 1)
	assertWorkspaceConfigRowCount(t, ctx, fixture, memory, 1)

	// The settings plane FIRST: a role answering "" to everything would satisfy
	// the refusals and mean nothing.
	assertWorkspaceConfigValue(t, ctx, fixture, setting, "settings row")

	for _, key := range []string{generic, memory, absent} {
		result, err := fixture.WorkspaceConfig.GetSetting(ctx, publicops.GetSettingRequest{Key: key})
		if err != nil {
			t.Fatalf("GetSetting(%q) = %v, want the absent-key answer: an error tells the caller who guessed the key that it named something", key, err)
		}
		if result.Key != key {
			t.Fatalf("GetSetting(%q) echoed key %q; the refusal is the absent-key answer, which echoes the request", key, result.Key)
		}
		if result.Value != "" {
			t.Fatalf("GetSetting(%q) = %q, want \"\": the kv plane is user data riding in the settings table and this role does not serve it", key, result.Value)
		}
	}

	// The refusal did not eat the rows. Out of band, because the role has just
	// promised it will not show them.
	assertWorkspaceConfigRowCount(t, ctx, fixture, generic, 1)
	assertWorkspaceConfigRowCount(t, ctx, fixture, memory, 1)

	// The write half still reaches the plane, which is the escape hatch for a
	// memory whose value has wedged something.
	unsetWorkspaceConfigSetting(t, ctx, fixture, generic)
	unsetWorkspaceConfigSetting(t, ctx, fixture, memory)
	assertWorkspaceConfigRowCount(t, ctx, fixture, generic, 0)
	assertWorkspaceConfigRowCount(t, ctx, fixture, memory, 0)
}

// RunWorkspaceConfigUnsetRemovesTheSetting pins that a removed key is gone from
// BOTH answers the role gives — the single read and the enumeration — rather
// than from one of them.
func RunWorkspaceConfigUnsetRemovesTheSetting(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	key := workspaceConfigKey(fixture, "removable")
	setWorkspaceConfigSetting(t, ctx, fixture, key, "present")
	assertWorkspaceConfigValue(t, ctx, fixture, key, "present")

	unsetWorkspaceConfigSetting(t, ctx, fixture, key)
	assertWorkspaceConfigValue(t, ctx, fixture, key, "")
	if _, ok := listWorkspaceConfigSettings(t, ctx, fixture)[key]; ok {
		t.Fatalf("ListSettings still carries %q after UnsetSetting", key)
	}
}

// RunWorkspaceConfigUnsetOfAnAbsentKeySucceeds pins workspaceconfig.go's
// "Removing a key nothing set SUCCEEDS": UnsetSetting states an intended end
// state, not a fact about the row it found.
func RunWorkspaceConfigUnsetOfAnAbsentKeySucceeds(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	key := workspaceConfigKey(fixture, "absent")

	result, err := fixture.WorkspaceConfig.UnsetSetting(ctx, publicops.UnsetSettingRequest{Key: key})
	if err != nil {
		t.Fatalf("UnsetSetting(%q) on an absent key = %v, want success", key, err)
	}
	if result.Key != key {
		t.Fatalf("UnsetSetting result key = %q, want %q", result.Key, key)
	}
	// Twice, because idempotence is the claim: the second call is the one a
	// retrying caller actually makes.
	if _, err := fixture.WorkspaceConfig.UnsetSetting(ctx, publicops.UnsetSettingRequest{Key: key}); err != nil {
		t.Fatalf("second UnsetSetting(%q) = %v, want success", key, err)
	}
}

// RunWorkspaceConfigRefusesAnEmptyKey pins the empty-key refusal on all three
// verbs that take one, as ErrValidation rather than as any error: the front
// doors classify on that sentinel to tell a mistake from a storage failure.
func RunWorkspaceConfigRefusesAnEmptyKey(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	for _, blank := range []string{"", "   "} {
		if _, err := fixture.WorkspaceConfig.GetSetting(ctx, publicops.GetSettingRequest{Key: blank}); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("GetSetting(%q) error = %v, want ErrValidation", blank, err)
		}
		if _, err := fixture.WorkspaceConfig.SetSetting(ctx, publicops.SetSettingRequest{Key: blank, Value: "x"}); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("SetSetting(%q) error = %v, want ErrValidation", blank, err)
		}
		if _, err := fixture.WorkspaceConfig.UnsetSetting(ctx, publicops.UnsetSettingRequest{Key: blank}); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("UnsetSetting(%q) error = %v, want ErrValidation", blank, err)
		}
	}
}

// RunWorkspaceConfigRefusesTheProtectedKeyOnSet pins the one key this plane
// will not write, in BOTH spellings, and pins that the refusal leaves the
// stored prefix standing.
//
// A refusal that had already written would be worse than no refusal at all: the
// workspace would be re-prefixed AND the caller told it had not been.
func RunWorkspaceConfigRefusesTheProtectedKeyOnSet(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	before := getWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyIssuePrefix)

	for _, test := range []struct{ key, want string }{
		// The underscored spelling keeps whatever the workspace was initialized
		// with; the dashed one was never written, so a write that slipped
		// through shows up as a value where there was none.
		{publicops.SettingKeyIssuePrefix, before},
		{"issue-prefix", ""},
	} {
		if _, err := fixture.WorkspaceConfig.SetSetting(ctx, publicops.SetSettingRequest{Key: test.key, Value: "hijack"}); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("SetSetting(%q) error = %v, want ErrValidation", test.key, err)
		}
		assertWorkspaceConfigValue(t, ctx, fixture, test.key, test.want)
	}
}

// RunWorkspaceConfigUnsetDoesNotRefuseTheProtectedKey pins the asymmetry
// workspaceconfig.go's UnsetSetting doc records as bd-yby99.34: Set refuses the
// prefix and Unset does not.
//
// It is pinned rather than fixed because refusing it is a user-visible change,
// and pinning keeps it from being closed on one backend and not the others. The
// prefix is restored afterwards, since the suite shares the workspace.
func RunWorkspaceConfigUnsetDoesNotRefuseTheProtectedKey(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	before := getWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyIssuePrefix)
	if before == "" {
		t.Fatalf("fixture has no %s set; this case needs one to remove and restore", publicops.SettingKeyIssuePrefix)
	}

	unsetWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyIssuePrefix)
	assertWorkspaceConfigValue(t, ctx, fixture, publicops.SettingKeyIssuePrefix, "")

	// Restored out of band, because the role refuses to write it back.
	if err := fixture.SetConfig(ctx, publicops.SettingKeyIssuePrefix, before); err != nil {
		t.Fatalf("restore %s to %q: %v", publicops.SettingKeyIssuePrefix, before, err)
	}
	assertWorkspaceConfigValue(t, ctx, fixture, publicops.SettingKeyIssuePrefix, before)
}

// RunWorkspaceConfigRefusesAnUnparseableCustomStatus pins the one value-shape
// refusal this plane makes, and pins that NOTHING is written when it fires.
//
// "Nothing" is two things, asserted separately: the config row keeps its
// previous value, AND custom_statuses keeps its previous contents. A body that
// wrote the row first and parsed while projecting would leave a stored status
// set that no read agrees with.
func RunWorkspaceConfigRefusesAnUnparseableCustomStatus(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	const good = "awaiting_review:active"
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyStatusCustom, good)
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 1)

	// A built-in name: refused by the parser, for a reason a caller could
	// plausibly hit rather than a syntactic accident.
	if _, err := fixture.WorkspaceConfig.SetSetting(ctx, publicops.SetSettingRequest{
		Key: publicops.SettingKeyStatusCustom, Value: "open",
	}); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("SetSetting(status.custom, %q) error = %v, want ErrValidation", "open", err)
	}
	assertWorkspaceConfigValue(t, ctx, fixture, publicops.SettingKeyStatusCustom, good)
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 1)
}

// RunWorkspaceConfigProjectsCustomStatuses pins the side effect
// workspaceconfig.go's SetSetting doc describes for status.custom: the value is
// not merely stored, it REWRITES custom_statuses, which reads consult first.
func RunWorkspaceConfigProjectsCustomStatuses(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyStatusCustom, "awaiting_review:active,awaiting_docs:wip")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 2)
	assertWorkspaceConfigTableHas(t, ctx, fixture, "custom_statuses", "awaiting_review")
	assertWorkspaceConfigTableHas(t, ctx, fixture, "custom_statuses", "awaiting_docs")
}

// RunWorkspaceConfigProjectsCustomTypes is the same pin for types.custom, and
// it FAILED on the unit-of-work backend before this role existed: that route
// wrote the string and left custom_types holding the previous set, so
// `bd config set types.custom` reported success while `bd create -t <the new
// type>` kept answering "invalid issue type" and doctor reported all-OK.
//
// The three-stage sequence pins the REWRITE rather than an insert: a second
// write replaces the first set instead of adding to it, and the empty value
// clears the table.
func RunWorkspaceConfigProjectsCustomTypes(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "research")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 1)
	assertWorkspaceConfigTableHas(t, ctx, fixture, "custom_types", "research")

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "session")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 1)
	assertWorkspaceConfigTableHas(t, ctx, fixture, "custom_types", "session")

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 0)
}

// RunWorkspaceConfigUnsetLeavesTheProjectionBehind pins the asymmetry
// workspaceconfig.go's UnsetSetting doc records as bd-yby99.33: removing the
// key that configured a projection does NOT undo the projection, so the custom
// types keep applying after the setting that named them is gone.
//
// All three implementations agree, so pinning it keeps it from being quietly
// fixed on one backend, which would make the answer depend on which route
// removed the key.
func RunWorkspaceConfigUnsetLeavesTheProjectionBehind(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "leftover")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 1)

	unsetWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom)
	assertWorkspaceConfigValue(t, ctx, fixture, publicops.SettingKeyTypesCustom, "")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 1)

	// Left as this case found it: the suite shares the workspace.
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 0)
	unsetWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom)
}

// RunWorkspaceConfigARefusedWriteRecordsNoHistory pins the other half of "and
// NOTHING is written": a refusal does not reach storage at all, so it leaves no
// history entry behind either.
//
// The delta is taken around the refusal rather than read off the top of the
// log: two commits made inside one second tie on date.
func RunWorkspaceConfigARefusedWriteRecordsNoHistory(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the no-write half of a refusal is unobservable here")
	}
	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}

	for _, req := range []publicops.SetSettingRequest{
		{Key: "", Value: "x"},
		{Key: publicops.SettingKeyIssuePrefix, Value: "hijack"},
		{Key: publicops.SettingKeyStatusCustom, Value: "open"},
	} {
		if _, err := fixture.WorkspaceConfig.SetSetting(ctx, req); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("SetSetting(%q) error = %v, want ErrValidation", req.Key, err)
		}
	}

	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across three refused writes, want no change", before, after)
	}
}

// RunWorkspaceConfigKeysAreCaseSensitive pins the collation of the key column
// this plane stores under: two keys differing only in case are two SETTINGS,
// each holding its own value, and both are enumerated.
//
// GetSettingRequest.Key states it from the other end — a key is used
// "verbatim: there is no namespace completion, no case folding" — and until now
// nothing held any backend to it. The stake is silent data loss in one
// direction and a wrong answer in the other: under a case-insensitive
// collation the second write REPLACES the first (the store bodies write with
// REPLACE INTO / an upsert on the key), so `bd config set myKey` would quietly
// destroy `mykey`, and every reader of either would get whichever row survived.
//
// BOTH READ PATHS ARE ASSERTED, because they are different SQL: GetSetting
// matches one key with `WHERE key = ?` and ListSettings enumerates the table.
// A collation that folded would break them differently — one answers the wrong
// row, the other returns one row where two were written — and the raw count
// below is what says which.
func RunWorkspaceConfigKeysAreCaseSensitive(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	lower := workspaceConfigKey(fixture, "casefold")
	upper := workspaceConfigKey(fixture, "CaseFold")
	if !strings.EqualFold(lower, upper) || lower == upper {
		t.Fatalf("the two probe keys %q and %q must differ, and differ ONLY in case; this case tests nothing otherwise", lower, upper)
	}

	setWorkspaceConfigSetting(t, ctx, fixture, lower, "lower")
	setWorkspaceConfigSetting(t, ctx, fixture, upper, "upper")

	// The lowercase write went FIRST, so a folding collation leaves "upper"
	// under both spellings and this is the assertion that names it.
	assertWorkspaceConfigValue(t, ctx, fixture, lower, "lower")
	assertWorkspaceConfigValue(t, ctx, fixture, upper, "upper")

	settings := listWorkspaceConfigSettings(t, ctx, fixture)
	folded := 0
	for key := range settings {
		if strings.EqualFold(key, lower) {
			folded++
		}
	}
	if folded != 2 {
		t.Fatalf("ListSettings carries %d keys case-folding to %q, want 2 — the two spellings are two settings", folded, lower)
	}

	// And the row count itself, because the role's two answers are both derived
	// from the table and a body that de-duplicated in Go would satisfy neither
	// half above for the right reason.
	var rows int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM config WHERE LOWER(`key`) = ?", []any{strings.ToLower(lower)}, &rows); err != nil {
		t.Fatalf("count the config rows case-folding to %q: %v", lower, err)
	}
	if rows != 2 {
		t.Fatalf("the config table holds %d rows case-folding to %q, want 2: the second write replaced the first", rows, lower)
	}
}

// RunWorkspaceConfigCustomStatusReadsAreOrderedByName pins the READ side of the
// projection RunWorkspaceConfigProjectsCustomStatuses pins the write side of:
// the statuses come back ORDERED BY NAME, alphabetically, independent of the
// order the config string listed them, each carrying its own category.
//
// The order is not cosmetic. It is the order `bd list`'s status vocabulary is
// built in (internal/workapi/list.go LoadListConfig), so it is the order every
// list-shaped command renders and groups by.
//
// THE FIXTURE IS WRITTEN OUT OF ORDER ON BOTH AXES — "zebra" before "alpha",
// and the alphabetically-first entry carrying the category that sorts LAST — so
// neither a body that preserved the config string's order nor one that ordered
// by category passes. The audit-tier ancestor of this case compared the
// detailed read as a SORTED set, which made its ordering half unobservable;
// this one compares the slice.
//
// It leaves the vocabulary CLEARED, which is the only direction a shared
// workspace can safely be left in: status.custom is workspace-global and a bare
// status installed here is claim-eligibility vocabulary a sibling never asked
// for.
func RunWorkspaceConfigCustomStatusReadsAreOrderedByName(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	vocabulary := workspaceConfigVocabulary(t, fixture)

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyStatusCustom, "zebra:wip,alpha:done")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 2)

	statuses := readWorkspaceConfigCustomStatuses(t, ctx, vocabulary, "after configuring two of them out of alphabetical order")
	want := []types.CustomStatus{
		{Name: "alpha", Category: types.CategoryDone},
		{Name: "zebra", Category: types.CategoryWIP},
	}
	if !slices.Equal(statuses, want) {
		t.Fatalf("the custom statuses read back as %v, want %v — ordered by NAME, and each carrying the category it was configured with",
			workspaceConfigStatusPairs(statuses), workspaceConfigStatusPairs(want))
	}

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyStatusCustom, "")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 0)
}

// RunWorkspaceConfigCustomTypeReadsAreOrderedByName is the same pin for
// types.custom, and it is a separate case rather than an arm of the one above
// because the two projections are two tables written by two syncs and read by
// two queries — a failure has to name which.
//
// The value is a JSON array in the order a caller would naturally write it, so
// the alphabetical answer cannot be the array's own order.
func RunWorkspaceConfigCustomTypeReadsAreOrderedByName(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	vocabulary := workspaceConfigVocabulary(t, fixture)

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, `["zebra","alpha"]`)
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 2)

	custom := readWorkspaceConfigCustomTypes(t, ctx, vocabulary, "after configuring two of them out of alphabetical order")
	if want := []string{"alpha", "zebra"}; !slices.Equal(custom, want) {
		t.Fatalf("the custom types read back as %v, want %v — ordered by NAME, not by the order the value listed them", custom, want)
	}

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "")
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 0)
}

// RunWorkspaceConfigConfiguredInfraTypesReplaceTheDefaultSet pins that a
// configured types.infra REPLACES the default infrastructure set outright
// rather than adding to it.
//
// Infra types decide which issue types route to the wisps plane instead of the
// versioned issues one, so a body that unioned the configured names with the
// built-in agent/role/message would keep versioning rows a workspace asked to
// keep ephemeral, and one that ignored the key would keep routing away rows it
// asked to version. The configured names are deliberately DISJOINT from the
// defaults, which is what makes "replaced" and "unioned" different answers; the
// case that reaches this key elsewhere in this package configures a name that
// is already in the default set, so it cannot tell them apart.
//
// THE PRE-VALUE IS READ AND ASSERTED, so the case cannot pass against a reader
// that answers the same thing however the key is set — and on the store legs it
// is what drives the CACHE: GetInfraTypes memoizes per store handle and only
// SetConfig drops it, so reading before and after is the only shape that
// exercises the invalidation. Restored to the default set on the way out.
func RunWorkspaceConfigConfiguredInfraTypesReplaceTheDefaultSet(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	vocabulary := workspaceConfigVocabulary(t, fixture)
	const key = "types.infra"

	before := readWorkspaceConfigInfraTypes(t, ctx, vocabulary, "before anything configures the key")
	if !maps.Equal(before, workspaceConfigDefaultInfraTypes) {
		t.Fatalf("the infra types read %v before this case configured any, want the default %v: "+
			"the replacement below is only observable against a known starting set", before, workspaceConfigDefaultInfraTypes)
	}

	setWorkspaceConfigSetting(t, ctx, fixture, key, "gate,probe")
	configured := readWorkspaceConfigInfraTypes(t, ctx, vocabulary, "after configuring gate,probe")
	if want := map[string]bool{"gate": true, "probe": true}; !maps.Equal(configured, want) {
		t.Fatalf("the infra types read %v after configuring %q, want exactly %v — a configured set REPLACES the default one",
			configured, "gate,probe", want)
	}

	setWorkspaceConfigSetting(t, ctx, fixture, key, "")
	restored := readWorkspaceConfigInfraTypes(t, ctx, vocabulary, "after clearing the key again")
	if !maps.Equal(restored, workspaceConfigDefaultInfraTypes) {
		t.Fatalf("the infra types read %v after the key was cleared, want the default %v back", restored, workspaceConfigDefaultInfraTypes)
	}
}

// RunWorkspaceConfigUnconfiguredVocabularyReadsAreEmptyNotErrors pins the
// success path a workspace takes on its very first list-shaped command.
//
// THIS IS THE EDGE THAT BRICKS `bd list`. internal/workapi/list.go's
// LoadListConfig loads all three of these reads UP FRONT and wraps any error —
// "load custom statuses: %w" and its two siblings — with no degraded path, and
// every list-shaped front door calls it before it can render a row. So on a
// workspace that has configured no vocabulary the answer has to be a VALUE:
// empty with a nil error for the two projections, and the DEFAULT infra set for
// the third. A backend that answers a scan error, a table-missing error or a
// nil-map surprise for the state a workspace is in the moment it is created
// does not degrade — it takes out `bd list`, `bd ready` and the rest at once,
// and every existing vocabulary case configures the vocabulary first, so none
// of them would notice.
//
// IT INSTALLS ITS OWN PRECONDITION AND THEN CLEARS IT, rather than assuming the
// workspace arrives unconfigured. Two things follow, and both are the point:
// the case depends on NOTHING a sibling left behind, and the emptiness it
// asserts is a value the reads MOVED to — asserted non-empty first — instead of
// one they were already sitting on, which is how a hook wired to a constant
// empty answer would pass.
//
// IT CLEARS BY WRITING THE EMPTY VALUE, NOT BY UNSETTING, and that is forced:
// removing one of these keys deliberately leaves its projection standing
// (bd-yby99.33, pinned by RunWorkspaceConfigUnsetLeavesTheProjectionBehind), so
// an unset workspace is not an unconfigured one. An empty value and an absent
// row are the same state to every reader here —
// RunWorkspaceConfigConflatesAnUnsetKeyWithAnEmptyValue pins that at the role —
// and the raw table counts below are what say the projections really are empty
// before the reads are asked.
func RunWorkspaceConfigUnconfiguredVocabularyReadsAreEmptyNotErrors(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) {
	t.Helper()
	vocabulary := workspaceConfigVocabulary(t, fixture)

	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyStatusCustom, "awaiting_review:active")
	setWorkspaceConfigSetting(t, ctx, fixture, publicops.SettingKeyTypesCustom, "research")
	setWorkspaceConfigSetting(t, ctx, fixture, "types.infra", "gate")
	if got := readWorkspaceConfigCustomStatuses(t, ctx, vocabulary, "while one is configured"); len(got) != 1 {
		t.Fatalf("the custom statuses read %v with one configured, want exactly it: the emptiness below is only meaningful as a change",
			workspaceConfigStatusPairs(got))
	}
	if got := readWorkspaceConfigCustomTypes(t, ctx, vocabulary, "while one is configured"); len(got) != 1 {
		t.Fatalf("the custom types read %v with one configured, want exactly it: the emptiness below is only meaningful as a change", got)
	}
	if got := readWorkspaceConfigInfraTypes(t, ctx, vocabulary, "while the key is configured"); !maps.Equal(got, map[string]bool{"gate": true}) {
		t.Fatalf("the infra types read %v with gate configured, want exactly it: the default below is only meaningful as a change", got)
	}

	for _, key := range []string{publicops.SettingKeyStatusCustom, publicops.SettingKeyTypesCustom, "types.infra"} {
		setWorkspaceConfigSetting(t, ctx, fixture, key, "")
	}
	// The projections are EMPTY, read raw, so the answers below are earned by
	// the state rather than by a reader that never consulted it.
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_statuses", 0)
	assertWorkspaceConfigTableCount(t, ctx, fixture, "custom_types", 0)

	if got := readWorkspaceConfigCustomStatuses(t, ctx, vocabulary, "on an unconfigured workspace"); len(got) != 0 {
		t.Errorf("the custom statuses read %v on an unconfigured workspace, want empty", workspaceConfigStatusPairs(got))
	}
	if got := readWorkspaceConfigCustomTypes(t, ctx, vocabulary, "on an unconfigured workspace"); len(got) != 0 {
		t.Errorf("the custom types read %v on an unconfigured workspace, want empty", got)
	}
	if got := readWorkspaceConfigInfraTypes(t, ctx, vocabulary, "on an unconfigured workspace"); !maps.Equal(got, workspaceConfigDefaultInfraTypes) {
		t.Errorf("the infra types read %v on an unconfigured workspace, want the default %v — an empty answer here silently un-routes every ephemeral type",
			got, workspaceConfigDefaultInfraTypes)
	}
}

// workspaceConfigKey namespaces a probe key under the fixture's prefix.
func workspaceConfigKey(fixture WorkspaceConfigFixture, name string) string {
	return "custom." + fixture.IssuePrefix + "-" + name
}

func setWorkspaceConfigSetting(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, key, value string) publicops.SetSettingResult {
	t.Helper()
	result, err := fixture.WorkspaceConfig.SetSetting(ctx, publicops.SetSettingRequest{Key: key, Value: value})
	if err != nil {
		t.Fatalf("SetSetting(%q, %q): %v", key, value, err)
	}
	return result
}

func unsetWorkspaceConfigSetting(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, key string) {
	t.Helper()
	if _, err := fixture.WorkspaceConfig.UnsetSetting(ctx, publicops.UnsetSettingRequest{Key: key}); err != nil {
		t.Fatalf("UnsetSetting(%q): %v", key, err)
	}
}

func getWorkspaceConfigSetting(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, key string) string {
	t.Helper()
	result, err := fixture.WorkspaceConfig.GetSetting(ctx, publicops.GetSettingRequest{Key: key})
	if err != nil {
		t.Fatalf("GetSetting(%q): %v", key, err)
	}
	if result.Key != key {
		t.Fatalf("GetSetting(%q) echoed key %q", key, result.Key)
	}
	return result.Value
}

func assertWorkspaceConfigValue(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, key, want string) {
	t.Helper()
	if got := getWorkspaceConfigSetting(t, ctx, fixture, key); got != want {
		t.Fatalf("GetSetting(%q) = %q, want %q", key, got, want)
	}
}

func listWorkspaceConfigSettings(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture) map[string]string {
	t.Helper()
	result, err := fixture.WorkspaceConfig.ListSettings(ctx, publicops.ListSettingsRequest{})
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	return result.Settings
}

// assertWorkspaceConfigRowCount reads the config table directly to say whether
// one key has a row.
//
// It is the only way to observe a key the role has stopped answering for: the
// two kv-plane cases need "the row is there and the read refuses it" to be a
// different fact from "there is no row", and every verb on this role conflates
// them by design.
func assertWorkspaceConfigRowCount(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, key string, want int) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM config WHERE `key` = ?", []any{key}, &got); err != nil {
		t.Fatalf("count the config rows keyed %q: %v", key, err)
	}
	if got != want {
		t.Fatalf("the config table holds %d rows keyed %q, want %d", got, key, want)
	}
}

// assertWorkspaceConfigTableCount reads a normalized projection table directly.
// The role deliberately gives no way to read one, so this is the only assertion
// that can tell "the value was stored" from "the value took effect".
//
// The table name is interpolated because a table name cannot be a bind
// parameter; both call sites pass a literal from this file.
func assertWorkspaceConfigTableCount(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, table string, want int) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM "+table, nil, &got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s holds %d rows, want %d — the setting was stored without its projection", table, got, want)
	}
}

func assertWorkspaceConfigTableHas(t *testing.T, ctx context.Context, fixture WorkspaceConfigFixture, table, name string) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM "+table+" WHERE name = ?", []any{name}, &got); err != nil {
		t.Fatalf("look up %q in %s: %v", name, table, err)
	}
	if got != 1 {
		t.Fatalf("%s holds %d rows named %q, want 1", table, got, name)
	}
}
