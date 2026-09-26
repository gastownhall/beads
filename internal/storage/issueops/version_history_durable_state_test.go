package issueops

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanonicalDurableStateIsJCS pins that canonicalDurableState -- the one
// helper RecordVersionInTx stores through -- emits the RFC 8785 (JCS) form,
// not encoding/json's, and that the difference is real: the same value
// marshaled without the JCS step yields different bytes.
//
// Why this matters (donnabox on gastownhall/beads#6358 item 4): the token
// the design derives from durable_state (#5898, sha256-jcs) is only stable if
// the bytes hashed are the bytes stored. Migration 0068 step 7 makes the
// column a LONGBLOB so storage keeps bytes verbatim; this test covers the
// other half, that the writer's bytes are the canonical ones in the first
// place, so the token is a function of content and not of whatever
// encoding/json or a caller-supplied json.Number happened to emit.
//
// The number inputs use json.Number and json.RawMessage deliberately: they
// are the only way to get encoding/json to emit a non-canonical number form
// such as 1.0 or 1E300 verbatim (a float64 1.0 already marshals as 1), which
// is exactly the input a JSON column would have renormalized on its own.
//
// Numbers are one axis. The rest of the pin is table-driven: the RFC 8785
// test vectors that ship with gowebpki/jcs v1.0.1 (testdata/jcs, see its
// README.md), a key-ordering case the vectors leave implicit (RFC 8785
// sorts keys by UTF-16 code units, so an astral-plane key sorts AFTER "é"
// even though its first byte is smaller), and the one place encoding/json
// and RFC 8785 disagree for ordinary issue text: encoding/json escapes <, >
// and & as \u003c-style sequences and JCS does not, so the stored bytes must
// be the JCS form or the token would depend on which marshaler ran first.
// Every case is also checked to be a fixed point of canonicalization, and a
// duplicate-key input is refused rather than silently resolved.
func TestCanonicalDurableStateIsJCS(t *testing.T) {
	t.Parallel()

	cases := append(jcsTestVectors(t),
		jcsCase{
			name:  "unicode and astral keys sort by UTF-16 code units",
			state: json.RawMessage(`{"b":1,"a":2,"é":3,"A":4,"😀":5}`),
			want:  []byte(`{"A":4,"a":2,"b":1,"é":3,"😀":5}`),
		},
		jcsCase{
			name:  "html escapes are normalized away",
			state: map[string]any{"s": `<a>&"'</a>`},
			want:  []byte(`{"s":"<a>&\"'</a>"}`),
		},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalDurableState(tc.state)
			if err != nil {
				t.Fatalf("canonicalDurableState: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("canonicalDurableState =\n  %s\nwant\n  %s", got, tc.want)
			}
			// canonical(canonical(x)) == canonical(x): the stored bytes are
			// a fixed point, so re-canonicalizing what was read back can
			// never change the token.
			again, err := canonicalDurableState(json.RawMessage(got))
			if err != nil {
				t.Fatalf("canonicalDurableState over its own output: %v", err)
			}
			if !bytes.Equal(again, got) {
				t.Fatalf("canonical form is not a fixed point:\n  %s\n  %s", got, again)
			}
		})
	}

	t.Run("plain marshal html-escapes and the canonical form does not", func(t *testing.T) {
		t.Parallel()
		// The normalization above is doing work: the plain marshal of the
		// same value carries encoding/json's HTML-safe escapes, which RFC
		// 8785 does not permit (only the escapes it requires survive).
		plain, err := json.Marshal(map[string]any{"s": `<a>&"'</a>`})
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if !bytes.Contains(plain, []byte(`\u003c`)) || !bytes.Contains(plain, []byte(`\u0026`)) {
			t.Fatalf("plain json.Marshal no longer HTML-escapes (%s); the fixture no longer exercises the normalization", plain)
		}
		got, err := canonicalDurableState(json.RawMessage(plain))
		if err != nil {
			t.Fatalf("canonicalDurableState: %v", err)
		}
		if bytes.Contains(got, []byte(`\u00`)) {
			t.Fatalf("canonical form still carries a \\u00xx escape: %s", got)
		}
	})

	t.Run("duplicate keys are rejected", func(t *testing.T) {
		t.Parallel()
		// RFC 8785 has no answer for a duplicate key (section 3.1 forbids
		// them), and encoding/json's compaction lets the input through, so
		// the JCS step is the only place a snapshot with two "k"s can be
		// refused instead of stored with whichever value happened to win.
		_, err := canonicalDurableState(json.RawMessage(`{"k":1,"k":2}`))
		if err == nil {
			t.Fatal("canonicalDurableState(duplicate keys) = nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), "Duplicate key") {
			t.Fatalf("canonicalDurableState(duplicate keys) error = %q, want it to name the duplicate key", err)
		}
	})

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

// jcsCase is one canonicalization pin: what RecordVersionInTx would hand to
// canonicalDurableState, and the exact bytes RFC 8785 says come out.
type jcsCase struct {
	name  string
	state any
	want  []byte
}

// jcsTestVectors loads the RFC 8785 input/expected pairs under testdata/jcs
// (provenance in its README.md; the upstream "output" directory is named
// "expected" here because the repository's .gitignore drops any output/
// tree). The count is pinned so an emptied or half-copied directory cannot
// pass as a vacuous table.
func jcsTestVectors(t *testing.T) []jcsCase {
	t.Helper()
	const shipped = 10 // the vectors in gowebpki/jcs v1.0.1's testdata
	dir := filepath.Join("testdata", "jcs")
	entries, err := os.ReadDir(filepath.Join(dir, "input"))
	if err != nil {
		t.Fatalf("list RFC 8785 vectors: %v", err)
	}
	var cases []jcsCase
	for _, entry := range entries {
		input, err := os.ReadFile(filepath.Join(dir, "input", entry.Name()))
		if err != nil {
			t.Fatalf("read RFC 8785 vector input: %v", err)
		}
		want, err := os.ReadFile(filepath.Join(dir, "expected", entry.Name()))
		if err != nil {
			t.Fatalf("read RFC 8785 vector output: %v", err)
		}
		cases = append(cases, jcsCase{name: "rfc8785 vector " + entry.Name(), state: json.RawMessage(input), want: want})
	}
	if len(cases) != shipped {
		t.Fatalf("found %d RFC 8785 vectors under %s, want the %d that ship with gowebpki/jcs v1.0.1", len(cases), dir, shipped)
	}
	return cases
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
