package memoryapi

import (
	"errors"
	"maps"
	"testing"

	"github.com/steveyegge/beads/memoryops"
)

// TestDeriveKeyGoldenTable is the pin that matters most in this package.
//
// DeriveKey has minted every auto-generated memory key since `bd remember`
// shipped, and the keys it minted are STORED: a change here does not fail, it
// re-keys. `bd remember "always run tests with -race flag"` would start writing
// a second row beside the memory the user has been updating for months, both
// rows would show up in `bd memories`, and nothing would report a problem.
//
// So this table is GOLDEN. Every row was produced by the shipped slugify, not
// by reading the code, and a row that changes is a decision about existing
// workspaces rather than a test to update. The rows are grouped by the property
// each one holds down.
func TestDeriveKeyGoldenTable(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		// The everyday shape, including the two examples in the command's own
		// help text — the keys agents have in their notes.
		{"help text example", "always run tests with -race flag", "always-run-tests-with-race-flag"},
		{"prose insight", "Dolt phantom DBs hide in three places", "dolt-phantom-dbs-hide-in-three-places"},
		{"a bare slug round-trips", "dolt-phantoms", "dolt-phantoms"},

		// Case folding and separator collapsing: one hyphen per RUN, whatever
		// the run was made of.
		{"uppercase folds", "UPPER CASE SHOUTING", "upper-case-shouting"},
		{"mixed whitespace collapses", "a  b\tc\nd", "a-b-c-d"},
		{"dots collapse", "v1.2.3 release notes", "v1-2-3-release-notes"},
		{"underscores collapse", "snake_case_words here", "snake-case-words-here"},
		{"existing hyphens are just separators", "hyphenated-word stays split", "hyphenated-word-stays-split"},
		{"digits survive", "12345 67890", "12345-67890"},

		// The eight-segment truncation, from both sides of the boundary. The
		// tail is DROPPED, not folded into the eighth segment.
		{"eight segments are kept whole", "one two three four five six seven eight", "one-two-three-four-five-six-seven-eight"},
		{"the ninth is dropped", "one two three four five six seven eight nine", "one-two-three-four-five-six-seven-eight"},
		{"and so is everything after the tenth", "one two three four five six seven eight nine ten eleven", "one-two-three-four-five-six-seven-eight"},
		{"segments from hyphens count the same", "one two three four five six seven eight-nine-ten", "one-two-three-four-five-six-seven-eight"},
		{"real prose hits the limit too", "the quick brown fox jumps over the lazy dog and keeps running forever", "the-quick-brown-fox-jumps-over-the-lazy"},

		// The 60-byte cap, and the trailing-hyphen trim that only fires when
		// the cut lands exactly on a separator.
		{"cap cuts mid-word", "supercalifragilisticexpialidocious antidisestablishmentarianism pneumonoultramicroscopic", "supercalifragilisticexpialidocious-antidisestablishmentarian"},
		{"cap cuts mid-word again", "aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd eeeeeeeeee ffffffffff", "aaaaaaaaaa-bbbbbbbbbb-cccccccccc-dddddddddd-eeeeeeeeee-fffff"},
		{"cap on a separator trims it", "aaaaaaaaaaa bbbbbbbbbbb ccccccccccc ddddddddddd eeeeeeeeeee fff", "aaaaaaaaaaa-bbbbbbbbbbb-ccccccccccc-ddddddddddd-eeeeeeeeeee"},
		{"one long word is capped", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-tail", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},

		// Non-ASCII is not transliterated, it is SEPARATOR. An accented word
		// shatters into fragments, and a script with no ASCII at all derives
		// nothing — which is the case ResolveKey has to refuse.
		{"accents shatter the word", "Café naïve résumé", "caf-na-ve-r-sum"},
		{"non-latin derives nothing", "日本語のメモ", ""},

		// Nothing to derive from.
		{"punctuation only", "!!!", ""},
		{"hyphens only", "---", ""},
		{"whitespace only", "   ", ""},
		{"empty", "", ""},
		{"trailing punctuation is trimmed", "trailing punctuation!!!", "trailing-punctuation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DeriveKey(test.content); got != test.want {
				t.Fatalf("DeriveKey(%q) = %q, want %q\n"+
					"This is a GOLDEN table: if the change was deliberate, it re-keys every "+
					"memory whose content derives differently, and the old rows stay behind.",
					test.content, got, test.want)
			}
		})
	}
}

// TestDeriveKeyNeverExceedsTheCap is the property behind three of the rows
// above, asserted over all of them at once so a future row cannot quietly widen
// the key space past what the config key column and the merge resolver have
// been carrying.
func TestDeriveKeyNeverExceedsTheCap(t *testing.T) {
	for _, content := range []string{
		"supercalifragilisticexpialidocious antidisestablishmentarianism pneumonoultramicroscopic",
		"aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd eeeeeeeeee ffffffffff",
		"aaaaaaaaaaa bbbbbbbbbbb ccccccccccc ddddddddddd eeeeeeeeeee fff",
	} {
		got := DeriveKey(content)
		if len(got) > 60 {
			t.Fatalf("DeriveKey(%q) is %d bytes, want at most 60", content, len(got))
		}
		if got != "" && got[len(got)-1] == '-' {
			t.Fatalf("DeriveKey(%q) = %q ends on the separator", content, got)
		}
	}
}

// TestValidateContentRefusesNothingToRemember pins the first refusal as the
// SENTINEL rather than the message: the front doors and the HTTP problem
// classifier both branch on errors.Is, and a body that wrapped this error would
// keep them working while a string comparison would not.
func TestValidateContentRefusesNothingToRemember(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		if err := ValidateContent(blank); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("ValidateContent(%q) = %v, want ErrValidation", blank, err)
		}
	}
	if err := ValidateContent(" x "); err != nil {
		t.Fatalf("ValidateContent(%q) = %v, want success: trimming decides the refusal, not the storage", " x ", err)
	}
}

// TestValidateKeyReturnsTheKeyUntouched pins that this validator does not
// normalize. A key with surrounding space is a key someone can hold, and
// trimming it here would read a different row from the one `bd remember --key`
// wrote.
func TestValidateKeyReturnsTheKeyUntouched(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		if _, err := ValidateKey(blank); !errors.Is(err, memoryops.ErrValidation) {
			t.Fatalf("ValidateKey(%q) = %v, want ErrValidation", blank, err)
		}
	}
	for _, key := range []string{" padded ", "Has Spaces.✓", "dolt-phantoms", "issue_prefix"} {
		got, err := ValidateKey(key)
		if err != nil {
			t.Fatalf("ValidateKey(%q) = %v, want success", key, err)
		}
		if got != key {
			t.Fatalf("ValidateKey(%q) = %q, want the key unchanged", key, got)
		}
	}
}

// TestResolveKeyPrefersTheExplicitKey pins the order: an explicit key wins even
// when it is content-shaped, and content is only derived from when none was
// given.
func TestResolveKeyPrefersTheExplicitKey(t *testing.T) {
	got, err := ResolveKey("Has Spaces.✓", "always run tests with -race flag")
	if err != nil {
		t.Fatalf("ResolveKey with an explicit key = %v, want success", err)
	}
	if got != "Has Spaces.✓" {
		t.Fatalf("ResolveKey = %q, want the explicit key verbatim", got)
	}

	got, err = ResolveKey("", "always run tests with -race flag")
	if err != nil {
		t.Fatalf("ResolveKey with no key = %v, want success", err)
	}
	if got != "always-run-tests-with-race-flag" {
		t.Fatalf("ResolveKey = %q, want the derivation", got)
	}
}

// TestResolveKeyRefusesWhatItCannotName pins both refusals at the one seam
// every implementation goes through, including the ORDER: empty content is
// refused as empty content even though it also derives to nothing, because that
// is the message a caller can act on.
func TestResolveKeyRefusesWhatItCannotName(t *testing.T) {
	if _, err := ResolveKey("", "  "); !errors.Is(err, memoryops.ErrValidation) {
		t.Fatalf("ResolveKey(\"\", blank) = %v, want ErrValidation", err)
	}
	if _, err := ResolveKey("", "!!!"); !errors.Is(err, memoryops.ErrValidation) {
		t.Fatalf("ResolveKey(\"\", \"!!!\") = %v, want ErrValidation", err)
	}
	// An explicit key rescues underivable content: that is what --key is for.
	if _, err := ResolveKey("shouting", "!!!"); err != nil {
		t.Fatalf("ResolveKey(\"shouting\", \"!!!\") = %v, want success", err)
	}
}

// TestFilterMemoriesFoldsBothSides pins the property that made this function
// worth extracting: the caller passes the term the user typed, and matching is
// case-insensitive on the key AND the value.
func TestFilterMemoriesFoldsBothSides(t *testing.T) {
	all := map[string]string{
		"dolt-phantoms": "Dolt phantom DBs hide in three places",
		"auth-jwt":      "auth module uses JWT not sessions",
		"race-flag":     "always run tests with -race flag",
	}

	for _, test := range []struct {
		name   string
		search string
		want   []string
	}{
		{"uppercase term matches a lowercase key", "DOLT", []string{"dolt-phantoms"}},
		{"lowercase term matches an uppercase value", "jwt", []string{"auth-jwt"}},
		{"a term matching only the value still matches", "sessions", []string{"auth-jwt"}},
		{"empty search is everything", "", []string{"auth-jwt", "dolt-phantoms", "race-flag"}},
		{"a miss is empty, not nil", "nothing-here", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := FilterMemories(all, test.search)
			if got == nil {
				t.Fatal("FilterMemories returned a nil map; the contract promises an empty one")
			}
			if len(got) != len(test.want) {
				t.Fatalf("FilterMemories(%q) = %v, want keys %v", test.search, got, test.want)
			}
			for _, key := range test.want {
				if got[key] != all[key] {
					t.Fatalf("FilterMemories(%q)[%q] = %q, want %q", test.search, key, got[key], all[key])
				}
			}
		})
	}
}

// TestFilterMemoriesDoesNotAliasItsArgument pins that a caller holding the
// whole plane still holds it after asking a narrower question. Returning the
// argument map for an empty search would be the cheap version, and it would
// make one caller's filtering visible to another.
func TestFilterMemoriesDoesNotAliasItsArgument(t *testing.T) {
	all := map[string]string{"a": "1", "b": "2"}
	before := maps.Clone(all)

	filtered := FilterMemories(all, "")
	filtered["c"] = "3"
	delete(filtered, "a")

	if !maps.Equal(all, before) {
		t.Fatalf("FilterMemories mutated its argument: %v, want %v", all, before)
	}
}

// TestResolveKeyRefusesAWhitespaceOnlyExplicitKey pins the rule Remember shares
// with Recall and Forget.
//
// ValidateKey refuses a key that is empty after trimming, and Recall and Forget
// both run it. ResolveKey used to accept any non-empty string, so
// `--key "   "` minted a row that no memory operation could name again:
// enumerable by List forever, unrecallable, unforgettable, reachable only
// through `bd config unset` on the raw storage key. Four separate review lenses
// found it independently.
//
// The keys that SURVIVE matter as much as the ones refused: surrounding space
// is preserved, because the rule is that a key must name something, not that it
// must be tidy.
func TestResolveKeyRefusesAWhitespaceOnlyExplicitKey(t *testing.T) {
	const content = "some content that derives fine"
	for _, key := range []string{" ", "   ", "\t", "\n", " \t\n "} {
		if _, err := ResolveKey(key, content); !errors.Is(err, memoryops.ErrValidation) {
			t.Errorf("ResolveKey(%q) error = %v, want ErrValidation: a key no read can name must not be writable", key, err)
		}
	}
	for _, key := range []string{" leading", "trailing ", " both ", "has space"} {
		got, err := ResolveKey(key, content)
		if err != nil {
			t.Errorf("ResolveKey(%q) error = %v, want the key verbatim", key, err)
			continue
		}
		if got != key {
			t.Errorf("ResolveKey(%q) = %q, want it byte for byte: surrounding space is preserved", key, got)
		}
	}
}
