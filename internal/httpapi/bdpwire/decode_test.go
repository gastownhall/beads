package bdpwire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Beads-authored documents — not upstream fixtures — that pin the decoding
// posture doc.go promises and the marshal shapes an authority relies on.

const (
	beadURL = "https://s.example/acme/beads/1"
	linkURL = "https://s.example/acme/links/1"
	typeURL = "https://t.example/types/x"
)

func TestStrictDecodeRefusesUnknownMembersInClosedEnvelopes(t *testing.T) {
	cases := []struct {
		name   string
		doc    string
		target any
	}{
		{"bead record", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{},"extra":1}`, &BeadRecord{}},
		{"attribution nested in a record", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","attribution":{"principal":"agent:p","status":"claimed","note":"x"},"properties":{}}`, &BeadRecord{}},
		{"link nested in ownedLinks", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{},"ownedLinks":{"` + typeURL + `":[{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","target":"urn:x","properties":{},"label":"no"}]}}`, &BeadRecord{}},
		{"link in an embedded page", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{},"links":{"items":[{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","target":"urn:x","properties":{},"direction":"outbound"}],"next":null}}`, &BeadRecord{}},
		{"discovery with a Read+Update member", `{"bdpVersion":"0","profile":"read","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/","types":"https://s.example/acme/types/","operations":"https://s.example/acme/operations/"}`, &ReadDiscovery{}},
		{"limits group", `{"page":{"defaultItems":50,"pageSize":10}}`, &AdvertisedLimits{}},
		{"descriptor", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[],"schema":"https://t.example/x.json"}`, &TypeDescriptor{}},
		{"owned link declaration", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"` + typeURL + `":{"max":1,"min":0}}}`, &TypeDescriptor{}},
		{"endpoint constraint", `{"id":"` + typeURL + `","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[],"external":"none","required":true},"target":{"conformsTo":[]}}`, &TypeDescriptor{}},
		{"type summary", `{"id":"` + typeURL + `","name":"X","describes":"bead","description":"d"}`, &TypeSummary{}},
		{"collection", `{"items":[],"next":null,"total":0}`, &BeadCollection{}},
		{"multiplicity policy", `{"linkConformsTo":"` + typeURL + `","endpoint":"source","max":1,"min":1}`, &MaximumEndpointMultiplicityPolicy{}},
	}
	for _, tc := range cases {
		err := Unmarshal([]byte(tc.doc), tc.target)
		if err == nil {
			t.Errorf("%s: an unknown member was accepted into %T", tc.name, tc.target)
			continue
		}
		if !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: refused for another reason: %v", tc.name, err)
		}
	}
}

func TestStrictDecodeRefusesTrailingData(t *testing.T) {
	var a Attribution
	if err := Unmarshal([]byte(`{"principal":"agent:p","status":"claimed"} {}`), &a); err == nil {
		t.Error("a second document was accepted")
	}
	if err := Unmarshal([]byte(`{"principal":"agent:p","status":"claimed"} x`), &a); err == nil {
		t.Error("trailing garbage was accepted")
	}
	if err := Unmarshal([]byte("  {\"principal\":\"agent:p\",\"status\":\"claimed\"}\n"), &a); err != nil {
		t.Errorf("surrounding whitespace was refused: %v", err)
	}
}

func TestDecodeReadsOneDocumentFromAReader(t *testing.T) {
	doc := `{"bdpVersion":"0","profile":"read","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/","types":"https://s.example/acme/types/"}`
	var d ReadDiscovery
	if err := Decode(strings.NewReader(doc), &d); err != nil {
		t.Fatal(err)
	}
	if d.Profile != ProfileRead || d.BDPVersion != BDPVersion || d.Scope != "https://s.example/acme/" {
		t.Errorf("decoded %+v", d)
	}
	if d.Limits != nil || d.Aliases != "" || d.Order != "" || d.MaximumEndpointMultiplicity != nil {
		t.Errorf("optional members materialized from nothing: %+v", d)
	}
}

func TestPropertiesAreCarriedVerbatimAndNeverNull(t *testing.T) {
	doc := `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{"title":"T","nested":{"deep":[1,2,{"k":null}]},"n":1.50,"extension":"retained"}}`
	var b BeadRecord
	if err := Unmarshal([]byte(doc), &b); err != nil {
		t.Fatal(err)
	}
	if got := string(b.Properties["nested"]); got != `{"deep":[1,2,{"k":null}]}` {
		t.Errorf("nested member carried as %s", got)
	}
	if got := string(b.Properties["n"]); got != "1.50" {
		t.Errorf("number literal rewritten to %s", got)
	}
	if got := string(b.Properties["extension"]); got != `"retained"` {
		t.Errorf("undeclared member carried as %s", got)
	}

	out, err := json.Marshal(BeadRecord{ID: beadURL, Type: typeURL, Revision: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"properties":{}`) {
		t.Errorf("nil properties marshaled as %s; the member is required and never null", out)
	}
	if p, err := json.Marshal(Properties(nil)); err != nil || string(p) != "{}" {
		t.Errorf("Properties(nil) = %s, %v", p, err)
	}
}

func TestRequiredArraysNeverMarshalAsNull(t *testing.T) {
	check := func(name string, v any, member string) {
		t.Helper()
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(out, &members); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := string(members[member]); got != "[]" {
			t.Errorf("%s: zero value serves %q as %s, want []", name, member, got)
		}
	}
	check("bead collection", BeadCollection{}, "items")
	check("link collection", LinkCollection{}, "items")
	check("types inventory", TypesInventory{}, "items")
	check("descriptor", TypeDescriptor{}, "conformsTo")
	check("endpoint constraint", EndpointConstraint{}, "conformsTo")

	// An owning Type with no owned Links of one declared Type still serves
	// that entry, as an empty array.
	out, err := json.Marshal(BeadRecord{OwnedLinks: OwnedLinks{typeURL: nil}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"ownedLinks":{"`+typeURL+`":[]}`) {
		t.Errorf("empty owned entry marshaled as %s", out)
	}
	// A Bead whose Type owns nothing has no member at all.
	out, err = json.Marshal(BeadRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ownedLinks") {
		t.Errorf("non-owning record serves ownedLinks: %s", out)
	}
}

func TestNextIsNullAfterTheFinalPage(t *testing.T) {
	var c LinkCollection
	if err := Unmarshal([]byte(`{"items":[],"next":null}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Next != nil {
		t.Errorf("null next decoded as %q", *c.Next)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"next":null`) {
		t.Errorf("final page marshaled as %s; next must be present and null", out)
	}

	next := "https://s.example/acme/links/?cursor=opaque"
	c.Next = &next
	out, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"next":"`+next+`"`) {
		t.Errorf("continuation marshaled as %s", out)
	}
}

func TestReferenceSum(t *testing.T) {
	t.Run("string arm", func(t *testing.T) {
		var r Reference
		if err := Unmarshal([]byte(`"`+beadURL+`"`), &r); err != nil {
			t.Fatal(err)
		}
		if r.Pinned() || r.URI != beadURL {
			t.Errorf("decoded %+v", r)
		}
		out, err := json.Marshal(r)
		if err != nil || string(out) != `"`+beadURL+`"` {
			t.Errorf("marshaled %s, %v", out, err)
		}
	})
	t.Run("object arm keeps the pin byte for byte", func(t *testing.T) {
		doc := "{\"uri\":\"urn:external:pin-witness\",\"revision\":\"  Cited-9F2c — α/β (draft) Å\\t\"}"
		var r Reference
		if err := Unmarshal([]byte(doc), &r); err != nil {
			t.Fatal(err)
		}
		if !r.Pinned() || r.Revision != "  Cited-9F2c — α/β (draft) Å\t" {
			t.Errorf("decoded %+v", r)
		}
		out, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical(t, out), canonical(t, []byte(doc))) {
			t.Errorf("round trip changed the pin: %s", out)
		}
	})
	t.Run("rejections", func(t *testing.T) {
		for _, doc := range []string{
			`{"uri":"urn:x"}`,                          // revision required
			`{"uri":"urn:x","revision":""}`,            // minLength 1, and the Go value would read as unpinned
			`{"revision":"r"}`,                         // uri required
			`{"uri":"urn:x","revision":"r","extra":1}`, // pinnedReference is closed
			`{"uri":"urn:x","revision":1}`,             // revision is a string
			`{"uri":1,"revision":"r"}`,                 // uri is a string
			`null`, `1`, `true`, `["urn:x"]`, `{}`,
		} {
			var r Reference
			if err := Unmarshal([]byte(doc), &r); err == nil {
				t.Errorf("%s decoded as %+v", doc, r)
			}
		}
	})
	t.Run("zero marshals as the empty string arm", func(t *testing.T) {
		out, err := json.Marshal(Reference{})
		if err != nil || string(out) != `""` {
			t.Errorf("Reference{} marshaled as %s, %v", out, err)
		}
	})
	t.Run("as link endpoints", func(t *testing.T) {
		doc := `{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","target":{"uri":"https://github.example/issues/123","revision":"8f0e2b"},"properties":{}}`
		var l LinkRecord
		if err := Unmarshal([]byte(doc), &l); err != nil {
			t.Fatal(err)
		}
		if l.Source.Pinned() || !l.Target.Pinned() || l.Target.Revision != "8f0e2b" {
			t.Errorf("decoded %+v", l)
		}
		out, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical(t, out), canonical(t, []byte(doc))) {
			t.Errorf("round trip changed the record: %s", out)
		}
		var bad LinkRecord
		if err := Unmarshal([]byte(`{"id":"`+linkURL+`","type":"`+typeURL+`","revision":"r1","source":null,"target":"urn:x","properties":{}}`), &bad); err == nil {
			t.Error("a null endpoint was accepted")
		}
	})
}

func TestReadProblemCarriesExtensionsAndValidates(t *testing.T) {
	doc := `{"type":"https://github.com/gastownhall/bdp/problems/gone","title":"Gone","status":410,"detail":"pruned by policy","instance":"https://s.example/acme/beads/relic","code":"resource-pruned","retry":"never","archivedAt":{"uri":"https://archive.example/acme/beads/relic","revision":"arch-r4 (as-written)"},"traceId":"abc","context":{"tenant":"acme","tags":["a","b"]}}`
	var p ReadProblem
	if err := Unmarshal([]byte(doc), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != CodeResourcePruned || p.Status != 410 || p.Retry != RetryNever || p.Title != "Gone" {
		t.Errorf("decoded %+v", p)
	}
	if p.ArchivedAt == nil || !p.ArchivedAt.Pinned() {
		t.Errorf("archivedAt decoded as %+v", p.ArchivedAt)
	}
	if got := string(p.Extensions["context"]); got != `{"tenant":"acme","tags":["a","b"]}` {
		t.Errorf("extension carried as %s", got)
	}
	if got := string(p.Extensions["traceId"]); got != `"abc"` {
		t.Errorf("extension carried as %s", got)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, []byte(doc))) {
		t.Errorf("round trip changed the problem\n got: %s\nwant: %s", canonical(t, out), canonical(t, []byte(doc)))
	}

	// An extension may not reuse a protocol-owned member name.
	p.Extensions["code"] = json.RawMessage(`"x"`)
	if _, err := json.Marshal(p); err == nil {
		t.Error("an extension named code was marshaled over the real one")
	}

	// A minimal problem has no extensions and serves none.
	var minimal ReadProblem
	if err := Unmarshal([]byte(`{"type":"https://github.com/gastownhall/bdp/problems/request","code":"invalid-parameter","retry":"never"}`), &minimal); err != nil {
		t.Fatal(err)
	}
	if minimal.Extensions != nil {
		t.Errorf("Extensions = %v, want nil", minimal.Extensions)
	}
	out, err = json.Marshal(minimal)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"type":"https://github.com/gastownhall/bdp/problems/request","code":"invalid-parameter","retry":"never"}` {
		t.Errorf("minimal problem marshaled as %s", out)
	}

	// Named members keep their types even though the envelope is open.
	var typed ReadProblem
	if err := Unmarshal([]byte(`{"type":"t","code":"forbidden","retry":"after-state-change","status":"403"}`), &typed); err == nil {
		t.Error("a string status was accepted")
	}
	if err := Unmarshal([]byte(`{"type":"t","code":"resource-pruned","retry":"never","archivedAt":{"uri":"urn:x"}}`), &typed); err == nil {
		t.Error("an unpinned archivedAt object was accepted")
	}
	if err := Unmarshal([]byte(`null`), &typed); err == nil {
		t.Error("null was accepted as a problem")
	}
}

func TestNewReadProblemFillsTheTableAndValidateEnforcesIt(t *testing.T) {
	p := NewReadProblem(CodeRateLimited)
	if p.Type != ProblemTypePrefix+"rate-limit" || p.Status != 429 || p.Retry != RetryAfterDelay || p.Code != CodeRateLimited {
		t.Errorf("NewReadProblem(rate-limited) = %+v", p)
	}
	for code := range readProblemTable {
		if err := NewReadProblem(code).Validate(); err != nil {
			t.Errorf("%s: %v", code, err)
		}
		withoutStatus := NewReadProblem(code)
		withoutStatus.Status = 0
		if err := withoutStatus.Validate(); err != nil {
			t.Errorf("%s without status: %v", code, err)
		}
	}

	pruned := NewReadProblem(CodeResourcePruned)
	pruned.ArchivedAt = &Reference{URI: "https://archive.example/relic"}
	if err := pruned.Validate(); err != nil {
		t.Errorf("archivedAt on resource-pruned: %v", err)
	}

	bad := map[string]ReadProblem{
		"wrong type":            {Type: ProblemTypePrefix + "gone", Code: CodeForbidden, Retry: RetryAfterStateChange},
		"wrong retry":           {Type: ProblemTypePrefix + "authorization", Code: CodeForbidden, Retry: RetryNever},
		"wrong status":          {Type: ProblemTypePrefix + "authorization", Code: CodeForbidden, Retry: RetryAfterStateChange, Status: 404},
		"archivedAt on erased":  {Type: ProblemTypePrefix + "gone", Code: CodeResourceErased, Retry: RetryNever, ArchivedAt: &Reference{URI: "urn:x"}},
		"unknown code":          {Type: ProblemTypePrefix + "request", Code: "bogus", Retry: RetryNever},
		"transactional-only ok": {Type: ProblemTypePrefix + "gone", Code: "event-history-expired", Retry: RetryNever},
	}
	for name, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}
