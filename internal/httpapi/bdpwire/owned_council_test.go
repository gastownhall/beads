package bdpwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOwnedLinksKeyGrammarAcrossCarriers(t *testing.T) {
	for _, tc := range []struct {
		key   string
		valid bool
	}{
		{"*", false}, {"types/local", false}, {"", false},
		{"http://", false}, {"HTTP://x", false},
		{"https://\n", false}, {"https://\r", false},
		{"https://\u2028", false}, {"https://\u2029", false},
		{"http://x", true}, {"https://x", true},
		{"https:// ", true}, {"http://\t", true},
		{"https://x\n", true}, {"https://x\r", true},
		{"https://x\u2028", true}, {"https://x\u2029", true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			groups := OwnedLinks{tc.key: {}}
			if err := groups.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate: %v; want acceptance %v", err, tc.valid)
			}
			if _, err := json.Marshal(groups); (err == nil) != tc.valid {
				t.Fatalf("Marshal: %v; want acceptance %v", err, tc.valid)
			}
			keyJSON, err := json.Marshal(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"id":"beads/owner","type":"https://example.org/types/b","revision":"r","properties":{},"ownedLinks":{` + string(keyJSON) + `:[]}}`)
			for _, decoder := range []struct {
				name   string
				decode func([]byte, any) error
			}{{"strict", Unmarshal}, {"encoding-json", json.Unmarshal}} {
				t.Run(decoder.name, func(t *testing.T) {
					var bead BeadRecord
					if err := decoder.decode(raw, &bead); (err == nil) != tc.valid {
						t.Fatalf("decode: %v; want acceptance %v", err, tc.valid)
					}
					if tc.valid && !reflect.DeepEqual(bead.OwnedLinks, groups) {
						t.Fatalf("lost explicit zero group: %#v", bead.OwnedLinks)
					}
				})
			}
		})
	}
}

func TestOwnedLinksEmptyEncodingPreserved(t *testing.T) {
	for _, tc := range []struct {
		value OwnedLinks
		wire  string
	}{
		{nil, `null`}, {OwnedLinks{}, `{}`},
		{OwnedLinks{"https://example.org/types/l": nil}, `{"https://example.org/types/l":[]}`},
		{OwnedLinks{"https://example.org/types/l": {}}, `{"https://example.org/types/l":[]}`},
	} {
		raw, err := json.Marshal(tc.value)
		if err != nil || string(raw) != tc.wire {
			t.Fatalf("Marshal: %s, %v; want %s", raw, err, tc.wire)
		}
		var standard OwnedLinks
		if err := json.Unmarshal(raw, &standard); err != nil {
			t.Fatal(err)
		}
		var strict OwnedLinks
		if err := Unmarshal(raw, &strict); (err == nil) != (tc.wire != `null`) {
			t.Fatalf("strict null/object policy changed: %s: %v", raw, err)
		}
	}
}

func TestOwnedOutgoingNilValidation(t *testing.T) {
	var descriptor TypeDescriptor
	if err := descriptor.OwnsOutgoing.Validate(); err != nil {
		t.Fatal(err)
	}
	empty := &OwnedOutgoingDeclarations{}
	if err := empty.Validate(); err == nil {
		t.Fatal("present empty declaration accepted")
	}
}

func TestOwnedOutgoingErrorPaths(t *testing.T) {
	for _, tc := range []struct{ raw, path string }{
		{`{"*":{"max":1},"*":{"max":2}}`, "TypeDescriptor.ownsOutgoing"},
		{`{"*":{"max":1,"max":2}}`, "TypeDescriptor.ownsOutgoing.*"},
		{`{"https://example.org/types/l":{"max":1,"max":2}}`, "TypeDescriptor.ownsOutgoing.https://example.org/types/l"},
		{`{"*":{"max":"bad"}}`, "TypeDescriptor.ownsOutgoing.*.max"},
		{`{"https://example.org/types/l":{"max":0}}`, "TypeDescriptor.ownsOutgoing.https://example.org/types/l.max"},
	} {
		var descriptor TypeDescriptor
		err := Unmarshal([]byte(`{"id":"https://example.org/types/b","name":"B","describes":"bead","conformsTo":[],"ownsOutgoing":`+tc.raw+`}`), &descriptor)
		if err == nil || !strings.HasPrefix(err.Error(), "bdpwire: "+tc.path+":") {
			t.Fatalf("lost package prefix or nested path %q: %v", tc.path, err)
		}
	}
}

// These are DTO acceptance checks against the vendored probe inputs, not
// observations of HTTP status or the fixture's temporarily-unavailable code.
// The one Link descriptor counterexample exercises a domain conditional that
// this DTO deliberately does not enforce; it is accounted for explicitly.
func TestVendoredWildcardDescriptorProbes(t *testing.T) {
	fixture := loadJSON(t, "fixtures/read-bdpbd-v1.json")
	groups := selectAll(fixture, "/oracles/wildcard-descriptors/*")
	if len(groups) == 0 {
		t.Fatal("no vendored wildcard probe groups")
	}
	domainOnly := 0
	for _, value := range groups {
		group := asMap(t, value, "wildcard probe group")
		descriptors := map[string]any{}
		for _, value := range selectAll(group, "/descriptors/*") {
			doc := asMap(t, value, "descriptor")
			id, ok := doc["id"].(string)
			if !ok || descriptors[id] != nil {
				t.Fatal("missing or duplicate descriptor id")
			}
			descriptors[id] = doc
		}
		rows := selectAll(group, "/rows/*")
		if len(rows) == 0 || len(rows) != len(descriptors) {
			t.Fatal("probe inputs and outcomes do not correspond")
		}
		for _, value := range rows {
			row := asMap(t, value, "outcome row")
			id, ok := row["id"].(string)
			if !ok || descriptors[id] == nil {
				t.Fatal("outcome has no unique descriptor input")
			}
			doc := descriptors[id]
			delete(descriptors, id)
			t.Run(id, func(t *testing.T) {
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				var descriptor TypeDescriptor
				err = Unmarshal(raw, &descriptor)
				if id == "https://work.example/types/wildcard-probe-link" {
					domainOnly++
					if row["outcome"] != "problem" || row["code"] != "temporarily-unavailable" || err != nil || descriptor.Describes != DescribesLink || descriptor.OwnsOutgoing == nil {
						t.Fatalf("domain-only exception changed: row=%v descriptor=%+v err=%v", row, descriptor, err)
					}
					return
				}
				switch row["outcome"] {
				case "success":
					if err != nil {
						t.Fatal(err)
					}
					out, marshalErr := json.Marshal(descriptor)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					want, marshalErr := json.Marshal(row["descriptor"])
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if string(canonical(t, out)) != string(canonical(t, want)) {
						t.Fatal("accepted descriptor differs from vendored outcome")
					}
				case "problem":
					if err == nil {
						t.Fatal("DTO accepted invalid vendored declaration")
					}
				default:
					t.Fatalf("unclassified fixture outcome %v", row["outcome"])
				}
			})
		}
	}
	if domainOnly != 1 {
		t.Fatalf("expected one explicitly accounted domain-only probe, got %d", domainOnly)
	}
}
