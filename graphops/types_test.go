package graphops_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/graphops"
)

var lowerHex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRevision(t *testing.T) {
	a, b := graphops.MintRevision(), graphops.MintRevision()
	if !lowerHex32.MatchString(a.String()) || !lowerHex32.MatchString(b.String()) {
		t.Fatalf("minted revisions must be 32 lowercase hex digits: %q %q", a, b)
	}
	if a.Equal(b) {
		t.Fatal("two minted revisions collided")
	}
	if _, err := graphops.NewRevision(""); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("empty revision: %v", err)
	}
	foreign, err := graphops.NewRevision("opaque-task-revision")
	if err != nil || foreign.String() != "opaque-task-revision" || foreign.IsZero() {
		t.Fatalf("a foreign authority's revision must be carried as written: %v", err)
	}
	if !(graphops.Revision{}).IsZero() {
		t.Fatal("zero revision must report IsZero")
	}
	if !foreign.Equal(foreign) {
		t.Fatal("a revision equals itself")
	}
}

// Every opaque string a value carries must be valid UTF-8, because
// encoding/json launders an invalid byte to U+FFFD: "\xff" and "\xfe" are two
// Go strings with ONE serialization, and would be two revisions with one
// ledger hash. The constructors refuse them; valid non-ASCII is carried and
// hashed as written.
func TestStringsMustBeValidUTF8(t *testing.T) {
	const scope = "https://beads.example/acme/"
	// The hazard, stated: encoding/json cannot tell the two apart.
	ff, _ := json.Marshal("\xff")
	fe, _ := json.Marshal("\xfe")
	if string(ff) != string(fe) || string(ff) != `"\ufffd"` {
		t.Fatalf("test premise: encoding/json serializes \\xff as %s and \\xfe as %s", ff, fe)
	}
	okDecl, _ := graphops.NewOwnedLinkDecl("https://work.example/types/c", "", 1)
	for name, construct := range map[string]func(s string) error{
		"revision":  func(s string) error { _, err := graphops.NewRevision(s); return err },
		"principal": func(s string) error { _, err := graphops.NewAttribution(s, graphops.AttributionClaimed); return err },
		"in-Scope pin": func(s string) error {
			_, err := graphops.NewInScopeRef("beads/x", s)
			return err
		},
		"parsed local pin": func(s string) error { _, err := graphops.ParseRef(scope, "beads/x", s); return err },
		"parsed external pin": func(s string) error {
			_, err := graphops.ParseRef(scope, "urn:external:pin-witness", s)
			return err
		},
		"label": func(s string) error {
			_, err := graphops.NewOwnedLinkDecl("https://work.example/types/c", s, 1)
			return err
		},
		"descriptor name": func(s string) error {
			_, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: s, Describes: graphops.KindBead})
			return err
		},
		"descriptor description": func(s string) error {
			_, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Description: s, Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{okDecl}})
			return err
		},
	} {
		for _, bad := range []string{"\xff", "\xfe", "a\xffb", "\xc3", "\xed\xa0\x80", "\xc0\x80"} {
			if err := construct(bad); !errors.Is(err, graphops.ErrValidation) {
				t.Errorf("%s %q: want ErrValidation, got %v", name, bad, err)
			}
		}
		for _, good := range []string{"plain", "  Cited-9F2c — α/β (draft) Å\t", "é", "\U0001F600", "�"} {
			if err := construct(good); err != nil {
				t.Errorf("%s %q: valid UTF-8 refused: %v", name, good, err)
			}
		}
	}
	// Valid, distinct revisions hash distinctly and round-trip as written.
	alpha, _ := graphops.NewRevision("rev-α")
	beta, _ := graphops.NewRevision("rev-β")
	spec := graphops.LedgerEventSpec{
		Seq: 1, Kind: graphops.LedgerUpdate, OpID: opID, Path: "beads/x", ResourceKind: graphops.KindBead,
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash,
	}
	spec.Revision = alpha
	a, err := graphops.NewLedgerEvent(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Revision = beta
	b, err := graphops.NewLedgerEvent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() == b.Hash() || !strings.Contains(string(a.CanonicalBytes()), `"revision":"rev-α"`) {
		t.Fatalf("revisions must be hashed as written: %s / %s", a.CanonicalBytes(), b.CanonicalBytes())
	}
	if !a.Revision().Equal(alpha) || a.Revision().String() != "rev-α" {
		t.Fatal("the revision must round-trip byte for byte")
	}
}

func TestAttribution(t *testing.T) {
	if _, err := graphops.NewAttribution("", graphops.AttributionClaimed); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("empty principal: %v", err)
	}
	if _, err := graphops.NewAttribution("agent:x", "verified"); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("a status asserting authentication must not exist: %v", err)
	}
	a, err := graphops.NewAttribution("agent:planner", graphops.AttributionUnknown)
	if err != nil || a.Principal() != "agent:planner" || a.Status() != graphops.AttributionUnknown || a.IsZero() {
		t.Fatalf("attribution: %+v %v", a, err)
	}
	if !(graphops.Attribution{}).IsZero() {
		t.Fatal("zero attribution is absent")
	}
	for _, s := range []graphops.AttributionStatus{graphops.AttributionClaimed, graphops.AttributionUnknown} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if graphops.AttributionStatus("").Valid() {
		t.Error("empty status should not be valid")
	}
}

func TestProperties(t *testing.T) {
	p, err := graphops.NewProperties([]byte(` {"b" : 1 , "a" : 1.0 } `))
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != `{"a":1,"b":1}` || string(p.Bytes()) != `{"a":1,"b":1}` || p.IsEmpty() {
		t.Fatalf("canonical properties: %s", p)
	}
	for _, in := range []string{`[1]`, `"x"`, `1`, `null`, `true`, `{"a":1,"a":2}`, `{`, ``, `{} x`} {
		if _, err := graphops.NewProperties([]byte(in)); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("properties %q: want ErrValidation, got %v", in, err)
		}
	}
	empty, err := graphops.NewProperties([]byte(` { } `))
	if err != nil || !empty.IsEmpty() || empty.String() != "{}" {
		t.Fatalf("empty object: %v %s", err, empty)
	}
	var zero graphops.Properties
	if string(zero.Bytes()) != "{}" || zero.String() != "{}" || !zero.IsEmpty() || !zero.Equal(empty) {
		t.Fatal("zero Properties is the empty object")
	}
	// Equality is RFC 6902 §4.6.
	q, _ := graphops.NewProperties([]byte(`{"a":1.0,"b":100e-2}`))
	if !p.Equal(q) || !q.Equal(p) {
		t.Fatal("numerically equal documents must be Equal")
	}
	r, _ := graphops.NewProperties([]byte(`{"a":1,"b":2}`))
	if p.Equal(r) {
		t.Fatal("different documents must not be Equal")
	}
	// Bytes() is a copy.
	b := p.Bytes()
	b[1] = 'x'
	if p.String() != `{"a":1,"b":1}` {
		t.Fatal("Bytes() must not alias the value")
	}
	// JSON round trip through a struct.
	var holder struct {
		P graphops.Properties `json:"p"`
	}
	if err := json.Unmarshal([]byte(`{"p":{"z":1,"y":2}}`), &holder); err != nil {
		t.Fatal(err)
	}
	if holder.P.String() != `{"y":2,"z":1}` {
		t.Fatalf("unmarshaled %s", holder.P)
	}
	out, err := json.Marshal(holder)
	if err != nil || string(out) != `{"p":{"y":2,"z":1}}` {
		t.Fatalf("marshaled %s %v", out, err)
	}
	if err := json.Unmarshal([]byte(`{"p":[1]}`), &holder); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("unmarshal of a non-object: %v", err)
	}
	raw, err := p.MarshalJSON()
	if err != nil || string(raw) != p.String() {
		t.Fatalf("MarshalJSON: %s %v", raw, err)
	}
}

func TestRef(t *testing.T) {
	const scope = "https://beads.example/acme/"
	in, err := graphops.NewInScopeRef("beads/task-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if !in.InScope() || in.Path() != "beads/task-42" || in.URI() != "" || in.Pinned() || in.Pin() != "" || in.IsZero() ||
		in.URL(scope) != "https://beads.example/acme/beads/task-42" {
		t.Fatalf("in-Scope ref: %+v", in)
	}
	if _, err := graphops.NewInScopeRef("links/x", ""); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("a Link path is not an endpoint: %v", err)
	}
	if _, err := graphops.NewInScopeRef("beads/caf%c3%a9", ""); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("a noncanonical path is refused, not trimmed: %v", err)
	}
	if !(graphops.Ref{}).IsZero() {
		t.Fatal("zero Ref is absent")
	}

	for _, tc := range []struct {
		ref     string
		inScope bool
		path    string
		uri     string
		reject  bool
	}{
		{"beads/task-42", true, "beads/task-42", "", false},
		{"https://beads.example/acme/beads/task-42", true, "beads/task-42", "", false},
		{"https://beads.example/acme/beads/Task-42", true, "beads/Task-42", "", false},
		{"https://beads.example/acme/beads/a/b/c", true, "beads/a/b/c", "", false},
		// Aliases of the Scope URL claim the Scope and must be canonical.
		{"https://BEADS.example/acme/beads/task-42", false, "", "", true},
		{"HTTPS://beads.example/acme/beads/task-42", false, "", "", true},
		{"https://beads.example:443/acme/beads/task-42", false, "", "", true},
		{"https://beads.example/acme/./beads/task-42", false, "", "", true},
		{"https://beads.example/x/../acme/beads/task-42", false, "", "", true},
		{"https://beads.example/acme/beads/task%2D42", false, "", "", true},
		{"https://beads.example/acme/beads/caf%c3%a9", false, "", "", true},
		{"https://beads.example/acme/beads/task-42?x=1", false, "", "", true},
		{"https://beads.example/acme/beads/task-42#f", false, "", "", true},
		// Under the Scope but not a Bead.
		{"https://beads.example/acme/links/l1", false, "", "", true},
		{"https://beads.example/acme/alias/x", false, "", "", true},
		{"https://beads.example/acme/", false, "", "", true},
		{"https://beads.example/acme/other/x", false, "", "", true},
		{"https://beads.example/acme/beads/", false, "", "", true},
		// Local spellings that are not Bead IDs.
		{"links/l1", false, "", "", true},
		{"alias/x", false, "", "", true},
		{"beads/x?y", false, "", "", true},
		{"beads/", false, "", "", true},
		{"", false, "", "", true},
		{"not a uri", false, "", "", true},
		{"//beads.example/acme/beads/x", false, "", "", true},
		{"../beads/x", false, "", "", true},
		{"https://beads.example/acme/beads/a b", false, "", "", true},
		{"urn:isbn:0451450523 x", false, "", "", true},
		{"ht_tp://x", false, "", "", true},
		{"1abc:xyz", false, "", "", true},
		{"urn:x%2", false, "", "", true},
		{"urn:x%zz", false, "", "", true},
		// Aliases through an empty port and through dot segments at the end.
		{"https://beads.example:/acme/beads/x", false, "", "", true},
		{"https://beads.example/acme/beads/x/.", false, "", "", true},
		{"https://beads.example/acme/beads/x/..", false, "", "", true},
		{"https://beads.example/../acme/beads/x", false, "", "", true},
		// External: preserved byte-identically.
		{"https://github.example/issues/123", false, "", "https://github.example/issues/123", false},
		{"https://beads.example/acmeX/beads/x", false, "", "https://beads.example/acmeX/beads/x", false},
		{"https://beads.example/acme", false, "", "https://beads.example/acme", false},
		{"https://beads.example:8443/acme/beads/x", false, "", "https://beads.example:8443/acme/beads/x", false},
		{"http://beads.example/acme/beads/x", false, "", "http://beads.example/acme/beads/x", false},
		{"https://other.example/acme/beads/x", false, "", "https://other.example/acme/beads/x", false},
		{"urn:isbn:0451450523", false, "", "urn:isbn:0451450523", false},
		{"mailto:a@b.example", false, "", "mailto:a@b.example", false},
		{"HTTPS://Other.Example/X", false, "", "HTTPS://Other.Example/X", false},
		{"https://[::1]/beads/x", false, "", "https://[::1]/beads/x", false},
		{"https://beads.example", false, "", "https://beads.example", false},
		{"https://[zzz]/x", false, "", "https://[zzz]/x", false},
		// Aliases of the Scope's ORIGIN under the WHATWG parser: a zero-padded
		// default port, a percent-encoded host character, mixed case in the
		// escape and the host. Each claims the Scope and is not canonical.
		{"https://beads.example:0443/acme/beads/x", false, "", "", true},
		{"https://beads.example:00443/acme/beads/x", false, "", "", true},
		{"https://beads%2Eexample/acme/beads/x", false, "", "", true},
		{"https://beads%2eexample/acme/beads/x", false, "", "", true},
		{"https://%42eads.example/acme/beads/x", false, "", "", true},
		// Not aliases: a trailing-dot host is a different host to the parser
		// (kept as written, so the external spelling survives byte for byte);
		// a host or port the parser cannot parse claims nothing.
		{"https://beads.example./acme/beads/x", false, "", "https://beads.example./acme/beads/x", false},
		{"https://BEADS.example./acme/beads/x", false, "", "https://BEADS.example./acme/beads/x", false},
		{"https://beads.example:99999/acme/beads/x", false, "", "https://beads.example:99999/acme/beads/x", false},
		{"https://beads.example:4x3/acme/beads/x", false, "", "https://beads.example:4x3/acme/beads/x", false},
		{"https://beads%2fexample/acme/beads/x", false, "", "https://beads%2fexample/acme/beads/x", false},
		{"https://beads.example:8443/acme/beads/x", false, "", "https://beads.example:8443/acme/beads/x", false},
		{"https://beads.123/acme/beads/x", false, "", "https://beads.123/acme/beads/x", false},
		{"https://beads.example%3A443/acme/beads/x", false, "", "https://beads.example%3A443/acme/beads/x", false},
	} {
		got, err := graphops.ParseRef(scope, tc.ref, "")
		if tc.reject {
			if !errors.Is(err, graphops.ErrValidation) {
				t.Errorf("ParseRef(%q): want ErrValidation, got %v (%+v)", tc.ref, err, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tc.ref, err)
			continue
		}
		if got.InScope() != tc.inScope || got.Path() != tc.path || got.URI() != tc.uri {
			t.Errorf("ParseRef(%q) = inScope %v path %q uri %q", tc.ref, got.InScope(), got.Path(), got.URI())
		}
		if got.URL(scope) != tc.uri && got.URL(scope) != scope+tc.path {
			t.Errorf("ParseRef(%q).URL = %q", tc.ref, got.URL(scope))
		}
	}
	// Aliases of numeric Scope hosts: every IPv4 spelling the parser
	// rewrites to dotted decimal, and every IPv6 spelling it compresses,
	// claims the Scope; only the serializer's spelling is the Bead's.
	for _, tc := range []struct{ scope, ref string }{
		{"https://[::1]:8443/s/", "https://[0:0:0:0:0:0:0:1]:8443/s/beads/x"},
		{"https://[::1]:8443/s/", "https://[::0001]:8443/s/beads/x"},
		{"https://[::1]:8443/s/", "https://[::1]:08443/s/beads/x"},
		{"https://[::ffff:102:304]/s/", "https://[::ffff:1.2.3.4]/s/beads/x"},
		{"https://[::ffff:102:304]/s/", "https://[::FFFF:102:304]/s/beads/x"},
		{"https://127.0.0.1/s/", "https://0x7f000001/s/beads/x"},
		{"https://127.0.0.1/s/", "https://0X7F000001/s/beads/x"},
		{"https://127.0.0.1/s/", "https://127.1/s/beads/x"},
		{"https://127.0.0.1/s/", "https://2130706433/s/beads/x"},
		{"https://127.0.0.1/s/", "https://0177.0.0.1/s/beads/x"},
		{"https://127.0.0.1/s/", "https://0x7f.0.0.1/s/beads/x"},
		{"https://127.0.0.1/s/", "https://127.0.0.1./s/beads/x"},
		{"https://127.0.0.1/s/", "https://127.0.0.1:443/s/beads/x"},
		{"https://127.0.0.1/s/", "https://127.0.0.1:0443/s/beads/x"},
		{"https://beads.example./s/", "https://BEADS.EXAMPLE./s/beads/x"},
		{"https://beads.example./s/", "https://beads%2Eexample./s/beads/x"},
	} {
		got, err := graphops.ParseRef(tc.scope, tc.ref, "")
		if !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("ParseRef(%q, %q): an alias of the Scope origin must be refused as noncanonical, got %v %+v", tc.scope, tc.ref, err, got)
		}
	}
	for _, tc := range []struct{ scope, ref string }{
		{"https://[::1]:8443/s/", "https://[::1]:8443/s/beads/x"},
		{"https://[::ffff:102:304]/s/", "https://[::ffff:102:304]/s/beads/x"},
		{"https://127.0.0.1/s/", "https://127.0.0.1/s/beads/x"},
		{"https://beads.example./s/", "https://beads.example./s/beads/x"},
	} {
		got, err := graphops.ParseRef(tc.scope, tc.ref, "")
		if err != nil || !got.InScope() || got.Path() != "beads/x" {
			t.Errorf("ParseRef(%q, %q): canonical in-Scope reference refused: %v %+v", tc.scope, tc.ref, err, got)
		}
	}
	// The undotted host is external to a trailing-dot Scope, and vice versa.
	if got, err := graphops.ParseRef("https://beads.example./s/", "https://beads.example/s/beads/x", ""); err != nil || got.InScope() {
		t.Errorf("undotted host against a trailing-dot Scope: %v %+v", err, got)
	}
	// An invalid Scope is the caller's error.
	if _, err := graphops.ParseRef("https://beads.example/acme", "beads/x", ""); !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("invalid scope: %v", err)
	}
	// Pins are provenance, not identity.
	pinned, _ := graphops.ParseRef(scope, "beads/x", "rev1")
	unpinned, _ := graphops.ParseRef(scope, "https://beads.example/acme/beads/x", "")
	if !pinned.Pinned() || pinned.Pin() != "rev1" || !pinned.SameURI(unpinned) || pinned.Equal(unpinned) || !pinned.Equal(pinned) {
		t.Fatal("pins must be ignored by SameURI and honored by Equal")
	}
	ext, _ := graphops.ParseRef(scope, "https://github.example/issues/123", "8f0e2b")
	if !ext.Pinned() || ext.Pin() != "8f0e2b" || ext.SameURI(pinned) || ext.URL(scope) != "https://github.example/issues/123" {
		t.Fatal("external pin must be carried byte-identically")
	}
}

func TestBeadAndLink(t *testing.T) {
	const scope = "https://beads.example/acme/"
	rev := graphops.MintRevision()
	attr, _ := graphops.NewAttribution("agent:planner", graphops.AttributionClaimed)
	props, _ := graphops.NewProperties([]byte(`{"title":"Specify BDP mutation","status":"open"}`))
	bead, err := graphops.NewBead(graphops.BeadSpec{Path: "beads/task-42", TypeURL: "https://work.example/types/task", Revision: rev, Attribution: attr, Properties: props})
	if err != nil {
		t.Fatal(err)
	}
	gotAttr, present := bead.Attribution()
	if bead.Path() != "beads/task-42" || bead.TypeURL() != "https://work.example/types/task" || !bead.Revision().Equal(rev) ||
		!present || gotAttr != attr || !bead.Properties().Equal(props) || bead.URL(scope) != scope+"beads/task-42" || bead.IsZero() {
		t.Fatalf("bead accessors: %+v", bead)
	}
	plain, _ := graphops.NewBead(graphops.BeadSpec{Path: "beads/x", TypeURL: "https://work.example/types/task", Revision: rev})
	if _, present := plain.Attribution(); present || !plain.Properties().IsEmpty() {
		t.Fatal("absent attribution and empty properties are the defaults")
	}
	if !(graphops.Bead{}).IsZero() {
		t.Fatal("zero Bead")
	}
	for name, spec := range map[string]graphops.BeadSpec{
		"bad path":      {Path: "beads/x/", TypeURL: "https://work.example/types/task", Revision: rev},
		"link path":     {Path: "links/x", TypeURL: "https://work.example/types/task", Revision: rev},
		"bad type":      {Path: "beads/x", TypeURL: "https://Work.example/types/task", Revision: rev},
		"zero revision": {Path: "beads/x", TypeURL: "https://work.example/types/task"},
	} {
		if _, err := graphops.NewBead(spec); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("bead %s: want ErrValidation, got %v", name, err)
		}
	}

	source, _ := graphops.NewInScopeRef("beads/task-42", "")
	target, _ := graphops.ParseRef(scope, "https://beads.example/acme/beads/person-7", "")
	external, _ := graphops.ParseRef(scope, "https://github.example/issues/123", "8f0e2b")
	link, err := graphops.NewLink(graphops.LinkSpec{Path: "links/assigned-to-81", TypeURL: "https://work.example/types/assigned-to", Revision: rev, Source: source, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if link.Path() != "links/assigned-to-81" || link.TypeURL() != "https://work.example/types/assigned-to" || !link.Revision().Equal(rev) ||
		!link.Source().SameURI(source) || !link.Target().SameURI(target) || link.URL(scope) != scope+"links/assigned-to-81" || link.IsZero() {
		t.Fatalf("link accessors: %+v", link)
	}
	if _, present := link.Attribution(); present || !link.Properties().IsEmpty() {
		t.Fatal("absent attribution and empty properties are the defaults")
	}
	ext, err := graphops.NewLink(graphops.LinkSpec{Path: "links/cites/1", TypeURL: "https://work.example/types/cites", Revision: rev, Source: source, Target: external, Attribution: attr, Properties: props})
	if err != nil || !ext.Target().Pinned() || ext.Target().URI() != "https://github.example/issues/123" {
		t.Fatalf("external target: %v", err)
	}
	if a, present := ext.Attribution(); !present || a != attr || !ext.Properties().Equal(props) {
		t.Fatal("link attribution and properties")
	}
	if !(graphops.Link{}).IsZero() {
		t.Fatal("zero Link")
	}
	// The endpoints are symmetric: an external source with an in-Scope target
	// is a Link this Scope owns.
	extSource, err := graphops.NewLink(graphops.LinkSpec{Path: "links/x", TypeURL: "https://work.example/types/cites", Revision: rev, Source: external, Target: target})
	if err != nil || extSource.Source().InScope() || extSource.Source().URI() != "https://github.example/issues/123" || !extSource.Target().InScope() {
		t.Fatalf("external source with an in-Scope target: %v %+v", err, extSource)
	}
	for name, spec := range map[string]graphops.LinkSpec{
		"bad path":      {Path: "beads/x", TypeURL: "https://work.example/types/cites", Revision: rev, Source: source, Target: target},
		"bad type":      {Path: "links/x", TypeURL: "work.example/types/cites", Revision: rev, Source: source, Target: target},
		"zero revision": {Path: "links/x", TypeURL: "https://work.example/types/cites", Source: source, Target: target},
		"both external": {Path: "links/x", TypeURL: "https://work.example/types/cites", Revision: rev, Source: external, Target: external},
		"zero source":   {Path: "links/x", TypeURL: "https://work.example/types/cites", Revision: rev, Target: target},
		"zero target":   {Path: "links/x", TypeURL: "https://work.example/types/cites", Revision: rev, Source: source},
		"both zero":     {Path: "links/x", TypeURL: "https://work.example/types/cites", Revision: rev},
	} {
		if _, err := graphops.NewLink(spec); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("link %s: want ErrValidation, got %v", name, err)
		}
	}
}

// TestLinkEndpointsAreSymmetric admits every endpoint pair the pinned Read
// fixtures carry (packages/conformance/fixtures/read-reference-v1.json at the
// pin, `links` and `externalEndpointLinks`), spelled exactly as the fixture
// spells them — local Bead IDs, opaque external URIs, and pins as written —
// and refuses the one pair the spec forbids: two external endpoints.
func TestLinkEndpointsAreSymmetric(t *testing.T) {
	const scope = "https://scope.example/acme/"
	rev := graphops.MintRevision()
	ref := func(uri, pin string) graphops.Ref {
		t.Helper()
		r, err := graphops.ParseRef(scope, uri, pin)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", uri, err)
		}
		return r
	}
	for _, tc := range []struct {
		name                  string
		source, target        graphops.Ref
		sourceIn, targetIn    bool
		sourcePin, targetPin  string
		sourceURI, targetPath string
	}{
		{"in-Scope → in-Scope", ref("beads/demo-b", ""), ref("beads/demo-a", ""), true, true, "", "", "", "beads/demo-a"},
		{"in-Scope → pinned external", ref("beads/demo-f", ""), ref("external:beads:mol-run-assignee", "  Cited-9F2c \u2014 \u03b1/\u03b2 (draft) A\u030a\t"), true, false, "", "  Cited-9F2c \u2014 \u03b1/\u03b2 (draft) A\u030a\t", "", ""},
		{"external → in-Scope", ref("external:beads:mol-run-assignee", ""), ref("beads/demo-f", ""), false, true, "", "", "external:beads:mol-run-assignee", "beads/demo-f"},
		{"external → pinned in-Scope", ref("urn:external:pin-witness", ""), ref("beads/demo-f", "pin-a-r1 (as-written)"), false, true, "", "pin-a-r1 (as-written)", "urn:external:pin-witness", "beads/demo-f"},
		{"collation witness → in-Scope", ref("urn:external:collation-witness", ""), ref("beads/demo-f", ""), false, true, "", "", "urn:external:collation-witness", "beads/demo-f"},
	} {
		link, err := graphops.NewLink(graphops.LinkSpec{Path: "links/external-source", TypeURL: "https://work.example/types/blocks", Revision: rev, Source: tc.source, Target: tc.target})
		if err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
			continue
		}
		s, tg := link.Source(), link.Target()
		if s.InScope() != tc.sourceIn || tg.InScope() != tc.targetIn || s.Pin() != tc.sourcePin || tg.Pin() != tc.targetPin ||
			s.URI() != tc.sourceURI || tg.Path() != tc.targetPath || !s.Equal(tc.source) || !tg.Equal(tc.target) {
			t.Errorf("%s: endpoints not carried as written: source %+v target %+v", tc.name, s, tg)
		}
	}
	_, err := graphops.NewLink(graphops.LinkSpec{Path: "links/x", TypeURL: "https://work.example/types/blocks", Revision: rev,
		Source: ref("urn:external:pin-witness", ""), Target: ref("external:beads:mol-run-assignee", "w-1")})
	if !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("a Link between two external URIs must be refused: %v", err)
	}
}

const assignedToJSON = `{
  "id": "https://work.example/types/assigned-to",
  "name": "Assigned To",
  "description": "Associates a work item with the person responsible for it.",
  "describes": "link",
  "conformsTo": [],
  "propertiesSchema": "https://work.example/schemas/assigned-to-properties-v1",
  "source": { "conformsTo": ["https://work.example/types/issue"] },
  "target": { "conformsTo": ["https://people.example/types/person"], "external": "none" }
}`

const decisionJSON = `{
  "id": "https://work.example/types/decision",
  "name": "Decision",
  "describes": "bead",
  "conformsTo": [],
  "ownsOutgoing": {
    "https://work.example/types/cites": { "label": "cites", "max": 8 },
    "https://work.example/types/blocks": { "max": 2 }
  }
}`

func TestTypeDescriptorParseAndCanonicalForm(t *testing.T) {
	link, err := graphops.ParseTypeDescriptor([]byte(assignedToJSON))
	if err != nil {
		t.Fatal(err)
	}
	desc, hasDesc := link.Description()
	schema, hasSchema := link.PropertiesSchema()
	src, hasSrc := link.Source()
	tgt, hasTgt := link.Target()
	tgtExternal, tgtHasExternal := tgt.External()
	_, srcHasExternal := src.External()
	if link.ID() != "https://work.example/types/assigned-to" || link.Name() != "Assigned To" || !hasDesc || desc == "" ||
		link.Describes() != graphops.KindLink || len(link.ConformsTo()) != 0 || !hasSchema || schema != "https://work.example/schemas/assigned-to-properties-v1" ||
		!hasSrc || !hasTgt || src.ConformsTo()[0] != "https://work.example/types/issue" || tgt.ConformsTo()[0] != "https://people.example/types/person" ||
		!tgtHasExternal || tgtExternal != graphops.ExternalNone || tgt.EffectiveExternal() != graphops.ExternalNone ||
		srcHasExternal || src.EffectiveExternal() != graphops.ExternalOpaque || len(link.OwnsOutgoing()) != 0 || link.IsZero() {
		t.Fatalf("parsed link descriptor: %+v", link)
	}
	wantCanonical := `{"conformsTo":[],"describes":"link","description":"Associates a work item with the person responsible for it.","id":"https://work.example/types/assigned-to","name":"Assigned To","propertiesSchema":"https://work.example/schemas/assigned-to-properties-v1","source":{"conformsTo":["https://work.example/types/issue"]},"target":{"conformsTo":["https://people.example/types/person"],"external":"none"}}`
	if got := string(link.CanonicalJSON()); got != wantCanonical {
		t.Fatalf("canonical descriptor\n got %s\nwant %s", got, wantCanonical)
	}
	if link.Fingerprint() != sha256Hex([]byte(wantCanonical)) {
		t.Fatal("fingerprint is sha256 of the canonical bytes")
	}
	again, err := graphops.ParseTypeDescriptor(link.CanonicalJSON())
	if err != nil || again.Fingerprint() != link.Fingerprint() {
		t.Fatalf("canonical form must round-trip to the same fingerprint: %v", err)
	}

	bead, err := graphops.ParseTypeDescriptor([]byte(decisionJSON))
	if err != nil {
		t.Fatal(err)
	}
	owns := bead.OwnsOutgoing()
	if len(owns) != 2 || owns[0].TypeURL() != "https://work.example/types/blocks" || owns[1].TypeURL() != "https://work.example/types/cites" {
		t.Fatalf("ownsOutgoing must be in code-unit order: %+v", owns)
	}
	cites, ok := bead.Owns("https://work.example/types/cites")
	label, hasLabel := cites.Label()
	if !ok || cites.Max() != 8 || !hasLabel || label != "cites" {
		t.Fatalf("Owns(cites): %+v %v", cites, ok)
	}
	if _, hasLabel := owns[0].Label(); hasLabel || owns[0].Max() != 2 {
		t.Fatal("blocks declaration has no label and max 2")
	}
	if _, ok := bead.Owns("https://work.example/types/relates"); ok {
		t.Fatal("an undeclared Link Type is not owned")
	}
	if _, hasDesc := bead.Description(); hasDesc {
		t.Fatal("absent description")
	}
	if _, hasSchema := bead.PropertiesSchema(); hasSchema {
		t.Fatal("absent propertiesSchema")
	}
	if _, hasSrc := bead.Source(); hasSrc {
		t.Fatal("a Bead Type has no source constraint")
	}
	wantBead := `{"conformsTo":[],"describes":"bead","id":"https://work.example/types/decision","name":"Decision","ownsOutgoing":{"https://work.example/types/blocks":{"max":2},"https://work.example/types/cites":{"label":"cites","max":8}}}`
	if got := string(bead.CanonicalJSON()); got != wantBead {
		t.Fatalf("canonical bead descriptor\n got %s\nwant %s", got, wantBead)
	}
	// ownsOutgoing input order does not matter; conformsTo order does.
	cites8, _ := graphops.NewOwnedLinkDecl("https://work.example/types/cites", "cites", 8)
	blocks2, _ := graphops.NewOwnedLinkDecl("https://work.example/types/blocks", "", 2)
	reordered, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/decision", Name: "Decision", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{cites8, blocks2}})
	if err != nil || reordered.Fingerprint() != bead.Fingerprint() {
		t.Fatalf("ownsOutgoing order must not change the fingerprint: %v", err)
	}
	// conformsTo is a SET: the canonical form sorts it by code unit, so the
	// authored order does not reach the fingerprint.
	ab, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/t", Name: "T", Describes: graphops.KindBead, ConformsTo: []string{"https://work.example/types/a", "https://work.example/types/b"}})
	ba, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/t", Name: "T", Describes: graphops.KindBead, ConformsTo: []string{"https://work.example/types/b", "https://work.example/types/a"}})
	if ab.Fingerprint() != ba.Fingerprint() || ab.ConformsTo()[0] != "https://work.example/types/a" || ba.ConformsTo()[0] != "https://work.example/types/a" {
		t.Fatal("conformsTo is a set: reordered parents must fingerprint identically and read back sorted")
	}
	wantSorted := `{"conformsTo":["https://work.example/types/a","https://work.example/types/b"],"describes":"bead","id":"https://work.example/types/t","name":"T"}`
	if got := string(ba.CanonicalJSON()); got != wantSorted {
		t.Fatalf("canonical conformsTo\n got %s\nwant %s", got, wantSorted)
	}
	// Code-unit order, not locale order: uppercase sorts before lowercase.
	cased, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/t", Name: "T", Describes: graphops.KindBead, ConformsTo: []string{"https://work.example/types/b", "https://work.example/types/B"}})
	if got := cased.ConformsTo(); got[0] != "https://work.example/types/B" || got[1] != "https://work.example/types/b" {
		t.Fatalf("conformsTo must sort by code unit: %v", got)
	}
	// The same law for an endpoint's conformsTo, and through the parser.
	const linkAB = `{"id":"https://work.example/types/l","name":"L","describes":"link","conformsTo":["https://work.example/types/y","https://work.example/types/x"],"source":{"conformsTo":["https://work.example/types/b","https://work.example/types/a"]},"target":{"conformsTo":[]}}`
	const linkBA = `{"id":"https://work.example/types/l","name":"L","describes":"link","conformsTo":["https://work.example/types/x","https://work.example/types/y"],"source":{"conformsTo":["https://work.example/types/a","https://work.example/types/b"]},"target":{"conformsTo":[]}}`
	lab, err := graphops.ParseTypeDescriptor([]byte(linkAB))
	if err != nil {
		t.Fatal(err)
	}
	lba, err := graphops.ParseTypeDescriptor([]byte(linkBA))
	if err != nil {
		t.Fatal(err)
	}
	wantLink := `{"conformsTo":["https://work.example/types/x","https://work.example/types/y"],"describes":"link","id":"https://work.example/types/l","name":"L","source":{"conformsTo":["https://work.example/types/a","https://work.example/types/b"]},"target":{"conformsTo":[]}}`
	if lab.Fingerprint() != lba.Fingerprint() || string(lab.CanonicalJSON()) != wantLink {
		t.Fatalf("endpoint conformsTo must be sorted in the canonical form:\n got %s\nwant %s", lab.CanonicalJSON(), wantLink)
	}
	labSrc, _ := lab.Source()
	if got := labSrc.ConformsTo(); got[0] != "https://work.example/types/a" {
		t.Fatalf("endpoint conformsTo must read back sorted: %v", got)
	}
	// Accessors return copies.
	ab.ConformsTo()[0] = "mutated"
	ab.OwnsOutgoing()
	if ab.ConformsTo()[0] != "https://work.example/types/a" {
		t.Fatal("ConformsTo must not alias the value")
	}
	c := ab.CanonicalJSON()
	c[0] = 'x'
	if ab.CanonicalJSON()[0] != '{' {
		t.Fatal("CanonicalJSON must not alias the value")
	}
	if !(graphops.TypeDescriptor{}).IsZero() {
		t.Fatal("zero descriptor")
	}
}

func TestTypeDescriptorRefusals(t *testing.T) {
	for name, in := range map[string]string{
		"unknown member":              `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"extra":1}`,
		"missing name":                `{"id":"https://work.example/types/x","describes":"bead","conformsTo":[]}`,
		"missing conformsTo":          `{"id":"https://work.example/types/x","name":"X","describes":"bead"}`,
		"empty name":                  `{"id":"https://work.example/types/x","name":"","describes":"bead","conformsTo":[]}`,
		"bad describes":               `{"id":"https://work.example/types/x","name":"X","describes":"type","conformsTo":[]}`,
		"bad id":                      `{"id":"work.example/types/x","name":"X","describes":"bead","conformsTo":[]}`,
		"self conformance":            `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":["https://work.example/types/x"]}`,
		"duplicate parent":            `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":["https://work.example/types/a","https://work.example/types/a"]}`,
		"noncanonical parent":         `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":["https://Work.example/types/a"]}`,
		"bad propertiesSchema":        `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"propertiesSchema":"schemas/x"}`,
		"bead with source":            `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"source":{"conformsTo":[]}}`,
		"link without target":         `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]}}`,
		"link with ownsOutgoing":      `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[]},"ownsOutgoing":{"https://work.example/types/c":{"max":1}}}`,
		"empty ownsOutgoing":          `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{}}`,
		"ownsOutgoing without max":    `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"label":"c"}}}`,
		"ownsOutgoing max zero":       `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":0}}}`,
		"ownsOutgoing empty label":    `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"label":"","max":1}}}`,
		"ownsOutgoing unknown member": `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":1,"x":1}}}`,
		"ownsOutgoing bad key":        `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"c":{"max":1}}}`,
		"source unknown member":       `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[],"x":1},"target":{"conformsTo":[]}}`,
		"source without conformsTo":   `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{},"target":{"conformsTo":[]}}`,
		"target empty external":       `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[],"external":""}}`,
		"target bad external":         `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[],"external":"maybe"}}`,
		"target bad parent":           `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":["x"]}}`,
		"not an object":               `[]`,
		"a string":                    `"x"`,
		"malformed":                   `{"id":`,
		"duplicate key":               `{"id":"https://work.example/types/x","id":"https://work.example/types/y","name":"X","describes":"bead","conformsTo":[]}`,
		"wrong member type":           `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":"https://work.example/types/a"}`,
		// Member names are exact and case-sensitive: encoding/json's
		// case-insensitive matching is not a closed shape.
		"case-variant id":              `{"ID":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[]}`,
		"case-variant duplicate":       `{"id":"https://work.example/types/x","Id":"https://work.example/types/y","name":"X","describes":"bead","conformsTo":[]}`,
		"case-variant conformsTo":      `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsto":[]}`,
		"case-variant endpoint member": `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"ConformsTo":[]},"target":{"conformsTo":[]}}`,
		"case-variant owned member":    `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"Max":1}}}`,
		// Absent and null are different things; the bundle admits only absence.
		"id null":                     `{"id":null,"name":"X","describes":"bead","conformsTo":[]}`,
		"name null":                   `{"id":"https://work.example/types/x","name":null,"describes":"bead","conformsTo":[]}`,
		"describes null":              `{"id":"https://work.example/types/x","name":"X","describes":null,"conformsTo":[]}`,
		"conformsTo null":             `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":null}`,
		"conformsTo null element":     `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[null]}`,
		"conformsTo number element":   `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[1]}`,
		"description null":            `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"description":null}`,
		"propertiesSchema null":       `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"propertiesSchema":null}`,
		"propertiesSchema empty":      `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"propertiesSchema":""}`,
		"source null on bead":         `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"source":null}`,
		"target null on bead":         `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"target":null}`,
		"target null on link":         `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":null}`,
		"source not an object":        `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":[],"target":{"conformsTo":[]}}`,
		"source conformsTo null":      `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":null},"target":{"conformsTo":[]}}`,
		"source conformsTo string":    `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":"x"},"target":{"conformsTo":[]}}`,
		"source conformsTo null elem": `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[null]},"target":{"conformsTo":[]}}`,
		"target external null":        `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[],"external":null}}`,
		"target external number":      `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[],"external":1}}`,
		"ownsOutgoing null":           `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":null}`,
		"ownsOutgoing array":          `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":[]}`,
		"ownsOutgoing entry null":     `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":null}}`,
		"ownsOutgoing entry number":   `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":1}}`,
		"ownsOutgoing max null":       `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":null}}}`,
		"ownsOutgoing max string":     `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":"1"}}}`,
		"ownsOutgoing max fractional": `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":1.5}}}`,
		"ownsOutgoing max huge":       `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":1e21}}}`,
		"ownsOutgoing max overflow":   `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":99999999999999999999}}}`,
		"ownsOutgoing max negative":   `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":-1}}}`,
		"ownsOutgoing label null":     `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"label":null,"max":1}}}`,
		"ownsOutgoing label number":   `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"label":1,"max":1}}}`,
		"name number":                 `{"id":"https://work.example/types/x","name":1,"describes":"bead","conformsTo":[]}`,
		"describes number":            `{"id":"https://work.example/types/x","name":"X","describes":1,"conformsTo":[]}`,
		"conformsTo object":           `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":{}}`,
		"missing id":                  `{"name":"X","describes":"bead","conformsTo":[]}`,
		"missing describes":           `{"id":"https://work.example/types/x","name":"X","conformsTo":[]}`,
	} {
		if _, err := graphops.ParseTypeDescriptor([]byte(in)); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("descriptor %s: want ErrValidation, got %v", name, err)
		}
	}
	// Schema-valid integer spellings decode exactly; description "" is absent.
	spelled, err := graphops.ParseTypeDescriptor([]byte(`{"id":"https://work.example/types/x","name":"X","description":"","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/c":{"max":1.0},"https://work.example/types/d":{"max":2e0},"https://work.example/types/e":{"max":300e-2}}}`))
	if err != nil {
		t.Fatalf("integral spellings must decode: %v", err)
	}
	if owns := spelled.OwnsOutgoing(); len(owns) != 3 || owns[0].Max() != 1 || owns[1].Max() != 2 || owns[2].Max() != 3 {
		t.Fatalf("decoded max values: %+v", owns)
	}
	if _, has := spelled.Description(); has {
		t.Fatal(`description "" reads as absent`)
	}
	// Constructor-level refusals not reachable through JSON.
	c, _ := graphops.NewEndpointConstraint(nil, "")
	if _, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Describes: graphops.KindLink, Source: &c, Target: &c, OwnsOutgoing: []graphops.OwnedLinkDecl{{}}}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("link with ownsOutgoing: %v", err)
	}
	if _, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{{}}}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("zero declaration: %v", err)
	}
	d, _ := graphops.NewOwnedLinkDecl("https://work.example/types/c", "", 1)
	if _, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{d, d}}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("duplicate declaration: %v", err)
	}
	if _, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Describes: graphops.KindBead, Source: &c}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("bead with endpoint: %v", err)
	}
	if _, err := graphops.NewOwnedLinkDecl("https://work.example/types/c", "", 0); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("max zero: %v", err)
	}
	if _, err := graphops.NewOwnedLinkDecl("c", "", 1); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("bad url: %v", err)
	}
	if _, err := graphops.NewEndpointConstraint([]string{"https://work.example/types/a", "https://work.example/types/a"}, ""); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("duplicate conformsTo: %v", err)
	}
	if _, err := graphops.NewEndpointConstraint(nil, "bogus"); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("bad external: %v", err)
	}
	e, err := graphops.NewEndpointConstraint([]string{"https://work.example/types/a"}, graphops.ExternalBead)
	if err != nil || e.EffectiveExternal() != graphops.ExternalBead {
		t.Fatalf("endpoint constraint: %v", err)
	}
	e.ConformsTo()[0] = "mutated"
	if e.ConformsTo()[0] != "https://work.example/types/a" {
		t.Fatal("ConformsTo must not alias the value")
	}
	for _, p := range []graphops.ExternalPolicy{graphops.ExternalNone, graphops.ExternalOpaque, graphops.ExternalBead} {
		if !p.Valid() {
			t.Errorf("%s should be valid", p)
		}
	}
	if graphops.ExternalPolicy("").Valid() {
		t.Error("empty policy should not be valid")
	}
}

// descriptorShape is the bundle's typeDescriptor definition in miniature —
// closed, every member typed, the conditionals on describes — decoded from
// canonical bytes with encoding/json's strict mode plus a null scan. It is
// what stands in for a JSON Schema validator here (graphops imports nothing
// that could run the bundle): the shape the constructor promises to emit.
func assertClosedDescriptorShape(t *testing.T, canonical []byte) {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &members); err != nil {
		t.Fatalf("canonical bytes are not an object: %v", err)
	}
	var scanNull func(prefix string, raw json.RawMessage)
	scanNull = func(prefix string, raw json.RawMessage) {
		if string(raw) == "null" {
			t.Errorf("%s: the canonical form must never carry null", prefix)
		}
		var nested map[string]json.RawMessage
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &nested) == nil {
			for k, v := range nested {
				scanNull(prefix+"."+k, v)
			}
		}
	}
	for k, v := range members {
		scanNull(k, v)
	}
	type endpoint struct {
		ConformsTo *[]string `json:"conformsTo"`
		External   *string   `json:"external"`
	}
	var shape struct {
		ID               *string   `json:"id"`
		Name             *string   `json:"name"`
		Description      *string   `json:"description"`
		Describes        *string   `json:"describes"`
		ConformsTo       *[]string `json:"conformsTo"`
		PropertiesSchema *string   `json:"propertiesSchema"`
		Source           *endpoint `json:"source"`
		Target           *endpoint `json:"target"`
		OwnsOutgoing     *map[string]struct {
			Label *string `json:"label"`
			Max   *int    `json:"max"`
		} `json:"ownsOutgoing"`
	}
	dec := json.NewDecoder(strings.NewReader(string(canonical)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&shape); err != nil {
		t.Fatalf("canonical bytes do not fit the closed shape: %v", err)
	}
	if shape.ID == nil || shape.Name == nil || *shape.Name == "" || shape.Describes == nil || shape.ConformsTo == nil {
		t.Fatalf("a required member is missing or empty in %s", canonical)
	}
	if shape.PropertiesSchema != nil && *shape.PropertiesSchema == "" {
		t.Errorf("propertiesSchema must not be empty when present")
	}
	if shape.Description != nil && *shape.Description == "" {
		t.Errorf("description must be omitted rather than empty")
	}
	checkEndpoint := func(name string, e *endpoint) {
		if e == nil || e.ConformsTo == nil {
			t.Errorf("%s: a Link Type's endpoint constraint must be present with an array conformsTo", name)
			return
		}
		if e.External != nil && !graphops.ExternalPolicy(*e.External).Valid() {
			t.Errorf("%s: external %q is not a policy", name, *e.External)
		}
	}
	switch *shape.Describes {
	case "link":
		checkEndpoint("source", shape.Source)
		checkEndpoint("target", shape.Target)
		if shape.OwnsOutgoing != nil {
			t.Errorf("a Link Type must not carry ownsOutgoing")
		}
	case "bead":
		if shape.Source != nil || shape.Target != nil {
			t.Errorf("a Bead Type must not carry endpoint constraints")
		}
		if shape.OwnsOutgoing != nil {
			if len(*shape.OwnsOutgoing) == 0 {
				t.Errorf("ownsOutgoing must be omitted rather than empty")
			}
			for key, decl := range *shape.OwnsOutgoing {
				// The Read foundation admits explicit Type URLs and the
				// max-only wildcard "*", which carries no label
				// (graphops.WildcardOwnedLinkKey).
				if key == graphops.WildcardOwnedLinkKey {
					if decl.Label != nil {
						t.Errorf("ownsOutgoing %s: the wildcard carries no label", key)
					}
				} else if err := graphops.ValidateTypeURL(key); err != nil {
					t.Errorf("ownsOutgoing key %q is neither a Type URL nor the wildcard: %v", key, err)
				}
				if decl.Max == nil || *decl.Max < 1 || (decl.Label != nil && *decl.Label == "") {
					t.Errorf("ownsOutgoing %s: max must be a positive integer and label nonempty when present", key)
				}
			}
		}
	default:
		t.Errorf("describes %q is not bead or link", *shape.Describes)
	}
}

// Constructor → canonical bytes → closed shape → parser: every descriptor
// the constructor admits serializes to a document the bundle's shape accepts
// and the parser reads back to the same fingerprint — the zero
// EndpointConstraint included, which used to serialize conformsTo as null.
func TestTypeDescriptorRoundTripsThroughItsCanonicalForm(t *testing.T) {
	const a, b, c = "https://work.example/types/a", "https://work.example/types/b", "https://work.example/types/c"
	anyBead, _ := graphops.NewEndpointConstraint(nil, "")
	none, _ := graphops.NewEndpointConstraint([]string{b, a}, graphops.ExternalNone)
	beadShaped, _ := graphops.NewEndpointConstraint([]string{a}, graphops.ExternalBead)
	cites8, _ := graphops.NewOwnedLinkDecl(c, "cites — α", 8)
	blocks2, _ := graphops.NewOwnedLinkDecl(b, "", 2)
	wild5, _ := graphops.NewWildcardOwnedLinkDecl(5)
	for name, spec := range map[string]graphops.TypeDescriptorSpec{
		"link with zero endpoints":       {ID: "https://work.example/types/l0", Name: "L0", Describes: graphops.KindLink, Source: &graphops.EndpointConstraint{}, Target: &graphops.EndpointConstraint{}},
		"link with constructed anything": {ID: "https://work.example/types/l1", Name: "L1", Describes: graphops.KindLink, Source: &anyBead, Target: &anyBead},
		"link with policies":             {ID: "https://work.example/types/l2", Name: "L2", Description: "d", Describes: graphops.KindLink, ConformsTo: []string{b, a}, PropertiesSchema: "https://work.example/schemas/l2", Source: &none, Target: &beadShaped},
		"bead minimal":                   {ID: "https://work.example/types/b0", Name: "B0", Describes: graphops.KindBead},
		"bead owning":                    {ID: "https://work.example/types/b1", Name: "B1 ✓", Description: "owns", Describes: graphops.KindBead, ConformsTo: []string{c, a, b}, OwnsOutgoing: []graphops.OwnedLinkDecl{cites8, blocks2}},
		"bead owning by wildcard":        {ID: "https://work.example/types/b2", Name: "B2", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{blocks2, wild5}},
	} {
		built, err := graphops.NewTypeDescriptor(spec)
		if err != nil {
			t.Errorf("%s: constructor refused: %v", name, err)
			continue
		}
		canonical := built.CanonicalJSON()
		assertClosedDescriptorShape(t, canonical)
		parsed, err := graphops.ParseTypeDescriptor(canonical)
		if err != nil {
			t.Errorf("%s: the parser refused the constructor's own canonical form %s: %v", name, canonical, err)
			continue
		}
		if parsed.Fingerprint() != built.Fingerprint() || string(parsed.CanonicalJSON()) != string(canonical) {
			t.Errorf("%s: round trip changed the descriptor\n built %s\nparsed %s", name, canonical, parsed.CanonicalJSON())
		}
		if strings.Contains(string(canonical), "null") {
			t.Errorf("%s: canonical form carries null: %s", name, canonical)
		}
	}
	// The zero constraint and the constructed empty constraint are one value.
	zero, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/l", Name: "L", Describes: graphops.KindLink, Source: &graphops.EndpointConstraint{}, Target: &graphops.EndpointConstraint{}})
	built, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/l", Name: "L", Describes: graphops.KindLink, Source: &anyBead, Target: &anyBead})
	if zero.Fingerprint() != built.Fingerprint() || !strings.Contains(string(zero.CanonicalJSON()), `"source":{"conformsTo":[]}`) {
		t.Fatalf("zero EndpointConstraint must canonicalize as the empty set: %s", zero.CanonicalJSON())
	}
	src, _ := zero.Source()
	if got := src.ConformsTo(); len(got) != 0 {
		t.Fatalf("zero constraint reads back as the empty set: %#v", got)
	}
}

func TestScopeIdentity(t *testing.T) {
	minted := time.Date(2026, 9, 7, 10, 0, 0, 0, time.FixedZone("x", 3600))
	claim := graphops.WitnessClaim{Held: true, Epoch: 3, LedgerSeq: 9, LedgerHash: strings.Repeat("a", 64), Unverified: true, Pending: "promote"}
	id, err := graphops.NewScopeIdentity(graphops.ScopeIdentitySpec{ScopeURL: scopeURL, AuthorityID: authorityID, Epoch: 3, MintedAt: minted, Claim: claim})
	if err != nil {
		t.Fatal(err)
	}
	if id.ScopeURL() != scopeURL || id.AuthorityID() != authorityID || id.Epoch() != 3 || id.MintedAt() != minted.UTC() ||
		id.MintedAt().Location() != time.UTC || id.Claim() != claim || id.IsZero() {
		t.Fatalf("identity accessors: %+v", id)
	}
	if !(graphops.ScopeIdentity{}).IsZero() {
		t.Fatal("zero identity")
	}
	for name, spec := range map[string]graphops.ScopeIdentitySpec{
		"bad url":         {ScopeURL: "https://beads.example/acme", AuthorityID: authorityID, MintedAt: minted},
		"local-test url":  {ScopeURL: "https://beads.example/local-test/", AuthorityID: authorityID, MintedAt: minted},
		"bad authority":   {ScopeURL: scopeURL, AuthorityID: "abc", MintedAt: minted},
		"zero minted at":  {ScopeURL: scopeURL, AuthorityID: authorityID},
		"bad ledger hash": {ScopeURL: scopeURL, AuthorityID: authorityID, MintedAt: minted, Claim: graphops.WitnessClaim{LedgerHash: "abc"}},
	} {
		if _, err := graphops.NewScopeIdentity(spec); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("identity %s: want ErrValidation, got %v", name, err)
		}
	}
	for _, d := range []graphops.LedgerDurability{graphops.LedgerInState, graphops.LedgerIndependent, graphops.LedgerNone} {
		if !d.Valid() {
			t.Errorf("%s should be valid", d)
		}
	}
	if graphops.LedgerDurability("maybe").Valid() {
		t.Error("unknown durability should not be valid")
	}
}

func TestCheckBeadRecord(t *testing.T) {
	rev := graphops.MintRevision()
	bead, _ := graphops.NewBead(graphops.BeadSpec{Path: "beads/d", TypeURL: "https://work.example/types/decision", Revision: rev})
	self, _ := graphops.NewInScopeRef("beads/d", "")
	other, _ := graphops.NewInScopeRef("beads/e", "")
	mk := func(path, typeURL string, source graphops.Ref) graphops.Link {
		l, err := graphops.NewLink(graphops.LinkSpec{Path: path, TypeURL: typeURL, Revision: rev, Source: source, Target: other})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	const cites, blocks = "https://work.example/types/cites", "https://work.example/types/blocks"
	citesDecl, _ := graphops.NewOwnedLinkDecl(cites, "", 8)
	blocksDecl, _ := graphops.NewOwnedLinkDecl(blocks, "", 2)
	owns := []graphops.OwnedLinkDecl{citesDecl, blocksDecl} // unsorted on purpose
	good := graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{
		{TypeURL: blocks}, // an owned Type with no Links is an EMPTY group
		{TypeURL: cites, Links: []graphops.Link{mk("links/c/1", cites, self), mk("links/c/2", cites, self)}},
	}}
	if err := graphops.CheckBeadRecord(good, owns); err != nil {
		t.Fatalf("complete record refused: %v", err)
	}
	if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead}, nil); err != nil {
		t.Fatalf("a Type owning nothing has no groups: %v", err)
	}
	// Each explicit group is within its declaration's max: at the max is
	// admitted, one over is refused (OW1 = A made Max an acceptance fact).
	atMax := graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{
		{TypeURL: blocks, Links: []graphops.Link{mk("links/b/1", blocks, self), mk("links/b/2", blocks, self)}},
		good.OwnedLinks[1],
	}}
	if err := graphops.CheckBeadRecord(atMax, owns); err != nil {
		t.Fatalf("explicit groups at their max refused: %v", err)
	}
	for name, rec := range map[string]graphops.BeadRecord{
		"over max":        {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: blocks, Links: []graphops.Link{mk("links/b/1", blocks, self), mk("links/b/2", blocks, self), mk("links/b/3", blocks, self)}}, good.OwnedLinks[1]}},
		"missing group":   {Bead: bead, OwnedLinks: good.OwnedLinks[1:]},
		"wrong order":     {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{good.OwnedLinks[1], good.OwnedLinks[0]}},
		"wrong type":      {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: blocks}, {TypeURL: cites, Links: []graphops.Link{mk("links/c/1", blocks, self)}}}},
		"wrong source":    {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: blocks}, {TypeURL: cites, Links: []graphops.Link{mk("links/c/1", cites, other)}}}},
		"unsorted links":  {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: blocks}, {TypeURL: cites, Links: []graphops.Link{mk("links/c/2", cites, self), mk("links/c/1", cites, self)}}}},
		"duplicate links": {Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: blocks}, {TypeURL: cites, Links: []graphops.Link{mk("links/c/1", cites, self), mk("links/c/1", cites, self)}}}},
	} {
		if err := graphops.CheckBeadRecord(rec, owns); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("record %s: want ErrValidation, got %v", name, err)
		}
	}
}

func TestClosedSetsAndDirection(t *testing.T) {
	if !graphops.KindBead.Valid() || !graphops.KindLink.Valid() || graphops.ResourceKind("type").Valid() {
		t.Fatal("ResourceKind closed set")
	}
	for d, want := range map[graphops.Direction]string{graphops.DirectionBoth: "both", graphops.DirectionIn: "in", graphops.DirectionOut: "out"} {
		if !d.Valid() || d.String() != want {
			t.Errorf("direction %d: %s", d, d)
		}
	}
	if graphops.Direction(9).Valid() || graphops.Direction(9).String() != "Direction(9)" {
		t.Fatal("unknown direction")
	}
	var zero graphops.IncidentRequest
	if zero.Direction != graphops.DirectionBoth {
		t.Fatal("a zero IncidentRequest asks the protocol's default question")
	}
	if !lowerHex32.MatchString(graphops.MintOpaqueToken()) {
		t.Fatal("opaque tokens are 32 lowercase hex digits")
	}
}

func TestErrorsAreOneVocabulary(t *testing.T) {
	// Aliases: the same value under two names, so one errors.Is arm matches.
	for name, pair := range map[string][2]error{
		"ErrValidation":             {graphops.ErrValidation, beadserrors.ErrValidation},
		"ErrNotFound":               {graphops.ErrNotFound, beadserrors.ErrNotFound},
		"ErrNotAuthority":           {graphops.ErrNotAuthority, beadserrors.ErrNotAuthority},
		"ErrStateRewound":           {graphops.ErrStateRewound, beadserrors.ErrStateRewound},
		"ErrStateChanged":           {graphops.ErrStateChanged, beadserrors.ErrStateChanged},
		"ErrSyncRequired":           {graphops.ErrSyncRequired, beadserrors.ErrSyncRequired},
		"ErrUnpublished":            {graphops.ErrUnpublished, beadserrors.ErrUnpublished},
		"ErrRepresentationTooLarge": {graphops.ErrRepresentationTooLarge, beadserrors.ErrRepresentationTooLarge},
		"ErrNotServedYet":           {graphops.ErrNotServedYet, beadserrors.ErrNotServedYet},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: graphops and beadserrors values differ", name)
		}
	}
	var unsupported error = &graphops.ErrUnsupported{Op: "BeadGraphReader", Backend: "custom"}
	var target *beadserrors.ErrUnsupported
	if !errors.As(unsupported, &target) || target.Op != "BeadGraphReader" {
		t.Fatal("ErrUnsupported is the beadserrors type")
	}
	// The domain-naming refusals are distinct sentinels.
	all := []error{graphops.ErrNoScope, graphops.ErrScopeExists, graphops.ErrURLReused, graphops.ErrNotAuthority, graphops.ErrNotFound, graphops.ErrValidation}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
	// GoneError is a refinement of not-found.
	var gone error = &graphops.GoneError{Path: "beads/x", State: graphops.AllocationPruned}
	if !errors.Is(gone, graphops.ErrNotFound) || errors.Is(gone, graphops.ErrValidation) {
		t.Fatal("a gone path is not found to a caller that does not ask")
	}
	var typed *graphops.GoneError
	if !errors.As(gone, &typed) || typed.Path != "beads/x" || typed.State != graphops.AllocationPruned {
		t.Fatal("a handler that asks gets the state")
	}
	if gone.Error() != "beads/x is gone (pruned)" {
		t.Fatalf("GoneError message: %q", gone.Error())
	}
	wrapped := errors.Join(errors.New("context"), gone)
	if !errors.Is(wrapped, graphops.ErrNotFound) {
		t.Fatal("wrapping keeps the relation")
	}
}

// The wildcard owned-Link declaration (bdp#1 item 5, ruled 2026-09-08):
// "*": { max } owns every outgoing Link Type not named explicitly, max bounds
// the Bead's WHOLE owned set — explicit Types' Links included, so no explicit
// max may exceed it (OW1 = A; TestWildcardMaxBoundsTheWholeOwnedSet) — and
// explicit entries take precedence for the Types they name. The Read foundation
// now carries this declaration on the wire. The independent canonical vector
// remains byte-exact, with "*" sorted first: 0x2A precedes every URL's "h".
func TestWildcardOwnedLinkDeclaration(t *testing.T) {
	const memory, cites, relates = "https://work.example/types/memory", "https://work.example/types/cites", "https://work.example/types/relates"
	any8, err := graphops.NewWildcardOwnedLinkDecl(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, hasLabel := any8.Label(); !any8.Wildcard() || any8.TypeURL() != graphops.WildcardOwnedLinkKey || any8.Max() != 8 || hasLabel {
		t.Fatalf("wildcard declaration: %+v", any8)
	}
	if (graphops.OwnedLinkDecl{}).Wildcard() {
		t.Fatal("the zero declaration is not the wildcard")
	}
	// The explicit max sits under the wildcard's: the wildcard's 8 bounds the
	// whole owned set, cites' 5 the cites Links inside it (OW1 = A).
	cites5, _ := graphops.NewOwnedLinkDecl(cites, "cites", 5)
	built, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: memory, Name: "Memory", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{cites5, any8}})
	if err != nil {
		t.Fatal(err)
	}
	const wantCanonical = `{"conformsTo":[],"describes":"bead","id":"https://work.example/types/memory","name":"Memory","ownsOutgoing":{"*":{"max":8},"https://work.example/types/cites":{"label":"cites","max":5}}}`
	const wantFingerprint = "9aba92834b71bd4153890415c243b3961058318602c478966ac5cde403862e26"
	if got := string(built.CanonicalJSON()); got != wantCanonical {
		t.Fatalf("canonical wildcard descriptor\n got %s\nwant %s", got, wantCanonical)
	}
	if built.Fingerprint() != wantFingerprint || built.Fingerprint() != sha256Hex([]byte(wantCanonical)) {
		t.Fatalf("fingerprint %s, want the golden %s", built.Fingerprint(), wantFingerprint)
	}
	assertClosedDescriptorShape(t, built.CanonicalJSON())
	if owns := built.OwnsOutgoing(); len(owns) != 2 || !owns[0].Wildcard() || owns[1].TypeURL() != cites {
		t.Fatalf("the wildcard sorts first, then Type URLs: %+v", owns)
	}
	// Explicit precedence, wildcard fallback, and "*" is never a Type.
	if got, ok := built.Owns(cites); !ok || got.Wildcard() || got.Max() != 5 {
		t.Fatalf("Owns(cites) must be the explicit declaration: %+v %v", got, ok)
	}
	if got, ok := built.Owns(relates); !ok || !got.Wildcard() || got.Max() != 8 {
		t.Fatalf("Owns(relates) must fall back to the wildcard: %+v %v", got, ok)
	}
	if _, ok := built.Owns(graphops.WildcardOwnedLinkKey); ok {
		t.Fatal(`Owns("*") must be false: the wildcard key is not a Type`)
	}
	// The wire form, in either key order, parses to the same fingerprint.
	parsed, err := graphops.ParseTypeDescriptor([]byte(`{"id":"https://work.example/types/memory","name":"Memory","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/cites":{"max":5,"label":"cites"},"*":{"max":8}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fingerprint() != wantFingerprint || string(parsed.CanonicalJSON()) != wantCanonical {
		t.Fatalf("parsed wildcard descriptor: %s", parsed.CanonicalJSON())
	}
	// A wildcard alone owns everything, at its bound.
	alone, err := graphops.ParseTypeDescriptor([]byte(`{"id":"https://work.example/types/memory","name":"Memory","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	const wantAlone = `{"conformsTo":[],"describes":"bead","id":"https://work.example/types/memory","name":"Memory","ownsOutgoing":{"*":{"max":1}}}`
	if got := string(alone.CanonicalJSON()); got != wantAlone || alone.Fingerprint() != "44ac14cf761e1c41d7c7a2f0c927c0456ae37b0a81c3f777d8f9fe059ddc4321" {
		t.Fatalf("canonical wildcard-only descriptor: %s %s", got, alone.Fingerprint())
	}
	if got, ok := alone.Owns(cites); !ok || !got.Wildcard() || got.Max() != 1 || len(alone.OwnsOutgoing()) != 1 {
		t.Fatalf("a lone wildcard owns every Type: %+v %v", got, ok)
	}
	// Without a wildcard an unnamed Type is still unowned: the pre-ruling law.
	plain, _ := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: memory, Name: "Memory", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{cites5}})
	if _, ok := plain.Owns(relates); ok {
		t.Fatal("no wildcard: an unnamed Type is not owned")
	}
	// Refusals through the parser: max is required on the wildcard exactly
	// as on an explicit entry, a label on it is refused, and "*" is not a
	// Type anywhere a Type URL is read.
	for name, in := range map[string]string{
		"wildcard without max":         `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{}}}`,
		"wildcard max zero":            `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":0}}}`,
		"wildcard max negative":        `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":-1}}}`,
		"wildcard max fractional":      `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":1.5}}}`,
		"wildcard with label":          `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"label":"any","max":1}}}`,
		"wildcard with empty label":    `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"label":"","max":1}}}`,
		"wildcard unknown member":      `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":1,"min":0}}}`,
		"wildcard entry null":          `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":null}}`,
		"wildcard on a Link Type":      `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":[]},"ownsOutgoing":{"*":{"max":1}}}`,
		"wildcard as id":               `{"id":"*","name":"X","describes":"bead","conformsTo":[]}`,
		"wildcard as parent":           `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":["*"]}`,
		"wildcard as propertiesSchema": `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"propertiesSchema":"*"}`,
		"wildcard as source parent":    `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":["*"]},"target":{"conformsTo":[]}}`,
		"wildcard as target parent":    `{"id":"https://work.example/types/x","name":"X","describes":"link","conformsTo":[],"source":{"conformsTo":[]},"target":{"conformsTo":["*"]}}`,
	} {
		if _, err := graphops.ParseTypeDescriptor([]byte(in)); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("descriptor %s: want ErrValidation, got %v", name, err)
		}
	}
	// Refusals at the constructors: the key is never a Type anywhere.
	if _, err := graphops.NewWildcardOwnedLinkDecl(0); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("wildcard max zero: %v", err)
	}
	if _, err := graphops.NewOwnedLinkDecl(graphops.WildcardOwnedLinkKey, "", 1); !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), "wildcard") {
		t.Errorf(`NewOwnedLinkDecl("*") must refuse and name the wildcard: %v`, err)
	}
	if _, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: memory, Name: "Memory", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{any8, cites5, any8}}); !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), "twice") {
		t.Errorf("two wildcards: %v", err)
	}
	if _, err := graphops.NewEndpointConstraint([]string{graphops.WildcardOwnedLinkKey}, ""); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("wildcard in an endpoint constraint: %v", err)
	}
	rev := graphops.MintRevision()
	if _, err := graphops.NewBead(graphops.BeadSpec{Path: "beads/m", TypeURL: graphops.WildcardOwnedLinkKey, Revision: rev}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("wildcard as a Bead's type: %v", err)
	}
	self, _ := graphops.NewInScopeRef("beads/m", "")
	if _, err := graphops.NewLink(graphops.LinkSpec{Path: "links/m/1", TypeURL: graphops.WildcardOwnedLinkKey, Revision: rev, Source: self, Target: self}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("wildcard as a Link's type: %v", err)
	}
	if err := graphops.ValidateTypeURL(graphops.WildcardOwnedLinkKey); !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), "wildcard owned-Link key") {
		t.Errorf(`ValidateTypeURL("*") must refuse by name: %v`, err)
	}
}

// The wildcard's max bounds the Bead's WHOLE owned set (OW1 = A, operator
// ruling 2026-09-08, gastownhall/bdp#22): every owned Link across every owned
// Type, the explicitly declared Types' Links included. So an explicit
// declaration's max may not exceed the wildcard's — refused at both doors,
// naming the Type and both numbers — while equal and lower are admitted, and
// a descriptor without a wildcard is bounded per Type only, as before.
func TestWildcardMaxBoundsTheWholeOwnedSet(t *testing.T) {
	const x, blocks, cites, relates = "https://work.example/types/x", "https://work.example/types/blocks", "https://work.example/types/cites", "https://work.example/types/relates"
	spec := func(owns ...graphops.OwnedLinkDecl) graphops.TypeDescriptorSpec {
		return graphops.TypeDescriptorSpec{ID: x, Name: "X", Describes: graphops.KindBead, OwnsOutgoing: owns}
	}
	any5, _ := graphops.NewWildcardOwnedLinkDecl(5)
	blocks2, _ := graphops.NewOwnedLinkDecl(blocks, "", 2)
	cites5, _ := graphops.NewOwnedLinkDecl(cites, "", 5)
	cites8, _ := graphops.NewOwnedLinkDecl(cites, "cites", 8)
	const wantRefusal = "ownsOutgoing https://work.example/types/cites: max 8 exceeds the wildcard's max 5"
	// Refused at the constructor, whatever the authored order and however
	// many compliant entries precede the offending one.
	for name, owns := range map[string][]graphops.OwnedLinkDecl{
		"explicit above the wildcard":                {cites8, any5},
		"wildcard first":                             {any5, cites8},
		"a compliant entry before the offending one": {any5, blocks2, cites8},
	} {
		_, err := graphops.NewTypeDescriptor(spec(owns...))
		if !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), wantRefusal) {
			t.Errorf("%s: want ErrValidation naming the Type and both numbers, got %v", name, err)
		}
	}
	// Refused at the JSON door with the same words, in either key order.
	for name, in := range map[string]string{
		"explicit above the wildcard": `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/cites":{"label":"cites","max":8},"*":{"max":5}}}`,
		"wildcard first":              `{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"*":{"max":5},"https://work.example/types/blocks":{"max":2},"https://work.example/types/cites":{"max":8}}}`,
	} {
		_, err := graphops.ParseTypeDescriptor([]byte(in))
		if !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), wantRefusal) {
			t.Errorf("descriptor %s: want ErrValidation naming the Type and both numbers, got %v", name, err)
		}
	}
	// Equal is admitted — the explicit Type may then fill the whole set by
	// itself — and so is lower; the wildcard's max is the whole-set bound
	// either way, and the canonical form re-admits, fingerprint intact.
	for name, owns := range map[string][]graphops.OwnedLinkDecl{
		"equal": {cites5, any5},
		"lower": {blocks2, any5},
		"both":  {cites5, blocks2, any5},
	} {
		built, err := graphops.NewTypeDescriptor(spec(owns...))
		if err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		if got, ok := built.Owns(relates); !ok || !got.Wildcard() || got.Max() != 5 {
			t.Errorf("%s: the wildcard's max is the whole-set bound: %+v %v", name, got, ok)
		}
		parsed, err := graphops.ParseTypeDescriptor(built.CanonicalJSON())
		if err != nil || parsed.Fingerprint() != built.Fingerprint() {
			t.Errorf("%s: the canonical form must re-admit with the same fingerprint: %v", name, err)
		}
	}
	// Without a wildcard there is no whole-set bound to exceed: explicit
	// maxes relate to nothing but their own Type.
	if _, err := graphops.NewTypeDescriptor(spec(cites8, blocks2)); err != nil {
		t.Errorf("no wildcard: explicit maxes are unrelated: %v", err)
	}
	if _, err := graphops.ParseTypeDescriptor([]byte(`{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"https://work.example/types/blocks":{"max":2},"https://work.example/types/cites":{"max":8}}}`)); err != nil {
		t.Errorf("no wildcard, parsed: explicit maxes are unrelated: %v", err)
	}
}

// CheckBeadRecord under a wildcard declaration (bdp#1 item 5): a group per
// wildcard-owned Type actually present, an empty group only for an explicit
// declaration, never a group keyed "*", and — OW1 = A — the whole owned set,
// every group together, within the wildcard's max, the explicit group also
// within its own.
func TestCheckBeadRecordUnderAWildcard(t *testing.T) {
	rev := graphops.MintRevision()
	bead, _ := graphops.NewBead(graphops.BeadSpec{Path: "beads/m", TypeURL: "https://work.example/types/memory", Revision: rev})
	self, _ := graphops.NewInScopeRef("beads/m", "")
	other, _ := graphops.NewInScopeRef("beads/e", "")
	mk := func(path, typeURL string, source graphops.Ref) graphops.Link {
		l, err := graphops.NewLink(graphops.LinkSpec{Path: path, TypeURL: typeURL, Revision: rev, Source: source, Target: other})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	const blocks, cites, relates = "https://work.example/types/blocks", "https://work.example/types/cites", "https://work.example/types/relates"
	// The wildcard's 5 bounds the whole owned set; cites' 3 bounds the cites
	// Links inside it (a descriptor that exists never has it the other way).
	citesDecl, _ := graphops.NewOwnedLinkDecl(cites, "", 3)
	wildcard, _ := graphops.NewWildcardOwnedLinkDecl(5)
	owns := []graphops.OwnedLinkDecl{wildcard, citesDecl} // unsorted on purpose
	many := func(prefix, typeURL string, n int) []graphops.Link {
		links := make([]graphops.Link, 0, n)
		for i := 1; i <= n; i++ {
			links = append(links, mk(fmt.Sprintf("%s/%d", prefix, i), typeURL, self))
		}
		return links
	}
	blocksGroup := graphops.OwnedLinkGroup{TypeURL: blocks, Links: many("links/b", blocks, 1)}
	relatesGroup := graphops.OwnedLinkGroup{TypeURL: relates, Links: many("links/r", relates, 2)}
	for name, groups := range map[string][]graphops.OwnedLinkGroup{
		"present wildcard groups around the empty explicit group": {blocksGroup, {TypeURL: cites}, relatesGroup},
		"the explicit group alone":                                {{TypeURL: cites}},
		"the explicit group with Links":                           {{TypeURL: cites, Links: many("links/c", cites, 1)}},
		"a wildcard group before the explicit group":              {blocksGroup, {TypeURL: cites}},
		"a wildcard group after the explicit group":               {{TypeURL: cites}, relatesGroup},
		"the whole owned set at the wildcard's max":               {blocksGroup, {TypeURL: cites, Links: many("links/c", cites, 2)}, relatesGroup},
		"the explicit group at its own max":                       {{TypeURL: cites, Links: many("links/c", cites, 3)}, relatesGroup},
		"wildcard-owned Links alone at the whole-set max":         {{TypeURL: cites}, {TypeURL: relates, Links: many("links/r", relates, 5)}},
	} {
		if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead, OwnedLinks: groups}, owns); err != nil {
			t.Errorf("record %s refused: %v", name, err)
		}
	}
	for name, groups := range map[string][]graphops.OwnedLinkGroup{
		"a group keyed by the wildcard":                                {{TypeURL: graphops.WildcardOwnedLinkKey}, {TypeURL: cites}},
		"an empty wildcard-owned group":                                {{TypeURL: blocks}, {TypeURL: cites}},
		"no explicit group, a wildcard group before it":                {blocksGroup},
		"no explicit group, a wildcard group after it":                 {relatesGroup},
		"groups out of order":                                          {{TypeURL: cites}, blocksGroup},
		"a repeated group":                                             {{TypeURL: cites}, {TypeURL: cites}},
		"a wildcard group holding another Type's Link":                 {{TypeURL: blocks, Links: []graphops.Link{mk("links/r/1", relates, self)}}, {TypeURL: cites}},
		"a wildcard group holding another source's Link":               {{TypeURL: blocks, Links: []graphops.Link{mk("links/b/1", blocks, other)}}, {TypeURL: cites}},
		"a wildcard group with unsorted Links":                         {{TypeURL: cites}, {TypeURL: relates, Links: []graphops.Link{mk("links/r/2", relates, self), mk("links/r/1", relates, self)}}},
		"the whole owned set over the wildcard's max":                  {blocksGroup, {TypeURL: cites, Links: many("links/c", cites, 3)}, relatesGroup},
		"wildcard-owned Links alone over the whole-set max":            {{TypeURL: cites}, {TypeURL: relates, Links: many("links/r", relates, 6)}},
		"the explicit group over its own max, under the whole-set max": {{TypeURL: cites, Links: many("links/c", cites, 4)}},
	} {
		if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead, OwnedLinks: groups}, owns); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("record %s: want ErrValidation, got %v", name, err)
		}
	}
	// The bound refusals name the set that overflowed, the count and the max.
	over := graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{blocksGroup, {TypeURL: cites, Links: many("links/c", cites, 3)}, relatesGroup}}
	if err := graphops.CheckBeadRecord(over, owns); err == nil || !strings.Contains(err.Error(), "6 owned Links across all groups, over the wildcard's whole-set max 5") {
		t.Errorf("whole-set refusal must name the count and the bound: %v", err)
	}
	overExplicit := graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{{TypeURL: cites, Links: many("links/c", cites, 4)}}}
	if err := graphops.CheckBeadRecord(overExplicit, owns); err == nil || !strings.Contains(err.Error(), "ownedLinks group https://work.example/types/cites holds 4 Links, over its declared max 3") {
		t.Errorf("explicit refusal must name the group, the count and the bound: %v", err)
	}
	// Without a wildcard nothing changed: an unnamed Type's group is refused
	// even when it holds that Type's Links.
	if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{blocksGroup, {TypeURL: cites}}}, []graphops.OwnedLinkDecl{citesDecl}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("unnamed Type without a wildcard: want ErrValidation, got %v", err)
	}
	// A Type owning only through the wildcard, with nothing present, has no
	// groups at all; with Links present, the whole set is bounded exactly as
	// when explicit declarations sit beside the wildcard.
	if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead}, []graphops.OwnedLinkDecl{wildcard}); err != nil {
		t.Errorf("lone wildcard, nothing present: %v", err)
	}
	if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{blocksGroup, {TypeURL: relates, Links: many("links/r", relates, 4)}}}, []graphops.OwnedLinkDecl{wildcard}); err != nil {
		t.Errorf("lone wildcard, whole set at its max: %v", err)
	}
	if err := graphops.CheckBeadRecord(graphops.BeadRecord{Bead: bead, OwnedLinks: []graphops.OwnedLinkGroup{blocksGroup, {TypeURL: relates, Links: many("links/r", relates, 5)}}}, []graphops.OwnedLinkDecl{wildcard}); !errors.Is(err, graphops.ErrValidation) {
		t.Errorf("lone wildcard, whole set over its max: want ErrValidation, got %v", err)
	}
}

// ownsOutgoing max obeys the binary64 admission law at both of its doors
// (bdp#21): the JSON door refuses it inside CanonicalizeJSON, naming the
// member's pointer (the Type URL's slashes escaped per RFC 6901), and the Go
// constructors refuse the same values with the same predicate, so every
// descriptor that exists has a canonical form the JSON door re-admits,
// fingerprint intact.
func TestOwnedLinkDeclMaxObeysTheBinary64Law(t *testing.T) {
	const c = "https://work.example/types/c"
	for _, max := range []int{9007199254740993, 9007199254740995, 1<<60 + 1, 1 << 60, math.MaxInt64} {
		for name, construct := range map[string]func() error{
			"explicit": func() error { _, err := graphops.NewOwnedLinkDecl(c, "", max); return err },
			"wildcard": func() error { _, err := graphops.NewWildcardOwnedLinkDecl(max); return err },
		} {
			err := construct()
			if !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), fmt.Sprintf("max %d does not round-trip through IEEE-754 binary64 (bdp#21)", max)) {
				t.Errorf("%s max %d: want the binary64 refusal, got %v", name, max, err)
			}
		}
		doc := fmt.Sprintf(`{"id":"https://work.example/types/x","name":"X","describes":"bead","conformsTo":[],"ownsOutgoing":{"%s":{"max":%d}}}`, c, max)
		_, err := graphops.ParseTypeDescriptor([]byte(doc))
		if !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), `at JSON pointer "/ownsOutgoing/https:~1~1work.example~1types~1c/max"`) {
			t.Errorf("descriptor max %d: want the binary64 refusal at the member's pointer, got %v", max, err)
		}
	}
	// Both admitted values, the explicit one under the wildcard's (OW1 = A).
	explicit, err := graphops.NewOwnedLinkDecl(c, "", 9007199254740992)
	if err != nil {
		t.Fatal(err)
	}
	wildcard, err := graphops.NewWildcardOwnedLinkDecl(9007199254740994)
	if err != nil {
		t.Fatal(err)
	}
	built, err := graphops.NewTypeDescriptor(graphops.TypeDescriptorSpec{ID: "https://work.example/types/x", Name: "X", Describes: graphops.KindBead, OwnsOutgoing: []graphops.OwnedLinkDecl{explicit, wildcard}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"conformsTo":[],"describes":"bead","id":"https://work.example/types/x","name":"X","ownsOutgoing":{"*":{"max":9007199254740994},"https://work.example/types/c":{"max":9007199254740992}}}`
	if got := string(built.CanonicalJSON()); got != want {
		t.Fatalf("canonical form\n got %s\nwant %s", got, want)
	}
	parsed, err := graphops.ParseTypeDescriptor(built.CanonicalJSON())
	if err != nil || parsed.Fingerprint() != built.Fingerprint() {
		t.Fatalf("the canonical form must re-admit with the same fingerprint: %v", err)
	}
	maxes := map[string]int{}
	for _, d := range parsed.OwnsOutgoing() {
		maxes[d.TypeURL()] = d.Max()
	}
	if len(maxes) != 2 || maxes[c] != 9007199254740992 || maxes[graphops.WildcardOwnedLinkKey] != 9007199254740994 {
		t.Fatalf("max values after the round trip: %v", maxes)
	}
}
