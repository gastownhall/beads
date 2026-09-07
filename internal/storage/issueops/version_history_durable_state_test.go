package issueops

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
)

// TestCanonicalDurableStateIsJCS pins that canonicalDurableState -- the one
// helper RecordVersionInTx stores through -- emits the RFC 8785 (JCS) form,
// not encoding/json's, and that the difference is real: the same value
// marshalled without the JCS step yields different bytes.
//
// Why this matters (donnabox on gastownhall/beads#6358 item 4): the token
// the design derives from durable_state (#5898, sha256-jcs) is only stable if
// the bytes hashed are the bytes stored. Migration 0068 step 7 makes the
// column a LONGBLOB so storage keeps bytes verbatim; this test covers the
// other half, that the writer's bytes are the canonical ones in the first
// place, so the token is a function of content and not of whatever
// encoding/json or a caller-supplied json.Number happened to emit.
//
// The inputs use json.Number and json.RawMessage deliberately: they are the
// only way to get encoding/json to emit a non-canonical number form such as
// 1.0 or 1E300 verbatim (a float64 1.0 already marshals as 1), which is
// exactly the input a JSON column would have renormalized on its own.
func TestCanonicalDurableStateIsJCS(t *testing.T) {
	t.Parallel()

	// Issue-shaped: string id, nested object, list, null, and the number
	// forms that Dolt's JSON type is known to renormalize.
	state := map[string]any{
		"id":       "bd-1",
		"priority": json.Number("1.0"),
		"metadata": map[string]any{
			"weight":  json.Number("1.50"),
			"huge":    json.Number("1E300"),
			"ordinal": json.Number("9007199254740993"), // 2^53 + 1
			"tags":    []any{json.Number("2.0"), "b", nil},
		},
		"raw": json.RawMessage(`{ "z" : 10.0e0 , "a" : true }`),
	}

	got, err := canonicalDurableState(state)
	if err != nil {
		t.Fatalf("canonicalDurableState: %v", err)
	}

	// RFC 8785: keys sorted, no whitespace, numbers in ES6 Number::toString
	// form (1.0 -> 1, 1.50 -> 1.5, 1E300 -> 1e+300, 2^53+1 -> the double it
	// rounds to, 10.0e0 -> 10).
	want := []byte(`{"id":"bd-1","metadata":{"huge":1e+300,"ordinal":9007199254740992,"tags":[2,"b",null],"weight":1.5},"priority":1,"raw":{"a":true,"z":10}}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("canonicalDurableState =\n  %s\nwant\n  %s", got, want)
	}

	// The JCS step is doing work: a plain marshal of the same value keeps
	// the caller's number forms and spacing, so its bytes differ.
	plain, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Equal(plain, got) {
		t.Fatalf("plain json.Marshal already equals the canonical form (%s); this test no longer proves canonicalization matters", plain)
	}
	for _, nonCanonical := range []string{"1.0", "1.50", "1E300", "9007199254740993", "10.0e0"} {
		if !bytes.Contains(plain, []byte(nonCanonical)) {
			t.Errorf("plain marshal lost the non-canonical form %q, so the fixture no longer exercises it: %s", nonCanonical, plain)
		}
		if bytes.Contains(got, []byte(nonCanonical)) {
			t.Errorf("canonical form still carries the non-canonical form %q: %s", nonCanonical, got)
		}
	}

	// Determinism: the token is a function of the content. A second pass
	// over the same value, and over the already-canonical bytes, hashes
	// identically.
	again, err := canonicalDurableState(state)
	if err != nil {
		t.Fatalf("canonicalDurableState (second pass): %v", err)
	}
	if sha256.Sum256(again) != sha256.Sum256(got) {
		t.Fatalf("canonicalDurableState is not deterministic:\n  %s\n  %s", got, again)
	}
	idempotent, err := canonicalDurableState(json.RawMessage(got))
	if err != nil {
		t.Fatalf("canonicalDurableState over its own output: %v", err)
	}
	if !bytes.Equal(idempotent, got) {
		t.Fatalf("canonical form is not a fixed point:\n  %s\n  %s", got, idempotent)
	}
}

// TestCanonicalDurableStateRejectsUnmarshalableState pins that a value
// encoding/json cannot marshal surfaces as an error rather than as an empty
// or partial snapshot, so RecordVersionInTx fails the transaction instead of
// minting a version row whose durable_state says nothing.
func TestCanonicalDurableStateRejectsUnmarshalableState(t *testing.T) {
	t.Parallel()

	if _, err := canonicalDurableState(map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("canonicalDurableState(unmarshalable) = nil error, want an error")
	}
}
