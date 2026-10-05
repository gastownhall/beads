package wireshape_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/wireshape"
)

// TestWireShapeDigest is the drift gate ContextResponse.wire_revision
// documents: it fails the moment the shape of any EXISTING response member,
// request-body member or operation parameter changes without
// CurrentWireRevision increasing to match.
//
// It is deliberately not one struct-equal, because the ways this can go red
// call for different instructions (goldenDrift picks one). A changed or
// removed entry at the SAME wire_revision is drift nobody signed off on —
// regenerate only after bumping CurrentWireRevision (see
// internal/httpapi/wire_revision.go) and the `wire_revision` property's
// revision table in openapi.v0.yaml, never before. A wire_revision LOWER than
// the golden's is never fixed by regenerating: that table is append-only, so
// the constant itself is wrong. Any other difference — a bumped
// wire_revision, or entries only added — means this golden is simply stale;
// regenerate it with:
//
//	go run ./internal/httpapi/wireshape/cmd/gendigest
func TestWireShapeDigest(t *testing.T) {
	golden := loadGolden(t)
	got, err := wireshape.Compute(httpapi.CurrentWireRevision)
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}

	if len(got.Entries) < 20 {
		// The document describes dozens of response members; a near-empty
		// digest means the walk found nothing, which would make every
		// assertion below pass vacuously.
		t.Fatalf("digest has %d entries, want the full walk", len(got.Entries))
	}

	if drift := goldenDrift(golden, got); drift != "" {
		t.Error(drift)
	}
}

// goldenDrift is TestWireShapeDigest's verdict, factored out so
// TestGoldenDrift can drive every arm with synthetic digests. It returns ""
// only when golden records exactly got — the same entries at the same
// wire_revision. Every other state fails; the arms only choose which
// instruction the failure gives.
func goldenDrift(golden, got wireshape.Digest) string {
	if got.WireRevision < golden.WireRevision {
		return fmt.Sprintf("CurrentWireRevision is %d, LOWER than the %d the golden records: wire_revision only "+
			"ever increases (the revision table in openapi.v0.yaml's `wire_revision` property is append-only), "+
			"so restore CurrentWireRevision (internal/httpapi/wire_revision.go) — never regenerate the golden "+
			"backwards", got.WireRevision, golden.WireRevision)
	}

	cmp := wireshape.Compare(golden, got)
	changed, removed, added := cmp.Changed, cmp.Removed, cmp.Added

	switch {
	case len(changed) > 0 || len(removed) > 0:
		if got.WireRevision <= golden.WireRevision {
			return fmt.Sprintf("wire shape (response/request-body member or parameter) changed without a "+
				"wire_revision bump: changed=%v removed=%v\n"+
				"bump CurrentWireRevision (internal/httpapi/wire_revision.go) and the revision table "+
				"in openapi.v0.yaml's `wire_revision` property, THEN regenerate the golden with "+
				"`go run ./internal/httpapi/wireshape/cmd/gendigest`", changed, removed)
		}
		return fmt.Sprintf("wire shape (response/request-body member or parameter) changed (changed=%v "+
			"removed=%v) and wire_revision moved %d -> %d, but the golden was not regenerated: run "+
			"`go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result",
			changed, removed, golden.WireRevision, got.WireRevision)
	case len(added) > 0:
		if got.WireRevision == golden.WireRevision {
			return fmt.Sprintf("wire shape (response/request-body member or parameter) added (%v) but the "+
				"golden was not regenerated: this is additive and needs no wire_revision bump, but still run "+
				"`go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result", added)
		}
		// Passing here would leave the golden a revision behind
		// CurrentWireRevision, and a later non-additive change at that
		// already-moved constant would then be told only to regenerate (which
		// SafeToWrite allows) instead of to bump.
		return fmt.Sprintf("wire shape (response/request-body member or parameter) added (%v) and "+
			"wire_revision moved %d -> %d, but the golden was not regenerated: run "+
			"`go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result",
			added, golden.WireRevision, got.WireRevision)
	case got.WireRevision != golden.WireRevision:
		return fmt.Sprintf("CurrentWireRevision is %d but the golden still says %d, with no shape change to justify "+
			"either: regenerate with `go run ./internal/httpapi/wireshape/cmd/gendigest`",
			got.WireRevision, golden.WireRevision)
	}
	return ""
}

// TestGoldenDrift is TestWireShapeDigest's own falsification: every way the
// committed golden can disagree with a fresh Compute must fail, each with its
// own instruction. "Added after a bump" is the state review found passing
// silently — no arm fired when entries were only added and wire_revision had
// also moved — and a LOWERED wire_revision was told to regenerate, which
// would have written the golden's revision backwards.
func TestGoldenDrift(t *testing.T) {
	golden := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "Widget", Member: "name", Type: "string", Required: true},
		},
	}
	changed := []wireshape.Entry{{Schema: "Widget", Member: "name", Type: "integer", Required: true}}
	added := append(append([]wireshape.Entry{}, golden.Entries...),
		wireshape.Entry{Schema: "Widget", Member: "color", Type: "string"})

	for _, tc := range []struct {
		name string
		got  wireshape.Digest
		want []string // phrases the failure must contain; none means the golden is current
	}{
		{"identical digest is current",
			golden, nil},
		{"changed entry at the same revision asks for a bump",
			wireshape.Digest{WireRevision: 2, Entries: changed}, []string{"changed without a wire_revision bump"}},
		{"removed entry at the same revision asks for a bump",
			wireshape.Digest{WireRevision: 2}, []string{"changed without a wire_revision bump"}},
		{"changed entry after a bump asks for a regenerate",
			wireshape.Digest{WireRevision: 3, Entries: changed}, []string{"changed (changed=", "moved 2 -> 3", "not regenerated"}},
		{"added entry at the same revision asks for a regenerate",
			wireshape.Digest{WireRevision: 2, Entries: added}, []string{"added (", "needs no wire_revision bump"}},
		{"added entry after a bump asks for a regenerate",
			wireshape.Digest{WireRevision: 3, Entries: added}, []string{"added (", "moved 2 -> 3", "not regenerated"}},
		{"bump with no shape change asks for a regenerate",
			wireshape.Digest{WireRevision: 3, Entries: golden.Entries}, []string{"no shape change"}},
		{"lowered revision with no shape change asks to restore the constant",
			wireshape.Digest{WireRevision: 1, Entries: golden.Entries}, []string{"LOWER than the 2", "never regenerate the golden backwards"}},
		{"changed entry at a lowered revision asks to restore the constant",
			wireshape.Digest{WireRevision: 1, Entries: changed}, []string{"LOWER than the 2"}},
		{"added entry at a lowered revision asks to restore the constant",
			wireshape.Digest{WireRevision: 1, Entries: added}, []string{"LOWER than the 2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drift := goldenDrift(golden, tc.got)
			if len(tc.want) == 0 {
				if drift != "" {
					t.Fatalf("current golden reported as drift: %s", drift)
				}
				return
			}
			if drift == "" {
				t.Fatal("stale golden passed silently")
			}
			for _, phrase := range tc.want {
				if !strings.Contains(drift, phrase) {
					t.Errorf("drift %q does not contain %q", drift, phrase)
				}
			}
		})
	}
}

// TestSafeToWrite is the gendigest write guard's own falsification (review
// MEDIUM: "refuse to write changed or removed entries unless
// CurrentWireRevision is higher than the golden's recorded revision"), plus
// the refusal of any candidate whose revision is LOWER than the golden's.
func TestSafeToWrite(t *testing.T) {
	base := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "Widget", Member: "name", Type: "string", Required: true},
		},
	}

	t.Run("pure addition at the same revision is always safe", func(t *testing.T) {
		candidate := wireshape.Digest{
			WireRevision: 2,
			Entries: append(append([]wireshape.Entry{}, base.Entries...),
				wireshape.Entry{Schema: "Widget", Member: "color", Type: "string"}),
		}
		if ok, reason := wireshape.SafeToWrite(base, candidate); !ok {
			t.Fatalf("pure addition refused: %s", reason)
		}
	})

	t.Run("identical digest is always safe", func(t *testing.T) {
		if ok, reason := wireshape.SafeToWrite(base, base); !ok {
			t.Fatalf("identical digest refused: %s", reason)
		}
	})

	t.Run("changed entry at the same revision is refused", func(t *testing.T) {
		candidate := wireshape.Digest{
			WireRevision: 2,
			Entries:      []wireshape.Entry{{Schema: "Widget", Member: "name", Type: "integer", Required: true}},
		}
		if ok, _ := wireshape.SafeToWrite(base, candidate); ok {
			t.Fatal("changed entry at the same revision was allowed")
		}
	})

	t.Run("removed entry at the same revision is refused", func(t *testing.T) {
		candidate := wireshape.Digest{WireRevision: 2, Entries: nil}
		if ok, _ := wireshape.SafeToWrite(base, candidate); ok {
			t.Fatal("removed entry at the same revision was allowed")
		}
	})

	t.Run("changed entry with a lower revision is refused", func(t *testing.T) {
		candidate := wireshape.Digest{
			WireRevision: 1,
			Entries:      []wireshape.Entry{{Schema: "Widget", Member: "name", Type: "integer", Required: true}},
		}
		if ok, _ := wireshape.SafeToWrite(base, candidate); ok {
			t.Fatal("changed entry with a LOWER revision was allowed")
		}
	})

	t.Run("unchanged entries with a lower revision are refused", func(t *testing.T) {
		candidate := wireshape.Digest{WireRevision: 1, Entries: base.Entries}
		ok, reason := wireshape.SafeToWrite(base, candidate)
		if ok {
			t.Fatal("unchanged entries with a LOWER revision were allowed — the golden's revision would move backwards")
		}
		if !strings.Contains(reason, "append-only") {
			t.Errorf("refusal %q does not point at the append-only revision table", reason)
		}
	})

	t.Run("pure addition with a lower revision is refused", func(t *testing.T) {
		candidate := wireshape.Digest{
			WireRevision: 1,
			Entries: append(append([]wireshape.Entry{}, base.Entries...),
				wireshape.Entry{Schema: "Widget", Member: "color", Type: "string"}),
		}
		if ok, _ := wireshape.SafeToWrite(base, candidate); ok {
			t.Fatal("pure addition with a LOWER revision was allowed — the golden's revision would move backwards")
		}
	})

	t.Run("changed entry with a higher revision is safe", func(t *testing.T) {
		candidate := wireshape.Digest{
			WireRevision: 3,
			Entries:      []wireshape.Entry{{Schema: "Widget", Member: "name", Type: "integer", Required: true}},
		}
		if ok, reason := wireshape.SafeToWrite(base, candidate); !ok {
			t.Fatalf("changed entry with a higher revision refused: %s", reason)
		}
	})

	t.Run("removed entry with a higher revision is safe", func(t *testing.T) {
		candidate := wireshape.Digest{WireRevision: 3, Entries: nil}
		if ok, reason := wireshape.SafeToWrite(base, candidate); !ok {
			t.Fatalf("removed entry with a higher revision refused: %s", reason)
		}
	})
}

// TestParameterMutationsAreCaught is the falsification for how Compare and
// SafeToWrite treat the parameter side of the digest (review: "the digest
// doesn't cover operation PARAMETERS ... so a type, enum, required, or
// removal change on an existing parameter passes the gate silently"). It
// mutates real entries out of the committed golden — not a synthetic fixture
// — so a future rename of the `listIssues` limit/sort parameters breaks this
// test loudly instead of letting the coverage go stale unnoticed. Because it
// mutates entries after the fact, it cannot see a field walkParam extracts
// wrongly; that is TestWalkParam's job (wireshape_internal_test.go).
//
// Each case checks the same two things TestSafeToWrite checks for schema
// members: the mutation is refused at the golden's own wire_revision, and
// allowed once the candidate's wire_revision is strictly higher — proving
// SafeToWrite treats a parameter entry exactly as non-additively as a
// response or request-body entry, using nothing parameter-specific.
func TestParameterMutationsAreCaught(t *testing.T) {
	golden := loadGolden(t)

	withEntry := func(mutate func(e wireshape.Entry) wireshape.Entry, schema, member string) []wireshape.Entry {
		out := make([]wireshape.Entry, 0, len(golden.Entries))
		for _, e := range golden.Entries {
			if e.Schema == schema && e.Member == member {
				e = mutate(e)
			}
			out = append(out, e)
		}
		return out
	}
	withoutEntry := func(schema, member string) []wireshape.Entry {
		out := make([]wireshape.Entry, 0, len(golden.Entries))
		for _, e := range golden.Entries {
			if e.Schema == schema && e.Member == member {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	mustFind := func(schema, member string) wireshape.Entry {
		t.Helper()
		for _, e := range golden.Entries {
			if e.Schema == schema && e.Member == member {
				return e
			}
		}
		t.Fatalf("golden has no %s/%s entry (has the spec changed? update this test's fixture keys)", schema, member)
		return wireshape.Entry{}
	}

	// listIssues' `limit` query parameter: {type: integer, default: "50"}.
	limit := mustFind("param:listIssues", "query:limit")
	if limit.Type != "integer" {
		t.Fatalf("param:listIssues query:limit is %q in the current golden, not \"integer\" — update this test's fixture", limit.Type)
	}
	// listIssues' `sort` query parameter: {type: string, enum: [created, priority]}.
	sortEntry := mustFind("param:listIssues", "query:sort")
	if len(sortEntry.Enum) < 2 {
		t.Fatalf("param:listIssues query:sort has %v enum values in the current golden, want >= 2 — update this test's fixture", sortEntry.Enum)
	}

	cases := []struct {
		name      string
		mutations []wireshape.Entry
	}{
		{
			name:      "removing a parameter",
			mutations: withoutEntry("param:listIssues", "query:limit"),
		},
		{
			name: "retyping a parameter",
			mutations: withEntry(func(e wireshape.Entry) wireshape.Entry {
				e.Type = "string"
				return e
			}, "param:listIssues", "query:limit"),
		},
		{
			name: "narrowing a parameter's enum",
			mutations: withEntry(func(e wireshape.Entry) wireshape.Entry {
				e.Enum = e.Enum[:1]
				return e
			}, "param:listIssues", "query:sort"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sameRevision := wireshape.Digest{WireRevision: golden.WireRevision, Entries: tc.mutations}
			if ok, _ := wireshape.SafeToWrite(golden, sameRevision); ok {
				t.Fatalf("%s at the same wire_revision was allowed — the digest did not see the change", tc.name)
			}

			higherRevision := wireshape.Digest{WireRevision: golden.WireRevision + 1, Entries: tc.mutations}
			if ok, reason := wireshape.SafeToWrite(golden, higherRevision); !ok {
				t.Fatalf("%s with a bumped wire_revision was refused: %s", tc.name, reason)
			}
		})
	}
}

func loadGolden(t *testing.T) wireshape.Digest {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var d wireshape.Digest
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	return d
}
