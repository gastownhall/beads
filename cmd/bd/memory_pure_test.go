// Pure-Go tests for memory command helpers. No cgo / Dolt dependency, so this
// file carries no build tag and compiles under CGO_ENABLED=0 with gms_pure_go.

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMatchesKnownCommand guards the `bd remember <subcommand>` misfire fix
// (GH#4401): a bare word that names a real top-level command must be flagged so
// it is not silently stored as memory content, while genuine multi-word
// insights (and arbitrary single words) must pass through untouched.
func TestMatchesKnownCommand(t *testing.T) {
	cases := []struct {
		name     string
		insight  string
		wantName string
		wantHit  bool
	}{
		{"sibling command recall", "recall", "recall", true},
		{"sibling command forget", "forget", "forget", true},
		{"sibling command memories", "memories", "memories", true},
		{"remember itself", "remember", "remember", true},
		{"case insensitive", "ReCall", "recall", true},
		{"multi-word insight", "always run tests with the -race flag", "", false},
		{"leading command word in a phrase", "recall the auth design", "", false},
		{"non-command single word", "zorblax", "", false},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotHit := matchesKnownCommand(rememberCmd, tc.insight)
			if gotHit != tc.wantHit {
				t.Fatalf("matchesKnownCommand(%q) hit = %v, want %v", tc.insight, gotHit, tc.wantHit)
			}
			if gotHit && gotName != tc.wantName {
				t.Errorf("matchesKnownCommand(%q) name = %q, want %q", tc.insight, gotName, tc.wantName)
			}
		})
	}
}

// TestIsBareMemoryKey pins the read-detection grammar for `bd remember`'s
// bare-key path (GH#7275): lowercase alphanumeric segments joined by single
// '.' or '-' separators, with no byte or segment cap. It is a grammar, not a
// derivation, so the keys DeriveKey rewrites (dots, nine-plus segments, over
// 60 bytes) are still reads — and everything else keeps the content path.
func TestIsBareMemoryKey(t *testing.T) {
	// Reads: every key the old round-trip gate accepted (a derived key is
	// always a bare key), plus the shapes derivation rewrites.
	reads := []string{
		"dolt-phantoms",                       // canonical short slug; round-trips through DeriveKey
		"always-run-tests-with-the-race-flag", // an eight-segment derived key
		"singleword",
		"123",
		"config.v2", // dot separators
		"a-b.c-d",   // mixed separators
		"one-two-three-four-five-six-seven-eight-nine", // nine segments; DeriveKey keeps eight
		strings.Repeat("m", 61),                        // one segment past the 60-byte cap
	}
	for _, value := range reads {
		if !isBareMemoryKey(value) {
			t.Errorf("isBareMemoryKey(%q) = false, want it read as a bare key", value)
		}
	}
	// Content: uppercase, underscores, whitespace, Unicode, irregular
	// separators — and the separator-only tokens, which must stay on the
	// content path so they keep the role's underivable-content refusal.
	content := []string{
		"", "   ", " slug", "slug\n", "a\tb", "two words",
		"CAPS", "a_b", "café", "!!!",
		".", "-", // separator-only: no key there to recall, and DeriveKey("") is ""
		"trail-", "-lead", "a--b", "a..b", "x.", ".x",
	}
	for _, value := range content {
		if isBareMemoryKey(value) {
			t.Errorf("isBareMemoryKey(%q) = true, want it to stay on the content path", value)
		}
	}
}

// TestPrintRecallResult guards the bd-5963 fix: presence must come from
// `RecallResult.Found` (row existence), never from `value != ""`. Before the
// fix, `bd recall` on a memory stored as the empty string printed "No memory
// with key" and exited 1 even though the row was there and `bd forget` would
// delete it — the CLI disagreed with the role and with the HTTP door about
// what "found" means for that row.
func TestPrintRecallResult(t *testing.T) {
	t.Run("found with empty value prints the empty value and exits clean", func(t *testing.T) {
		savedJSONOutput := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = savedJSONOutput }()

		var err error
		out := captureStdout(t, func() error {
			err = printRecallResult("empty-key", "", true)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "\n" {
			t.Errorf("stdout = %q, want a single newline (the empty value)", out)
		}
	})

	t.Run("found with empty value in JSON reports found true", func(t *testing.T) {
		savedJSONOutput := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = savedJSONOutput }()

		var err error
		out := captureStdout(t, func() error {
			err = printRecallResult("empty-key", "", true)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var body map[string]interface{}
		if uerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &body); uerr != nil {
			t.Fatalf("unmarshal %q: %v", out, uerr)
		}
		if body["found"] != true {
			t.Errorf("found = %v, want true", body["found"])
		}
		if body["value"] != "" {
			t.Errorf("value = %v, want \"\"", body["value"])
		}
		if body["key"] != "empty-key" {
			t.Errorf("key = %v, want %q", body["key"], "empty-key")
		}
	})

	t.Run("found with non-empty value is unchanged", func(t *testing.T) {
		savedJSONOutput := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = savedJSONOutput }()

		var err error
		out := captureStdout(t, func() error {
			err = printRecallResult("k", "hello", true)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "hello\n" {
			t.Errorf("stdout = %q, want %q", out, "hello\n")
		}
	})

	t.Run("not found prints the miss and exits 1", func(t *testing.T) {
		savedJSONOutput := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = savedJSONOutput }()

		var err error
		stderr := captureStderr(t, func() {
			err = printRecallResult("gone", "", false)
		})
		if !strings.Contains(stderr, `No memory with key "gone"`) {
			t.Errorf("stderr = %q, want it to name the missing key", stderr)
		}
		ee, ok := err.(*exitError)
		if !ok || ee.Code != 1 {
			t.Errorf("err = %v, want *exitError{Code: 1}", err)
		}
	})

	t.Run("not found in JSON reports found false and exits 1", func(t *testing.T) {
		savedJSONOutput := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = savedJSONOutput }()

		var err error
		out := captureStdout(t, func() error {
			err = printRecallResult("gone", "", false)
			return nil
		})
		var body map[string]interface{}
		if uerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &body); uerr != nil {
			t.Fatalf("unmarshal %q: %v", out, uerr)
		}
		if body["found"] != false {
			t.Errorf("found = %v, want false", body["found"])
		}
		ee, ok := err.(*exitError)
		if !ok || ee.Code != 1 {
			t.Errorf("err = %v, want *exitError{Code: 1}", err)
		}
	})
}

// TestRememberBareKeyPath guards the same presence rule on the
// `bd remember <bare-slug>` read path: a bare slug naming a memory stored as
// the empty string recalls it, and only a slug naming nothing is refused.
func TestRememberBareKeyPath(t *testing.T) {
	savedJSONOutput := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = savedJSONOutput }()

	t.Run("found with empty value recalls it", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() error {
			err = rememberBareKeyPath("empty-key", "empty-key", "", true)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var body map[string]interface{}
		if uerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &body); uerr != nil {
			t.Fatalf("unmarshal %q: %v", out, uerr)
		}
		if body["action"] != "recalled" || body["found"] != true || body["value"] != "" {
			t.Errorf("body = %v, want action recalled, found true, empty value", body)
		}
	})

	t.Run("not found is refused", func(t *testing.T) {
		var err error
		captureStdout(t, func() error {
			err = rememberBareKeyPath("gone", "gone", "", false)
			return nil
		})
		ee, ok := err.(*exitError)
		if !ok || ee.Code != 1 {
			t.Errorf("err = %v, want *exitError{Code: 1}", err)
		}
	})

	// The text refusal names bd remember and bd memories bare, since a
	// pasted backtick pair runs what it holds, and ends with the command that
	// stores the token anyway, so a copy of it carries no sentence period
	// into the key.
	t.Run("not found refusal ends with its command", func(t *testing.T) {
		jsonOutput = false
		defer func() { jsonOutput = true }()
		var err error
		out := captureStderr(t, func() {
			err = rememberBareKeyPath("auth-flow", "auth-flow", "", false)
		})
		if err == nil {
			t.Fatal("rememberBareKeyPath stored a bare key naming nothing")
		}
		if strings.Contains(out, "`") {
			t.Errorf("refusal wraps a command in backticks:\n%s", out)
		}
		if want := `To store it anyway, run: bd remember "auth-flow" --key auth-flow` + "\n"; !strings.HasSuffix(out, want) {
			t.Errorf("refusal = %q\nwant it to end %q", out, want)
		}
	})
}
