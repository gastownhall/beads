package wireshape_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// wire_revision, entries only added, or a `sides` map that no longer matches
// — means this golden is simply stale; regenerate it with:
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
// only when golden records exactly got — the same entries and sides at the
// same wire_revision. Every other state fails; the arms only choose which
// instruction the failure gives.
func goldenDrift(golden, got wireshape.Digest) string {
	if got.WireRevision < golden.WireRevision {
		return fmt.Sprintf("CurrentWireRevision is %d, LOWER than the %d the golden records: wire_revision only "+
			"ever increases (the revision table in openapi.v0.yaml's `wire_revision` property is append-only), "+
			"so restore CurrentWireRevision (internal/httpapi/wire_revision.go) — never regenerate the golden "+
			"backwards", got.WireRevision, golden.WireRevision)
	}

	cmp := wireshape.Compare(golden, got)
	changed, removed, added, widened := cmp.Changed, cmp.Removed, cmp.Added, cmp.Widened
	sides := sidesDrift(golden.Sides, got.Sides)

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
	case len(added) > 0 || len(widened) > 0:
		// Widened (a request-only enum strictly growing) is additive, same as
		// Added (a brand-new key): neither needs CurrentWireRevision to move,
		// both just need the golden regenerated.
		if got.WireRevision == golden.WireRevision {
			return fmt.Sprintf("wire shape (response/request-body member or parameter) added (%v) or had a "+
				"request-only enum widen (%v) but the golden was not regenerated: this is additive and needs "+
				"no wire_revision bump, but still run `go run ./internal/httpapi/wireshape/cmd/gendigest` "+
				"and commit the result", added, widened)
		}
		// Passing here would leave the golden a revision behind
		// CurrentWireRevision, and a later non-additive change at that
		// already-moved constant would then be told only to regenerate (which
		// SafeToWrite allows) instead of to bump.
		return fmt.Sprintf("wire shape (response/request-body member or parameter) added (%v) or had a "+
			"request-only enum widen (%v) and wire_revision moved %d -> %d, but the golden was not "+
			"regenerated: run `go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result",
			added, widened, golden.WireRevision, got.WireRevision)
	case got.WireRevision != golden.WireRevision:
		return fmt.Sprintf("CurrentWireRevision is %d but the golden still says %d, with no shape change to justify "+
			"either: regenerate with `go run ./internal/httpapi/wireshape/cmd/gendigest`",
			got.WireRevision, golden.WireRevision)
	case len(sides) > 0:
		// Compare reads Sides only for the request-only widening carve-out, so
		// a stale map changes no member's shape: it misfiles the next enum
		// widening on those schemas instead. Nothing to bump, but the golden
		// is still not what gendigest writes.
		return fmt.Sprintf("the golden's `sides` map disagrees with a fresh digest for %v: this needs no "+
			"wire_revision bump, but run `go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result",
			sides)
	}
	return ""
}

// sidesDrift lists, sorted, every schema whose side differs between want and
// got, including a schema only one of them records.
func sidesDrift(want, got map[string]string) []string {
	var drift []string
	for schema, side := range want {
		if gotSide, ok := got[schema]; !ok || gotSide != side {
			drift = append(drift, schema)
		}
	}
	for schema := range got {
		if _, ok := want[schema]; !ok {
			drift = append(drift, schema)
		}
	}
	slices.Sort(drift)
	return drift
}

// TestGoldenDrift is TestWireShapeDigest's own falsification: every way the
// committed golden can disagree with a fresh Compute must fail, each with its
// own instruction. "Added after a bump" is the state review found passing
// silently — no arm fired when entries were only added and wire_revision had
// also moved — and a LOWERED wire_revision was told to regenerate, which
// would have written the golden's revision backwards. A stale `sides` map
// passed the same way until goldenDrift compared it.
func TestGoldenDrift(t *testing.T) {
	golden := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "Widget", Member: "name", Type: "string", Required: true},
		},
		Sides: map[string]string{"Widget": "response"},
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
		{"side recorded only by the fresh digest asks for a regenerate",
			wireshape.Digest{WireRevision: 2, Entries: golden.Entries,
				Sides: map[string]string{"Widget": "response", "Gadget": "request"}},
			[]string{"`sides` map disagrees", "[Gadget]", "needs no wire_revision bump"}},
		{"side that moved asks for a regenerate",
			wireshape.Digest{WireRevision: 2, Entries: golden.Entries, Sides: map[string]string{"Widget": "both"}},
			[]string{"`sides` map disagrees", "[Widget]"}},
		{"side recorded only by the golden asks for a regenerate",
			wireshape.Digest{WireRevision: 2, Entries: golden.Entries}, []string{"`sides` map disagrees", "[Widget]"}},
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

// TestEnumWideningAdditivity is the falsification for widensAdditively
// (review MED 1: "the only trigger [for S4's wire_revision bump] is the
// request-only enum widening of SweepRequest.tier ... fix the wireshape gate
// so widening a request-only enum counts as additive, while response-side
// enum widening stays breaking"). Two synthetic digests, identical except for
// their Sides map, prove the carve-out is keyed on Sides alone — Entry itself
// never changes shape.
func TestEnumWideningAdditivity(t *testing.T) {
	requestBase := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "SweepRequest", Member: "tier", Type: "string", Required: true,
				Enum: []string{"durable", "ephemeral"}},
		},
		Sides: map[string]string{"SweepRequest": "request"},
	}
	requestWidened := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "SweepRequest", Member: "tier", Type: "string", Required: true,
				Enum: []string{"durable", "ephemeral", "wisps-plane"}},
		},
		Sides: map[string]string{"SweepRequest": "request"},
	}

	t.Run("a request-only enum widening is additive at the same revision", func(t *testing.T) {
		cmp := wireshape.Compare(requestBase, requestWidened)
		if len(cmp.Changed) != 0 {
			t.Fatalf("Changed = %v, want none (the widening belongs in Widened)", cmp.Changed)
		}
		if len(cmp.Widened) != 1 {
			t.Fatalf("Widened = %v, want exactly one entry", cmp.Widened)
		}
		if ok, reason := wireshape.SafeToWrite(requestBase, requestWidened); !ok {
			t.Fatalf("request-only enum widening at the same revision refused: %s", reason)
		}
		drift := goldenDrift(requestBase, requestWidened)
		if drift == "" || !strings.Contains(drift, "needs no wire_revision bump") {
			t.Fatalf("goldenDrift = %q, want an additive (no-bump) instruction", drift)
		}
	})

	responseBase := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "SweepResult", Member: "tier", Type: "string", Required: true,
				Enum: []string{"durable", "ephemeral"}},
		},
		Sides: map[string]string{"SweepResult": "response"},
	}
	responseWidened := wireshape.Digest{
		WireRevision: 2,
		Entries: []wireshape.Entry{
			{Schema: "SweepResult", Member: "tier", Type: "string", Required: true,
				Enum: []string{"durable", "ephemeral", "wisps-plane"}},
		},
		Sides: map[string]string{"SweepResult": "response"},
	}

	t.Run("the identical widening on a response-side entry stays breaking", func(t *testing.T) {
		cmp := wireshape.Compare(responseBase, responseWidened)
		if len(cmp.Widened) != 0 {
			t.Fatalf("Widened = %v, want none (a response-side widening is not additive)", cmp.Widened)
		}
		if len(cmp.Changed) != 1 {
			t.Fatalf("Changed = %v, want exactly one entry", cmp.Changed)
		}
		if ok, _ := wireshape.SafeToWrite(responseBase, responseWidened); ok {
			t.Fatal("response-side enum widening at the same revision was allowed")
		}
		bumped := wireshape.Digest{WireRevision: 3, Entries: responseWidened.Entries, Sides: responseWidened.Sides}
		if ok, reason := wireshape.SafeToWrite(responseBase, bumped); !ok {
			t.Fatalf("response-side enum widening with a bumped revision refused: %s", reason)
		}
		drift := goldenDrift(responseBase, responseWidened)
		if drift == "" || !strings.Contains(drift, "changed without a wire_revision bump") {
			t.Fatalf("goldenDrift = %q, want the ordinary non-additive instruction", drift)
		}
	})

	t.Run("an enum narrowing on a request-only entry is still breaking", func(t *testing.T) {
		narrowed := wireshape.Digest{
			WireRevision: 2,
			Entries: []wireshape.Entry{
				{Schema: "SweepRequest", Member: "tier", Type: "string", Required: true,
					Enum: []string{"durable"}},
			},
			Sides: map[string]string{"SweepRequest": "request"},
		}
		cmp := wireshape.Compare(requestBase, narrowed)
		if len(cmp.Widened) != 0 {
			t.Fatalf("Widened = %v, want none (narrowing is not a widening)", cmp.Widened)
		}
		if len(cmp.Changed) != 1 {
			t.Fatalf("Changed = %v, want exactly one entry", cmp.Changed)
		}
	})

	t.Run("going from no enum at all to a fixed enum on a request-only entry is not additive", func(t *testing.T) {
		// Pinning case for review finding: widensAdditively required
		// len(new.Enum) > len(old.Enum), which holds vacuously when the old
		// side has no enum (a free string). That let a request-only member
		// go from unconstrained to a fixed set at the same wire_revision —
		// the opposite of widening, since the server now 400s a value an
		// old client could send yesterday.
		noEnumBase := wireshape.Digest{
			WireRevision: 2,
			Entries: []wireshape.Entry{
				{Schema: "SweepRequest", Member: "tier", Type: "string", Required: true},
			},
			Sides: map[string]string{"SweepRequest": "request"},
		}
		cmp := wireshape.Compare(noEnumBase, requestWidened)
		if len(cmp.Widened) != 0 {
			t.Fatalf("Widened = %v, want none (no enum before is a new restriction, not a widening)", cmp.Widened)
		}
		if len(cmp.Changed) != 1 {
			t.Fatalf("Changed = %v, want exactly one entry", cmp.Changed)
		}
		if ok, _ := wireshape.SafeToWrite(noEnumBase, requestWidened); ok {
			t.Fatal("no-enum-to-enum tightening at the same revision was allowed")
		}
		bumped := wireshape.Digest{WireRevision: 3, Entries: requestWidened.Entries, Sides: requestWidened.Sides}
		if ok, reason := wireshape.SafeToWrite(noEnumBase, bumped); !ok {
			t.Fatalf("no-enum-to-enum tightening with a bumped revision refused: %s", reason)
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
