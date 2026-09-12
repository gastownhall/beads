package bdpwire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The strict decoder's contract (decode.go): exact member names, null and
// absent kept apart, required members present, integers decoded exactly.
// Each table names the document it refuses or admits and why.

func wantDecodeError(t *testing.T, err error, doc, mention string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want an error mentioning %q", doc, mention)
		return
	}
	if !strings.Contains(err.Error(), mention) {
		t.Errorf("%s: refused for another reason: %v (want %q)", doc, err, mention)
	}
}

func TestStrictDecodeMemberNamesAreExactAndUnique(t *testing.T) {
	for _, tc := range []struct {
		doc     string
		target  any
		mention string
	}{
		// Codex's counterexample: a case variant passed DisallowUnknownFields
		// and changed the URI.
		{`{"uri":"urn:x","revision":"r","URI":"urn:y"}`, &Reference{}, `unknown field "URI"`},
		{`{"uri":"urn:x","revision":"r","Revision":"s"}`, &Reference{}, `unknown field "Revision"`},
		{`{"uri":"urn:x","uri":"urn:y","revision":"r"}`, &Reference{}, `duplicate member "uri"`},
		{`{"Principal":"agent:p","status":"claimed"}`, &Attribution{}, `unknown field "Principal"`},
		{`{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{},"Revision":"r2"}`, &BeadRecord{}, `unknown field "Revision"`},
		{`{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","target":"urn:x","properties":{},"Source":"urn:z"}`, &LinkRecord{}, `unknown field "Source"`},
		{`{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":{"uri":"` + beadURL + `","revision":"r","URI":"x"},"target":"urn:x","properties":{}}`, &LinkRecord{}, `source: unknown field "URI"`},
		{`{"id":"` + typeURL + `","name":"X","describes":"bead","ConformsTo":[]}`, &TypeDescriptor{}, `unknown field "ConformsTo"`},
		{`{"id":"` + typeURL + `","name":"X","describes":"link","conformsTo":[],"source":{"conformsto":[]},"target":{"conformsTo":[]}}`, &TypeDescriptor{}, `source: unknown field "conformsto"`},
		{`{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"` + typeURL + `":{"Max":1}}}`, &TypeDescriptor{}, `unknown field "Max"`},
		{`{"BdpVersion":"0","profile":"read","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/","types":"https://s.example/acme/types/"}`, &ReadDiscovery{}, `unknown field "BdpVersion"`},
		{`{"page":{"DefaultItems":50}}`, &AdvertisedLimits{}, `page: unknown field "DefaultItems"`},
		{`{"items":[],"Next":null}`, &BeadCollection{}, `unknown field "Next"`},
		{`{"items":[],"next":null,"next":null}`, &BeadCollection{}, `duplicate member "next"`},
		{`{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{"a":1,"a":2}}`, &BeadRecord{}, `properties: duplicate member "a"`},
		{`{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{},"ownedLinks":{"` + typeURL + `":[],"` + typeURL + `":[]}}`, &BeadRecord{}, `ownedLinks: duplicate member`},
	} {
		wantDecodeError(t, Unmarshal([]byte(tc.doc), tc.target), tc.doc, tc.mention)
	}

	// A problem is the one open envelope: a case variant of a protocol-owned
	// name is an EXTENSION, exactly as a client would read it, and never the
	// named member.
	var p ReadProblem
	if err := Unmarshal([]byte(`{"type":"t","code":"forbidden","retry":"after-state-change","Code":"x"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != CodeForbidden || string(p.Extensions["Code"]) != `"x"` {
		t.Errorf("case-variant member on a problem: code %q extensions %v", p.Code, p.Extensions)
	}
	wantDecodeError(t, Unmarshal([]byte(`{"type":"t","code":"forbidden","code":"forbidden","retry":"never"}`), &p), "duplicate code", `duplicate member "code"`)
}

func TestStrictDecodeKeepsNullAndAbsentApart(t *testing.T) {
	bead := func(extra string) string {
		return `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":{}` + extra + `}`
	}
	descriptor := func(extra string) string {
		return `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[]` + extra + `}`
	}
	discovery := func(extra string) string {
		return `{"bdpVersion":"0","profile":"read","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/","types":"https://s.example/acme/types/"` + extra + `}`
	}
	for _, tc := range []struct {
		name   string
		doc    string
		target any
	}{
		{"uri null", `{"uri":null,"revision":"r"}`, &Reference{}},
		{"revision null", `{"uri":"urn:x","revision":null}`, &Reference{}},
		{"reference null", `null`, &Reference{}},
		{"archivedAt null", `{"type":"t","code":"resource-pruned","retry":"never","archivedAt":null}`, &ReadProblem{}},
		{"title null", `{"type":"t","code":"forbidden","retry":"after-state-change","title":null}`, &ReadProblem{}},
		{"status null", `{"type":"t","code":"forbidden","retry":"after-state-change","status":null}`, &ReadProblem{}},
		{"code null", `{"type":"t","code":null,"retry":"never"}`, &ReadProblem{}},
		{"problem null", `null`, &ReadProblem{}},
		{"items null", `{"items":null,"next":null}`, &BeadCollection{}},
		{"item null", `{"items":[null],"next":null}`, &LinkCollection{}},
		{"properties null", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1","properties":null}`, &BeadRecord{}},
		{"attribution null", bead(`,"attribution":null`), &BeadRecord{}},
		{"links null", bead(`,"links":null`), &BeadRecord{}},
		{"ownedLinks null", bead(`,"ownedLinks":null`), &BeadRecord{}},
		{"ownedLinks entry null", bead(`,"ownedLinks":{"` + typeURL + `":null}`), &BeadRecord{}},
		{"ownedLinks link null", bead(`,"ownedLinks":{"` + typeURL + `":[null]}`), &BeadRecord{}},
		{"revision null", `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":null,"properties":{}}`, &BeadRecord{}},
		{"source null", `{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":null,"target":"urn:x","properties":{}}`, &LinkRecord{}},
		{"target null", `{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","target":null,"properties":{}}`, &LinkRecord{}},
		{"conformsTo null", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":null}`, &TypeDescriptor{}},
		{"conformsTo element null", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[null]}`, &TypeDescriptor{}},
		{"description null", descriptor(`,"description":null`), &TypeDescriptor{}},
		{"propertiesSchema null", descriptor(`,"propertiesSchema":null`), &TypeDescriptor{}},
		{"source constraint null", `{"id":"` + typeURL + `","name":"X","describes":"link","conformsTo":[],"source":null,"target":{"conformsTo":[]}}`, &TypeDescriptor{}},
		{"external null", `{"id":"` + typeURL + `","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[],"external":null},"target":{"conformsTo":[]}}`, &TypeDescriptor{}},
		{"ownsOutgoing null", descriptor(`,"ownsOutgoing":null`), &TypeDescriptor{}},
		{"ownsOutgoing entry null", descriptor(`,"ownsOutgoing":{"` + typeURL + `":null}`), &TypeDescriptor{}},
		{"label null", descriptor(`,"ownsOutgoing":{"` + typeURL + `":{"label":null,"max":1}}`), &TypeDescriptor{}},
		{"max null", descriptor(`,"ownsOutgoing":{"` + typeURL + `":{"max":null}}`), &TypeDescriptor{}},
		{"aliases null", discovery(`,"aliases":null`), &ReadDiscovery{}},
		{"order null", discovery(`,"order":null`), &ReadDiscovery{}},
		{"limits null", discovery(`,"limits":null`), &ReadDiscovery{}},
		{"limits group null", discovery(`,"limits":{"page":null}`), &ReadDiscovery{}},
		{"limit null", discovery(`,"limits":{"page":{"defaultItems":null}}`), &ReadDiscovery{}},
		{"multiplicity null", discovery(`,"maximumEndpointMultiplicity":null`), &ReadDiscovery{}},
		{"multiplicity policy null", discovery(`,"maximumEndpointMultiplicity":[null]`), &ReadDiscovery{}},
		{"policy max null", `{"linkConformsTo":"` + typeURL + `","endpoint":"source","max":null}`, &MaximumEndpointMultiplicityPolicy{}},
		{"summary describes null", `{"id":"` + typeURL + `","name":"X","describes":null}`, &TypeSummary{}},
		{"document null into a struct", `null`, &BeadRecord{}},
		{"document null into a map", `null`, &OwnedLinks{}},
		{"document null into a slice", `null`, &TypeIDs{}},
	} {
		wantDecodeError(t, Unmarshal([]byte(tc.doc), tc.target), tc.name, "null")
	}

	// The one nullable member: a collection's next is present-and-null after
	// the final page, and absent is a different thing — a missing member.
	for _, target := range []any{&BeadCollection{}, &LinkCollection{}, &TypesInventory{}} {
		if err := Unmarshal([]byte(`{"items":[],"next":null}`), target); err != nil {
			t.Errorf("%T: next:null refused: %v", target, err)
		}
		wantDecodeError(t, Unmarshal([]byte(`{"items":[]}`), target), "collection without next", `missing required member "next"`)
	}
	var c BeadCollection
	if err := Unmarshal([]byte(`{"items":[],"next":null}`), &c); err != nil || c.Next != nil || c.Items == nil || len(c.Items) != 0 {
		t.Errorf("final page decoded as %+v, %v", c, err)
	}
	// Absent optional members stay absent (nil / zero), and present empty
	// containers stay present.
	var b BeadRecord
	if err := Unmarshal([]byte(bead(`,"ownedLinks":{}`)), &b); err != nil || b.OwnedLinks == nil || len(b.OwnedLinks) != 0 || b.Attribution != nil || b.Links != nil {
		t.Errorf("present-but-empty ownedLinks decoded as %+v, %v", b, err)
	}
}

func TestStrictDecodeRequiresTheBundlesRequiredMembers(t *testing.T) {
	for _, tc := range []struct {
		doc     string
		target  any
		missing string
	}{
		{`{"id":"` + beadURL + `","type":"` + typeURL + `","properties":{}}`, &BeadRecord{}, "revision"},
		{`{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1"}`, &BeadRecord{}, "properties"},
		{`{"id":"` + linkURL + `","type":"` + typeURL + `","revision":"r1","source":"` + beadURL + `","properties":{}}`, &LinkRecord{}, "target"},
		{`{"id":"` + typeURL + `","name":"X","describes":"bead"}`, &TypeDescriptor{}, "conformsTo"},
		{`{"id":"` + typeURL + `","name":"X","describes":"link","conformsTo":[],"source":{},"target":{"conformsTo":[]}}`, &TypeDescriptor{}, "conformsTo"},
		{`{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"` + typeURL + `":{"label":"l"}}}`, &TypeDescriptor{}, "max"},
		{`{"bdpVersion":"0","profile":"read","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/"}`, &ReadDiscovery{}, "types"},
		{`{"linkConformsTo":"` + typeURL + `","endpoint":"source"}`, &MaximumEndpointMultiplicityPolicy{}, "max"},
		{`{"type":"t","code":"forbidden"}`, &ReadProblem{}, "retry"},
		{`{"principal":"agent:p"}`, &Attribution{}, "status"},
		{`{"revision":"r"}`, &Reference{}, "uri"},
		{`{"uri":"urn:x"}`, &Reference{}, "revision"},
		{`{"id":"` + typeURL + `","name":"X"}`, &TypeSummary{}, "describes"},
		{`{"next":null}`, &TypesInventory{}, "items"},
		{`{}`, &BeadRecord{}, "id"},
	} {
		wantDecodeError(t, Unmarshal([]byte(tc.doc), tc.target), tc.doc, `missing required member "`+tc.missing+`"`)
	}
	// Optional members may be absent: the smallest conforming documents.
	for _, tc := range []struct {
		doc    string
		target any
	}{
		{`{"type":"t","code":"forbidden","retry":"after-state-change"}`, &ReadProblem{}},
		{`{}`, &AdvertisedLimits{}},
		{`{"page":{}}`, &AdvertisedLimits{}},
		{`{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[]}`, &TypeDescriptor{}},
		{`{"conformsTo":[]}`, &EndpointConstraint{}},
		{`{"max":1}`, &OwnedLinkDeclaration{}},
	} {
		if err := Unmarshal([]byte(tc.doc), tc.target); err != nil {
			t.Errorf("%s into %T: %v", tc.doc, tc.target, err)
		}
	}
}

func TestIntegersDecodeExactlyWithinTheDocumentedRange(t *testing.T) {
	for _, tc := range []struct {
		literal string
		want    int
		ok      bool
	}{
		{"1", 1, true}, {"1.0", 1, true}, {"1e0", 1, true}, {"1E+2", 100, true}, {"100e-2", 1, true},
		{"0.5e1", 5, true}, {"1.50e1", 15, true}, {"2.000", 2, true}, {"0", 0, true}, {"-0", 0, true},
		{"0.0e5", 0, true}, {"0e99999999999999999999", 0, true}, {"1e18", 1000000000000000000, true},
		{"9223372036854775807", 9223372036854775807, true}, {"-9223372036854775808", -9223372036854775808, true},
		{"9223372036854775807.0", 9223372036854775807, true}, {"922337203685477580.7e1", 9223372036854775807, true},
		{"1.5", 0, false}, {"250e-2", 0, false}, {"1e-1", 0, false}, {"0.1", 0, false},
		{"1.00000000000000000000000000001", 0, false}, {"1e-99999999999999999", 0, false},
		{"9223372036854775808", 0, false}, {"-9223372036854775809", 0, false}, {"1e19", 0, false},
		{"12345678901234567890", 0, false}, {"1e99999999999999999999", 0, false}, {"1e1000", 0, false},
	} {
		var decl OwnedLinkDeclaration
		err := Unmarshal([]byte(`{"max":`+tc.literal+`}`), &decl)
		if tc.ok {
			if err != nil {
				t.Errorf("max %s: refused: %v", tc.literal, err)
			} else if decl.Max != tc.want {
				t.Errorf("max %s decoded as %d, want %d", tc.literal, decl.Max, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("max %s: decoded as %d, want a refusal (fractional or out of range)", tc.literal, decl.Max)
		}
	}
	// Not a number at all.
	for _, literal := range []string{`"1"`, `true`, `null`, `[1]`, `{}`, `""`} {
		var decl OwnedLinkDeclaration
		wantDecodeError(t, Unmarshal([]byte(`{"max":`+literal+`}`), &decl), "max "+literal, "max")
	}
	// The same rule on every integer member.
	var p ReadProblem
	if err := Unmarshal([]byte(`{"type":"t","code":"resource-pruned","retry":"never","status":410.0}`), &p); err != nil || p.Status != 410 {
		t.Errorf("status 410.0: %+v %v", p, err)
	}
	var limits AdvertisedLimits
	if err := Unmarshal([]byte(`{"page":{"defaultItems":5e1,"maximumItems":2.5e2},"transaction":{"operations":1e2,"duration":"PT1S"}}`), &limits); err != nil ||
		limits.Page.DefaultItems != 50 || limits.Page.MaximumItems != 250 || limits.Transaction.Operations != 100 {
		t.Errorf("limits: %+v %+v %v", limits.Page, limits.Transaction, err)
	}
	var policy MaximumEndpointMultiplicityPolicy
	if err := Unmarshal([]byte(`{"linkConformsTo":"`+typeURL+`","endpoint":"target","max":3.0}`), &policy); err != nil || policy.Max != 3 {
		t.Errorf("policy max 3.0: %+v %v", policy, err)
	}
}

func TestStrictDecodeHoldsEveryMemberToItsJSONType(t *testing.T) {
	bead := func(extra string) string {
		return `{"id":"` + beadURL + `","type":"` + typeURL + `","revision":"r1"` + extra + `}`
	}
	for _, tc := range []struct {
		name    string
		doc     string
		target  any
		mention string
	}{
		{"properties string", bead(`,"properties":"x"`), &BeadRecord{}, "expected a JSON object, got string"},
		{"properties array", bead(`,"properties":[]`), &BeadRecord{}, "expected a JSON object, got array"},
		{"id array", `{"id":[],"type":"t","revision":"r","properties":{}}`, &BeadRecord{}, "expected a JSON string, got array"},
		{"id number", `{"id":1,"type":"t","revision":"r","properties":{}}`, &BeadRecord{}, "expected a JSON string, got number"},
		{"items object", `{"items":{},"next":null}`, &BeadCollection{}, "expected a JSON array, got object"},
		{"item string", `{"items":["x"],"next":null}`, &BeadCollection{}, "items[0]: expected a JSON object, got string"},
		{"next number", `{"items":[],"next":1}`, &BeadCollection{}, "next: expected a JSON string, got number"},
		{"attribution string", bead(`,"properties":{},"attribution":"agent:p"`), &BeadRecord{}, "attribution: expected a JSON object, got string"},
		{"ownedLinks array", bead(`,"properties":{},"ownedLinks":[]`), &BeadRecord{}, "ownedLinks: expected a JSON object, got array"},
		{"ownedLinks entry object", bead(`,"properties":{},"ownedLinks":{"` + typeURL + `":{}}`), &BeadRecord{}, "expected a JSON array, got object"},
		{"conformsTo string", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":"` + typeURL + `"}`, &TypeDescriptor{}, "conformsTo: expected a JSON array, got string"},
		{"conformsTo element number", `{"id":"` + typeURL + `","name":"X","describes":"bead","conformsTo":[1]}`, &TypeDescriptor{}, "conformsTo[0]: expected a JSON string, got number"},
		{"reference number", `1`, &Reference{}, "reference must be a URI string or a pinned-reference object, got number"},
		{"reference array", `["urn:x"]`, &Reference{}, "got array"},
		{"reference boolean", `true`, &Reference{}, "got boolean"},
		{"problem array", `[]`, &ReadProblem{}, "problem must be an object, got array"},
		{"problem string status", `{"type":"t","code":"forbidden","retry":"after-state-change","status":"403"}`, &ReadProblem{}, "status: expected a JSON integer, got string"},
		{"struct from string", `"x"`, &Attribution{}, "expected a JSON object, got string"},
		{"struct from array", `[]`, &BeadRecord{}, "expected a JSON object, got array"},
		{"map from array", `[]`, &OwnedLinks{}, "expected a JSON object, got array"},
		{"slice from object", `{}`, &TypeIDs{}, "expected a JSON array, got object"},
		{"string from number", `1`, new(string), "expected a JSON string, got number"},
		{"int from string", `"1"`, new(int), "expected a JSON integer, got string"},
		{"unsupported kind", `true`, new(bool), "cannot decode a JSON boolean into bool"},
		{"unsupported float", `1.5`, new(float64), "cannot decode a JSON number into float64"},
	} {
		wantDecodeError(t, Unmarshal([]byte(tc.doc), tc.target), tc.name, tc.mention)
	}
}

func TestReadDiscoveryValidateEnforcesTheBundleConstants(t *testing.T) {
	base := func(version, profile, order string) string {
		doc := `{"bdpVersion":"` + version + `","profile":"` + profile + `","scope":"https://s.example/acme/","beads":"https://s.example/acme/beads/","links":"https://s.example/acme/links/","types":"https://s.example/acme/types/"`
		if order != "" {
			doc += `,"order":"` + order + `"`
		}
		return doc + `}`
	}
	for _, tc := range []struct {
		version, profile, order string
		ok                      bool
		mention                 string
	}{
		{"0", "read", "", true, ""},
		{"0", "read", "canonical-uri", true, ""},
		{"1", "read", "", false, `bdpVersion "1"`},
		{"", "read", "", false, `bdpVersion ""`},
		{"0", "read-update", "", false, `profile "read-update"`},
		{"0", "transactional", "", false, `profile "transactional"`},
		{"0", "READ", "", false, `profile "READ"`},
		{"0", "read", "by-title", false, `order "by-title"`},
	} {
		var d ReadDiscovery
		// The SHAPE decodes: version and profile are strings on the wire and
		// their values are Validate's business, not the decoder's.
		if err := Unmarshal([]byte(base(tc.version, tc.profile, tc.order)), &d); err != nil {
			t.Errorf("%s/%s/%s: decode: %v", tc.version, tc.profile, tc.order, err)
			continue
		}
		err := d.Validate()
		if tc.ok {
			if err != nil {
				t.Errorf("%s/%s/%s: Validate: %v", tc.version, tc.profile, tc.order, err)
			}
			continue
		}
		wantDecodeError(t, err, tc.version+"/"+tc.profile+"/"+tc.order, tc.mention)
	}
	// The spec's own Read discovery example validates.
	var spec ReadDiscovery
	if err := Unmarshal(readSchemaFile(t, "spec-examples/1662-scope-discovery-and-human-documentation-1.json"), &spec); err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("the spec's Read discovery example: %v", err)
	}
}

// The encoding/json entry points reach the same decoders, so a Reference or
// a ReadProblem decoded by a caller's json.Unmarshal is held to the closed
// rule too — the two types the bundle states structural facts about.
func TestEncodingJSONEntryPointsStayStrict(t *testing.T) {
	var r Reference
	wantDecodeError(t, json.Unmarshal([]byte(`{"uri":"urn:x","revision":"r","URI":"urn:y"}`), &r), "json.Unmarshal reference", `unknown field "URI"`)
	wantDecodeError(t, json.Unmarshal([]byte(`{"uri":null,"revision":"r"}`), &r), "json.Unmarshal null uri", "null")
	wantDecodeError(t, json.Unmarshal([]byte(`null`), &r), "json.Unmarshal null reference", "got null")
	if err := json.Unmarshal([]byte(`{"uri":"urn:x","revision":"r"}`), &r); err != nil || r.URI != "urn:x" || r.Revision != "r" {
		t.Errorf("json.Unmarshal pinned reference: %+v %v", r, err)
	}
	var p ReadProblem
	wantDecodeError(t, json.Unmarshal([]byte(`{"type":"t","code":"forbidden","code":"x","retry":"never"}`), &p), "json.Unmarshal duplicate code", `duplicate member "code"`)
	wantDecodeError(t, json.Unmarshal([]byte(`{"type":"t","code":"forbidden","retry":"never","archivedAt":null}`), &p), "json.Unmarshal archivedAt null", "null")
	wantDecodeError(t, json.Unmarshal([]byte(`null`), &p), "json.Unmarshal null problem", "problem must be an object")
	// A record decoded through encoding/json is lenient on its OWN members
	// (documented in doc.go) but its nested Reference is not.
	var l LinkRecord
	wantDecodeError(t, json.Unmarshal([]byte(`{"id":"x","type":"t","revision":"r","source":{"uri":"u","revision":"r","URI":"v"},"target":"urn:x","properties":{}}`), &l), "json.Unmarshal nested reference", `unknown field "URI"`)
}

func TestUnmarshalTargetsAndReaders(t *testing.T) {
	wantDecodeError(t, Unmarshal([]byte(`{}`), Attribution{}), "non-pointer target", "non-nil pointer")
	var nilTarget *Attribution
	wantDecodeError(t, Unmarshal([]byte(`{}`), nilTarget), "nil pointer target", "non-nil pointer")
	wantDecodeError(t, Unmarshal(nil, &Attribution{}), "empty input", "EOF")
	wantDecodeError(t, Unmarshal([]byte(`{"principal":`), &Attribution{}), "truncated input", "unexpected EOF")
	wantDecodeError(t, Decode(failingReader{}, &Attribution{}), "failing reader", "read failed")
	// Whitespace anywhere outside strings is not significant.
	var a Attribution
	if err := Unmarshal([]byte(" {\n\t\"principal\" :\t\"agent:p\" ,\n \"status\": \"claimed\" }\n"), &a); err != nil || a.Principal != "agent:p" {
		t.Errorf("whitespace: %+v %v", a, err)
	}
	// Map keys are exact too, and values decode recursively with their path
	// in the diagnostic.
	var owned OwnedLinks
	wantDecodeError(t, Unmarshal([]byte(`{"`+typeURL+`":[{"id":"x"}]}`), &owned), "owned link path", `OwnedLinks.`+typeURL+`[0]: missing required member "type"`)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
