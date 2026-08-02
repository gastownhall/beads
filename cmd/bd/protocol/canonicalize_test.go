package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalizeNormalizesTimestamps(t *testing.T) {
	in := []byte(`{"id":"x","created_at":"2026-06-25T00:15:41Z","updated_at":"2026-06-25T00:15:41.123456Z","title":"not a 2026-06-25 date"}`)
	out, err := CanonicalizeJSON(in)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "2026-06-25T00:15:41Z") || strings.Contains(s, "2026-06-25T00:15:41.123456Z") {
		t.Fatalf("timestamps not normalized:\n%s", s)
	}
	if !strings.Contains(s, `"<TS>"`) {
		t.Fatalf("expected <TS> placeholder:\n%s", s)
	}
	// A string that merely embeds a date is not a timestamp value and must survive.
	if !strings.Contains(s, "not a 2026-06-25 date") {
		t.Fatalf("non-timestamp string was wrongly rewritten:\n%s", s)
	}
}

func TestCanonicalizeSortsObjectArrays(t *testing.T) {
	// Same logical set, different order -> identical canonical bytes.
	a := []byte(`[{"id":"b"},{"id":"a"},{"id":"c"}]`)
	b := []byte(`[{"id":"c"},{"id":"a"},{"id":"b"}]`)
	ca, err := CanonicalizeJSON(a)
	if err != nil {
		t.Fatalf("canonicalize a: %v", err)
	}
	cb, err := CanonicalizeJSON(b)
	if err != nil {
		t.Fatalf("canonicalize b: %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("array ordering not normalized:\n%s\nvs\n%s", ca, cb)
	}
}

func TestCanonicalizeIdempotent(t *testing.T) {
	in := []byte(`{"b":2,"a":1,"items":[{"id":"y","t":"2026-06-25T00:15:41Z"},{"id":"x"}]}`)
	once, err := CanonicalizeJSON(in)
	if err != nil {
		t.Fatalf("canonicalize once: %v", err)
	}
	twice, err := CanonicalizeJSON(once)
	if err != nil {
		t.Fatalf("canonicalize twice: %v", err)
	}
	if !bytes.Equal(once, twice) {
		t.Fatalf("canonicalize not idempotent:\n%s\nvs\n%s", once, twice)
	}
}

func TestCanonicalizeRejectsTrailingText(t *testing.T) {
	// A JSON payload followed by a warning line must be rejected, not silently
	// truncated to the leading value — otherwise a command that emitted valid
	// JSON plus a warning would pass the corpus gate while real consumers choke.
	if _, err := CanonicalizeJSON([]byte("{\"ok\":true}\nwarning: partial write\n")); err == nil {
		t.Fatal("expected error for trailing non-JSON text, got nil")
	}
}

func TestCanonicalizeRejectsMultipleValues(t *testing.T) {
	// Two concatenated JSON values (e.g. a double-printed result) must be
	// rejected so the corpus never pins only half of the real output.
	if _, err := CanonicalizeJSON([]byte(`{"a":1}{"b":2}`)); err == nil {
		t.Fatal("expected error for a second JSON value, got nil")
	}
}

func TestCanonicalizeAllowsTrailingWhitespace(t *testing.T) {
	// Trailing whitespace/newlines are normal on command stdout and must stay
	// acceptable; only non-whitespace after the first value is an error.
	if _, err := CanonicalizeJSON([]byte("{\"a\":1}\n  \n")); err != nil {
		t.Fatalf("trailing whitespace should be allowed: %v", err)
	}
}

// TestCanonicalizeDropsProvenance pins the build-provenance transform: `commit`
// is dropped entirely (its presence varies by build env), while `version`,
// `branch`, and `build` are placeholdered (stable presence, release-specific
// value). This is the subtlest, most-caveated part of the canonicalizer, and it
// matches bare keys at *every* nesting depth (see the NOTE ON SCOPE in
// corpus.go), so it must hold for a flat blob and for one nested under the v2
// `data` envelope. Doing it without a live store means a break in the provenance
// logic fails here in the pure unit suite, not only in the Dolt-gated golden
// test a Docker-less contributor never runs.
func TestCanonicalizeDropsProvenance(t *testing.T) {
	assertProvenance := func(t *testing.T, label string, obj map[string]any) {
		t.Helper()
		if v, ok := obj["commit"]; ok {
			t.Errorf("%s: commit must be dropped, still present as %v", label, v)
		}
		for key, want := range map[string]string{
			"version": "<VERSION>",
			"branch":  "<BRANCH>",
			"build":   "<BUILD>",
		} {
			if got, _ := obj[key].(string); got != want {
				t.Errorf("%s: %s = %q, want placeholder %q", label, key, got, want)
			}
		}
		// schema_version is the coordination canary and must survive untouched.
		if _, ok := obj["schema_version"]; !ok {
			t.Errorf("%s: schema_version must be preserved, not dropped", label)
		}
	}

	t.Run("flat", func(t *testing.T) {
		in := []byte(`{"version":"1.2.3","branch":"main","build":"cgo","commit":"abc1234","schema_version":6}`)
		out, err := CanonicalizeJSON(in)
		if err != nil {
			t.Fatalf("canonicalize: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal canonical output: %v\n%s", err, out)
		}
		assertProvenance(t, "flat", got)
	})

	t.Run("envelope-nested", func(t *testing.T) {
		// Envelope mode nests the version payload under "data"; the transform is
		// explicitly documented as not scopable to "top level only", so the nested
		// block must be canonicalized the same way as the flat one.
		in := []byte(`{"data":{"version":"1.2.3","branch":"main","build":"cgo","commit":"abc1234","schema_version":6},"schema_version":6}`)
		out, err := CanonicalizeJSON(in)
		if err != nil {
			t.Fatalf("canonicalize: %v", err)
		}
		var top map[string]any
		if err := json.Unmarshal(out, &top); err != nil {
			t.Fatalf("unmarshal canonical output: %v\n%s", err, out)
		}
		data, ok := top["data"].(map[string]any)
		if !ok {
			t.Fatalf("envelope: data block missing or not an object:\n%s", out)
		}
		assertProvenance(t, "envelope", data)
	})
}

// TestCorpusDoubleRunByteIdentical generates the corpus twice, in two fresh
// stores, and asserts every blob is byte-identical. This is the determinism
// backstop: if any nondeterministic field survives canonicalization, it fails
// here at PR time rather than as flaky corpus drift later.
func TestCorpusDoubleRunByteIdentical(t *testing.T) {
	requireDoltStore(t, "corpus double-run test")
	for _, envelope := range []bool{false, true} {
		first := generateCorpus(t, envelope)
		second := generateCorpus(t, envelope)
		if len(first) != len(second) {
			t.Fatalf("envelope=%v: blob count differs: %d vs %d", envelope, len(first), len(second))
		}
		for name, a := range first {
			b, ok := second[name]
			if !ok {
				t.Fatalf("envelope=%v: %q missing on second run", envelope, name)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("envelope=%v: %q is nondeterministic across runs:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", envelope, name, a, b)
			}
		}
	}
}
