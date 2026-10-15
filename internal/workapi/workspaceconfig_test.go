package workapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The settings-write rules, checked without a database.
//
// The conformance contract pins what only a real backend can show — that a row
// and its projection land together, and that a refusal leaves both alone. What
// is here is the part all three implementations share, so a rule broken here
// would otherwise fail three suites at once.

func TestValidateSettingKeyRefusesOnlyTheEmptyKey(t *testing.T) {
	for _, key := range []string{"", " ", "\t\n"} {
		if _, err := ValidateSettingKey(key); !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateSettingKey(%q) error = %v, want ErrValidation", key, err)
		}
	}
	// Everything else is a key, including the shapes a front door routes
	// elsewhere: which SOURCE owns a key is not this validator's question.
	for _, key := range []string{
		"custom.anything",
		"status.custom",
		"export.auto", // yaml-only at the front door, but not this plane's call
		"beads.role",  // git config at the front door, likewise
		"  padded  ",  // stored verbatim, untrimmed
		"Mixed.Case",  // no case folding
	} {
		got, err := ValidateSettingKey(key)
		if err != nil {
			t.Errorf("ValidateSettingKey(%q) = %v, want it accepted", key, err)
		}
		if got != key {
			t.Errorf("ValidateSettingKey(%q) returned %q; the key is used verbatim", key, got)
		}
	}
}

func TestValidateSettingWriteRefusesTheProtectedKeyInBothSpellings(t *testing.T) {
	for _, key := range []string{"issue_prefix", "issue-prefix"} {
		_, err := ValidateSettingWrite(key, "bd")
		if !errors.Is(err, issueops.ErrValidation) {
			t.Fatalf("ValidateSettingWrite(%q) error = %v, want ErrValidation", key, err)
		}
		// The message names the commands that DO own the prefix, so a caller is
		// not left hunting for a flag on the verb that just refused them.
		for _, want := range []string{"bd init --prefix", "bd bootstrap", "bd rename-prefix"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ValidateSettingWrite(%q) message %q does not name %q", key, err, want)
			}
		}
	}
}

// TestValidateSettingWriteRefusesYamlOnlyKeys pins GH#7559: `bd config set`
// already refuses a YamlOnlyKey write via config.IsYamlOnlyKey at its own
// door, but this plane's HTTP settings surface (bd serve and its dashboard,
// PUT /v0/beads/config/{key}) had no such rule, so a tracker secret
// (github.token, linear.api_key, jira.api_token, gitlab.token, ado.pat) sent
// through it landed as a plain row in the Dolt config table — a SYNCED
// table, replicated to every remote on the next bd dolt push/pull. The
// PoC's five secret keys, plus a non-secret YamlOnlyKey (the exact GH#536
// shape the map exists to prevent: a write that reports success but no
// reader ever consults), must all be refused here now.
func TestValidateSettingWriteRefusesYamlOnlyKeys(t *testing.T) {
	for _, key := range []string{
		// The PoC's five secrets.
		"github.token", "linear.api_key", "jira.api_token", "gitlab.token", "ado.pat",
		// A non-secret YamlOnlyKey: the rule is "this plane is the database,
		// these settings are not database settings", not "secrets only".
		"no-db",
	} {
		_, err := ValidateSettingWrite(key, "SECRET-OR-SETTING-VALUE")
		if !errors.Is(err, issueops.ErrValidation) {
			t.Fatalf("ValidateSettingWrite(%q) error = %v, want ErrValidation", key, err)
		}
		if strings.Contains(err.Error(), "SECRET-OR-SETTING-VALUE") {
			t.Errorf("ValidateSettingWrite(%q) error echoes the value %v; a secret must never appear in an error message", key, err)
		}
		if !strings.Contains(err.Error(), "config.yaml") {
			t.Errorf("ValidateSettingWrite(%q) message %q does not point the caller at config.yaml", key, err)
		}
	}
}

func TestValidateSettingWriteParsesTheCustomStatusValue(t *testing.T) {
	for _, value := range []string{
		"awaiting_review",
		"awaiting_review:active,awaiting_docs:wip",
		// Empty CLEARS the set, and clearing is not a value to parse. Refusing
		// it here would make the key impossible to unset by writing.
		"",
	} {
		if _, err := ValidateSettingWrite("status.custom", value); err != nil {
			t.Errorf("ValidateSettingWrite(status.custom, %q) = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{
		"open",              // collides with a built-in status
		"Awaiting Review",   // spaces and capitals are outside the name shape
		"awaiting_review:",  // trailing colon, empty category
		"awaiting:nonsense", // category outside the closed set
	} {
		if _, err := ValidateSettingWrite("status.custom", value); !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateSettingWrite(status.custom, %q) error = %v, want ErrValidation", value, err)
		}
	}
}

// TestValidateSettingWriteRefusesAMalformedRetainedTypesValue pins that
// types.retained, a SAFETY setting, fails closed at write time on SYNTAX: a
// value the lenient list parse would read as something other than the list
// the operator wrote — a bracketed list that is not JSON, a quoted name, two
// names split by a space — or as no name at all (`null`, `,`). Spelling is not
// refused: an alias or a case variant of a built-in type is canonicalized by
// the one read (RetainedTypesFromSetting), so it protects the type rows carry.
func TestValidateSettingWriteRefusesAMalformedRetainedTypesValue(t *testing.T) {
	for _, value := range []string{
		"",                 // unsets the key
		"   ",              // so does a blank one
		"op",               // one name
		"op,fact",          // the comma form
		" op , fact ",      // trimmed on read
		`["op","fact"]`,    // the JSON form
		`[]`,               // an empty JSON list retains nothing
		`[ ]`,              // so does one spelled with a space
		"never-registered", // a well-formed unknown name matches nothing, and is allowed
		"OpsKey",           // so does one in mixed case that is no built-in type
		"feature",          // a built-in type
		"feat",             // an alias: the read canonicalizes it to feature
		"mol",              // an alias only bd update --type expands
		"Chore",            // a built-in in another case: the read lower-cases it
	} {
		if _, err := ValidateSettingWrite(issueops.SettingKeyTypesRetained, value); err != nil {
			t.Errorf("ValidateSettingWrite(types.retained, %q) = %v, want it accepted", value, err)
		}
	}
	for _, test := range []struct{ value, wantInError string }{
		{"[op]", "JSON array"},
		{`["op","fact"`, "JSON array"},
		{`[1]`, "JSON array"},
		{`"op"`, "op"},
		{"'op'", "op"},
		{"op fact", "op fact"},
		{`["op fact"]`, "op fact"},
		{`["op,fact"]`, "op,fact"},
		{"op,[fact]", "[fact]"},
		{"null", "names no type"},
		{",", "names no type"},
		{" , ", "names no type"},
		{`[""]`, "names no type"},
	} {
		_, err := ValidateSettingWrite(issueops.SettingKeyTypesRetained, test.value)
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateSettingWrite(types.retained, %q) error = %v, want ErrValidation", test.value, err)
			continue
		}
		if !strings.Contains(err.Error(), test.wantInError) {
			t.Errorf("ValidateSettingWrite(types.retained, %q) error = %q, want it to name %q", test.value, err, test.wantInError)
		}
	}
}

// TestValidateSettingWriteReturnsTheValueUnchanged is the machine half of
// issueops.SetSettingResult.Value's promise: a successful write stores what the
// caller sent. Nothing normalizes today, and this is what would fail if
// something started to.
func TestValidateSettingWriteReturnsTheValueUnchanged(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{"custom.thing", "  spaced  "},
		{"custom.thing", "a,b,c"},
		{"types.custom", "research,session"},
		{"status.custom", "awaiting_review:active"},
		{"claim.pools", ""},
	} {
		got, err := ValidateSettingWrite(test.key, test.value)
		if err != nil {
			t.Fatalf("ValidateSettingWrite(%q, %q): %v", test.key, test.value, err)
		}
		if got != test.value {
			t.Errorf("ValidateSettingWrite(%q, %q) returned %q, want the value unchanged", test.key, test.value, got)
		}
	}
}

// TestValidateSettingWriteBoundsTheKeyAndTheValue pins that the storage bounds
// are the ROLE's, so every leg refuses the same writes with the same typed
// error rather than leaving the refusal to a column (Dolt's VARCHAR(255) key
// and TEXT value) that answers with an unclassifiable driver error.
//
// THE CEILINGS ARE ASSERTED FIRST, because a validator that refused every long
// value would pass the refusal arms on their own: what makes it a BOUND is that
// exactly MaxFieldLen characters and exactly MaxTextBytes bytes are accepted.
// The key is counted in characters and the value in bytes, so each arm also
// drives the unit the other one does not count in.
func TestValidateSettingWriteBoundsTheKeyAndTheValue(t *testing.T) {
	keyAt := strings.Repeat("k", types.MaxFieldLen)
	multibyteKeyAt := strings.Repeat("\u00e9", types.MaxFieldLen) // 510 bytes, 255 characters
	valueAt := strings.Repeat("v", types.MaxTextBytes)
	for _, ok := range []struct{ name, key, value string }{
		{"a key of exactly MaxFieldLen characters", keyAt, "v"},
		{"a multibyte key of exactly MaxFieldLen characters", multibyteKeyAt, "v"},
		{"a value of exactly MaxTextBytes bytes", "custom.big", valueAt},
	} {
		if _, err := ValidateSettingWrite(ok.key, ok.value); err != nil {
			t.Errorf("%s was refused: %v", ok.name, err)
		}
	}
	for _, bad := range []struct{ name, key, value, want string }{
		{"a key one character past MaxFieldLen", keyAt + "k", "v", "config key"},
		{"a multibyte key one character past MaxFieldLen", multibyteKeyAt + "\u00e9", "v", "config key"},
		{"a value one byte past MaxTextBytes", "custom.big", valueAt + "v", "config value"},
		{"a value under MaxTextBytes characters that is past it in bytes", "custom.big",
			strings.Repeat("\u00e9", types.MaxTextBytes/2+1), "config value"},
	} {
		_, err := ValidateSettingWrite(bad.key, bad.value)
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", bad.name, err)
			continue
		}
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Errorf("%s: error = %v, want it to carry types.ErrFieldTooLong too", bad.name, err)
		}
		if !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: message %q does not name %q", bad.name, err, bad.want)
		}
	}
	// The READ and the REMOVE carry no bound: they compare a key and never
	// store it.
	if _, err := ValidateSettingKey(keyAt + "k"); err != nil {
		t.Errorf("ValidateSettingKey(over-long key) = %v, want it accepted: only the write is bounded", err)
	}
}

// TestFilterSettingsEnumerationDropsTheWholeKVPrefix is the machine half of the
// enumeration exclusion. The conformance contract pins it through a real
// backend at all three wirings; what is checkable here is the prefix boundary
// itself, which is where the plausible mistakes are — the narrower memory
// constant, a match that is not anchored, a "kv" that is not "kv.".
func TestFilterSettingsEnumerationDropsTheWholeKVPrefix(t *testing.T) {
	stored := map[string]string{
		"issue_prefix":                 "bd",
		"custom.thing":                 "kept",
		"status.custom":                "awaiting_review",
		"kv.deploy-token":              "a generic bd kv row",
		"kv.memory.architecture":       "the deploy token is sk-live-000",
		"kv.memory.issue_prefix":       "a memory shadowing a settings name",
		"kvetch":                       "not the prefix: kv. carries the dot",
		"memory.not-under-kv":          "not the prefix either",
		"custom.mentions.kv.somewhere": "the prefix has to be at the front",
	}
	want := map[string]string{
		"issue_prefix":                 "bd",
		"custom.thing":                 "kept",
		"status.custom":                "awaiting_review",
		"kvetch":                       "not the prefix: kv. carries the dot",
		"memory.not-under-kv":          "not the prefix either",
		"custom.mentions.kv.somewhere": "the prefix has to be at the front",
	}

	got := FilterSettingsEnumeration(stored)
	if len(got) != len(want) {
		t.Fatalf("FilterSettingsEnumeration returned %d keys %v, want %d %v", len(got), got, len(want), want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("FilterSettingsEnumeration()[%q] = %q, want %q", key, got[key], value)
		}
	}
	// The argument belongs to the caller, and one body passes the map its store
	// handed back: filtering in place would empty the store's own answer.
	if len(stored) != 9 {
		t.Errorf("FilterSettingsEnumeration mutated its argument: %d keys left, want 9", len(stored))
	}
}

// TestFilterSettingsEnumerationIsEmptyNeverNil pins the half of the promise the
// two bodies used to make separately: ListSettingsResult.Settings can be ranged
// over without a guard, and at least one store path answers nil.
func TestFilterSettingsEnumerationIsEmptyNeverNil(t *testing.T) {
	for name, stored := range map[string]map[string]string{
		"nil":          nil,
		"empty":        {},
		"only kv rows": {"kv.memory.a": "1", "kv.b": "2"},
	} {
		got := FilterSettingsEnumeration(stored)
		if got == nil {
			t.Errorf("FilterSettingsEnumeration(%s) returned a nil map, want an empty one", name)
		}
		if len(got) != 0 {
			t.Errorf("FilterSettingsEnumeration(%s) = %v, want empty", name, got)
		}
	}
}

// TestBothSettingsFiltersDrawTheSameBoundary is the guard against the failure
// mode a second predicate would introduce: an enumeration that hides a key a
// point read still hands over. The two filters are the two halves of one
// firewall, so they are asserted against ONE key list rather than against each
// other's expectations.
func TestBothSettingsFiltersDrawTheSameBoundary(t *testing.T) {
	for key, onThePlane := range map[string]bool{
		"kv.deploy-token":              true,
		"kv.memory.architecture":       true,
		"kv.":                          true,
		"issue_prefix":                 false,
		"status.custom":                false,
		"kvetch":                       false,
		"memory.not-under-kv":          false,
		"custom.mentions.kv.somewhere": false,
		"":                             false,
	} {
		if got := KeyIsOnTheKVPlane(key); got != onThePlane {
			t.Errorf("KeyIsOnTheKVPlane(%q) = %v, want %v", key, got, onThePlane)
		}
		_, enumerated := FilterSettingsEnumeration(map[string]string{key: "v"})[key]
		if enumerated == onThePlane {
			t.Errorf("FilterSettingsEnumeration kept %q = %v, want kept = %v", key, enumerated, !onThePlane)
		}
		_, refused := FilterSettingsPointRead(key)
		if refused != onThePlane {
			t.Errorf("FilterSettingsPointRead(%q) refused = %v, want %v — the two filters must draw the same boundary",
				key, refused, onThePlane)
		}
	}
}

// TestFilterSettingsPointReadAnswersExactlyLikeAnAbsentKey pins what makes the
// refusal a refusal a caller cannot detect: the absent-key answer, which
// SettingResult.Value documents as the echoed key, "" and a nil error. There is
// no ErrNotFound on this role to return instead, and an error of any kind would
// confirm the guessed key to the caller.
func TestFilterSettingsPointReadAnswersExactlyLikeAnAbsentKey(t *testing.T) {
	const key = "kv.memory.deploy-notes"

	result, refused := FilterSettingsPointRead(key)
	if !refused {
		t.Fatalf("FilterSettingsPointRead(%q) let the key through", key)
	}
	if want := (issueops.SettingResult{Key: key}); result != want {
		t.Errorf("FilterSettingsPointRead(%q) = %+v, want %+v — the same result a key nothing stored produces", key, result, want)
	}
}

// TestFilterSettingsPointReadLeavesASettingToTheStore pins the other direction:
// the filter must not answer for a key it does not own, or every settings read
// would return "" without ever reaching the database.
func TestFilterSettingsPointReadLeavesASettingToTheStore(t *testing.T) {
	for _, key := range []string{"issue_prefix", "status.custom", "custom.thing"} {
		result, refused := FilterSettingsPointRead(key)
		if refused {
			t.Errorf("FilterSettingsPointRead(%q) refused a settings key", key)
		}
		if result != (issueops.SettingResult{}) {
			t.Errorf("FilterSettingsPointRead(%q) = %+v alongside refused=false, want the zero result the caller ignores", key, result)
		}
	}
}
