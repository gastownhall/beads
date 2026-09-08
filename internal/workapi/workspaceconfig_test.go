package workapi

import (
	"errors"
	"strings"
	"testing"

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
