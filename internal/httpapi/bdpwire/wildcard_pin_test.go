package bdpwire

import (
	"encoding/json"
	"testing"
)

func TestOwnedOutgoingSumAtReadFoundationPin(t *testing.T) {
	for _, raw := range []string{
		`{"*":{"max":3}}`,
		`{"https://example.org/types/l":{"max":2}}`,
		`{"*":{"max":3},"https://example.org/types/l":{"max":3,"label":"Links"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var d OwnedOutgoingDeclarations
			if err := Unmarshal([]byte(raw), &d); err != nil {
				t.Fatal(err)
			}
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			var again OwnedOutgoingDeclarations
			if err := json.Unmarshal(got, &again); err != nil {
				t.Fatal(err)
			}
			if d.Wildcard != nil && (again.Wildcard == nil || again.Wildcard.Max != d.Wildcard.Max) {
				t.Fatal("lost wildcard")
			}
			if len(again.Types) != len(d.Types) {
				t.Fatal("lost explicit entries")
			}
		})
	}
	for _, raw := range []string{
		`{}`, `{"*":{}}`, `{"*":{"max":0}}`, `{"*":{"max":-1}}`,
		`{"*":{"max":3,"label":""}}`, `{"*":{"max":3,"label":"all"}}`,
		`{"*":{"max":3,"label":null}}`, `{"*":{"max":3,"extra":1}}`,
		`{"*":{"max":2},"https://example.org/types/l":{"max":3}}`,
		`{"https://example.org/types/l":{"max":2,"label":""}}`,
		`{"https://example.org/types/l":{}}`, `{"https://example.org/types/l":{"max":0}}`,
		`{"types/local":{"max":1}}`, `{"https://\n":{"max":1}}`, `{"*":{"max":2},"*":{"max":3}}`,
	} {
		t.Run("reject/"+raw, func(t *testing.T) {
			var direct, standard OwnedOutgoingDeclarations
			if err := Unmarshal([]byte(raw), &direct); err == nil {
				t.Fatal("strict decode accepted invalid declaration")
			}
			if err := json.Unmarshal([]byte(raw), &standard); err == nil {
				t.Fatal("JSON decode accepted invalid declaration")
			}
		})
	}
	for _, d := range []OwnedOutgoingDeclarations{
		{}, {Wildcard: &OwnedWildcardDeclaration{Max: 0}},
		{Types: map[string]OwnedLinkDeclaration{"*": {Max: 1}}},
		{Wildcard: &OwnedWildcardDeclaration{Max: 1}, Types: map[string]OwnedLinkDeclaration{"https://example.org/types/l": {Max: 2}}},
	} {
		if d.Validate() == nil {
			t.Fatal("Validate accepted invalid constructed sum")
		}
		if _, err := json.Marshal(d); err == nil {
			t.Fatal("marshal accepted invalid constructed sum")
		}
	}
}

func TestWildcardSchemaKeyIsOnlyADeclaration(t *testing.T) {
	defs := loadBundleDefs(t)
	if asMap(t, defs["absoluteHttpUrl"], "absoluteHttpUrl")["pattern"] != `^https?://.+` {
		t.Fatal("owned key pattern drifted from pinned schema")
	}
	descriptor := asMap(t, defs["typeDescriptor"], "typeDescriptor")
	owns := asMap(t, asMap(t, descriptor["properties"], "properties")["ownsOutgoing"], "ownsOutgoing")
	wildcard := asMap(t, asMap(t, owns["properties"], "properties")["*"], "wildcard")
	if wildcard["$ref"] != "#/$defs/ownedWildcardDeclaration" {
		t.Fatal("wildcard lost its distinct max-only definition")
	}
	bead := asMap(t, defs["beadRecord"], "beadRecord")
	owned := asMap(t, asMap(t, bead["properties"], "properties")["ownedLinks"], "ownedLinks")
	if asMap(t, owned["propertyNames"], "propertyNames")["$ref"] != "#/$defs/absoluteHttpUrl" {
		t.Fatal("record grouping must remain keyed by actual Link Type URL")
	}
}

func TestOwnedTypePatternECMAScriptLineTerminators(t *testing.T) {
	for _, suffix := range []string{"\n", "\r", "\u2028", "\u2029"} {
		for _, prefix := range []string{"http://", "https://"} {
			for _, control := range []bool{false, true} {
				key := prefix + suffix
				if control {
					// The schema pattern is not end anchored: an initial ordinary
					// character can match even if a line terminator follows it.
					key = prefix + "x" + suffix
				}
				t.Run(key, func(t *testing.T) {
					value := OwnedOutgoingDeclarations{Types: map[string]OwnedLinkDeclaration{key: {Max: 1}}}
					if (value.Validate() == nil) != control {
						t.Fatalf("Validate acceptance differs from ECMAScript prefix pattern: %q", key)
					}
					if _, err := json.Marshal(value); (err == nil) != control {
						t.Fatalf("Marshal acceptance differs from ECMAScript prefix pattern: %q", key)
					}
					raw, err := json.Marshal(map[string]any{key: map[string]int{"max": 1}})
					if err != nil {
						t.Fatal(err)
					}
					var strict, standard OwnedOutgoingDeclarations
					if err := Unmarshal(raw, &strict); (err == nil) != control {
						t.Fatalf("strict Unmarshal acceptance differs: %q", key)
					}
					if err := json.Unmarshal(raw, &standard); (err == nil) != control {
						t.Fatalf("JSON Unmarshal acceptance differs: %q", key)
					}
				})
			}
		}
	}
}

func TestDescriptorUsesStrictOwnedOutgoingSum(t *testing.T) {
	prefix := `{"id":"https://example.org/types/b","name":"Owner","describes":"bead","conformsTo":[],"ownsOutgoing":`
	for _, raw := range []string{`{"*":{"max":2}}`, `{"*":{"max":2},"https://example.org/types/l":{"max":1}}`} {
		var d TypeDescriptor
		if err := Unmarshal([]byte(prefix+raw+`}`), &d); err != nil {
			t.Fatal(err)
		}
		if d.OwnsOutgoing == nil || d.OwnsOutgoing.Wildcard == nil || d.OwnsOutgoing.Wildcard.Max != 2 {
			t.Fatal("descriptor omitted wildcard")
		}
	}
	for _, raw := range []string{`{"*":{}}`, `{"*":{"max":2,"label":""}}`, `{"*":{"max":2},"https://example.org/types/l":{"max":3}}`} {
		var d TypeDescriptor
		if err := Unmarshal([]byte(prefix+raw+`}`), &d); err == nil {
			t.Fatal("nested sum accepted invalid declaration")
		}
	}
}

// The source regexp assertion above is a provenance tripwire, not semantic
// equivalence proof. These controls preserve characters ECMAScript dot admits
// and the prefix match after an otherwise ordinary character.
func TestOwnedTypePatternAdmitsWhitespaceAndSuffixes(t *testing.T) {
	for _, key := range []string{"https:// ", "http://\t", "https://x\nmore", "http://x\rmore", "https://x\u2028more", "http://x\u2029more"} {
		t.Run(key, func(t *testing.T) {
			value := OwnedOutgoingDeclarations{Types: map[string]OwnedLinkDeclaration{key: {Max: 1}}}
			if err := value.Validate(); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var strict, standard OwnedOutgoingDeclarations
			if err := Unmarshal(raw, &strict); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &standard); err != nil {
				t.Fatal(err)
			}
		})
	}
}
