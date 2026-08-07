package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
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
// ListSettings: no key under `kv.` is enumerated, and the asymmetry that stops
// the exclusion there — GetSetting still answers each of those keys by name.
//
// The two planes share one table, so this is the case that tells a body
// filtering the SETTINGS plane from one that just happens to hold no memories.
// Both probes are written THROUGH the role, because SetSetting still accepts
// them: a write that lands and an enumeration that omits it is the whole claim,
// and seeding out of band would leave the write half untested.
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

	// And the stated asymmetry: the exclusion is on the ENUMERATION only, so a
	// caller naming an exact key still gets it. Removing this half would not
	// fail any other case — it is pinned here or nowhere.
	assertWorkspaceConfigValue(t, ctx, fixture, generic, "kv row")
	assertWorkspaceConfigValue(t, ctx, fixture, memory, "the deploy token is sk-live-000")
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
