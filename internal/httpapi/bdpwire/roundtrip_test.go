package bdpwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Round-trip against the vendored fixtures: every envelope-shaped document in
// them is decoded strictly into its Go type, marshaled back, and compared to
// the original canonically (sorted members, normalized whitespace and
// escaping, numbers as written). A member the Go type drops, invents,
// renames or retypes fails the comparison; a member the strict decoder does
// not know fails the decode.
//
// The upstream realization fixtures are seed data plus oracles, not literal
// HTTP bodies, so each row names the JSON-pointer-with-wildcards at which the
// envelope-shaped documents live. Seed Bead and Link records carry `localId`
// where a served record carries `id` and are therefore exercised through their
// envelope-shaped members (references, attribution, properties), while the
// complete served records — owned Links with absolute URLs, pinned external
// targets — come from the owned-links oracle and the spec's own examples.

type roundTrip struct {
	file   string
	path   string
	target func() any
}

func newTarget[T any]() func() any {
	return func() any { return new(T) }
}

var roundTrips = []roundTrip{
	// fixtures/reference-domain/reference-domain.json at the pin.
	{"fixtures/reference-domain.json", "/beadTypes/*", newTarget[TypeSummary]()},
	{"fixtures/reference-domain.json", "/linkTypes/*", newTarget[TypeSummary]()},
	{"fixtures/reference-domain.json", "/typeDescriptors/*", newTarget[TypeDescriptor]()},
	{"fixtures/reference-domain.json", "/externalEndpointLinks/*/source", newTarget[Reference]()},
	{"fixtures/reference-domain.json", "/externalEndpointLinks/*/target", newTarget[Reference]()},
	{"fixtures/reference-domain.json", "/externalEndpointLinks/*/properties", newTarget[Properties]()},

	// packages/conformance/fixtures/read-reference-v1.json: the bdptest
	// realization of the reviewed Read topology.
	{"fixtures/read-reference-v1.json", "/typeDescriptors/*", newTarget[TypeDescriptor]()},
	{"fixtures/read-reference-v1.json", "/types/*", newTarget[TypeSummary]()},
	{"fixtures/read-reference-v1.json", "/beads/*/attribution", newTarget[Attribution]()},
	{"fixtures/read-reference-v1.json", "/beads/*/properties", newTarget[Properties]()},
	{"fixtures/read-reference-v1.json", "/links/*/source", newTarget[Reference]()},
	{"fixtures/read-reference-v1.json", "/links/*/target", newTarget[Reference]()},
	{"fixtures/read-reference-v1.json", "/links/*/attribution", newTarget[Attribution]()},
	{"fixtures/read-reference-v1.json", "/links/*/properties", newTarget[Properties]()},
	{"fixtures/read-reference-v1.json", "/oracles/owned-links/ownedLinks", newTarget[OwnedLinks]()},
	{"fixtures/read-reference-v1.json", "/oracles/owned-links/ownedLinks/*/*", newTarget[LinkRecord]()},
	{"fixtures/read-reference-v1.json", "/oracles/attribution/*", newTarget[Attribution]()},
	{"fixtures/read-reference-v1.json", "/disclosures/*/archivedAt", newTarget[Reference]()},

	// packages/conformance/fixtures/read-bdpbd-v1.json: the real-bd
	// realization.
	{"fixtures/read-bdpbd-v1.json", "/typeDescriptors/*", newTarget[TypeDescriptor]()},
	{"fixtures/read-bdpbd-v1.json", "/oracles/attribution/*", newTarget[Attribution]()},
	{"fixtures/read-bdpbd-v1.json", "/oracles/resources/*/properties", newTarget[Properties]()},

	// docs/specs/bdp.md examples at the pin (schema/PROVENANCE names the lines).
	{"spec-examples/0583-scope-aggregate-constraints-1.json", "/maximumEndpointMultiplicity/*", newTarget[MaximumEndpointMultiplicityPolicy]()},
	{"spec-examples/1662-scope-discovery-and-human-documentation-1.json", "", newTarget[ReadDiscovery]()},
	{"spec-examples/1817-advertised-limits-1.json", "/limits", newTarget[AdvertisedLimits]()},
	{"spec-examples/1994-resource-records-1.json", "", newTarget[BeadRecord]()},
	{"spec-examples/2009-resource-records-2.json", "", newTarget[LinkRecord]()},
	{"spec-examples/2044-resource-records-3.json", "", newTarget[Reference]()},
	{"spec-examples/2053-resource-records-4.json", "", newTarget[Reference]()},
	{"spec-examples/2112-resource-views-1.json", "", newTarget[BeadRecord]()},
	{"spec-examples/2112-resource-views-1.json", "/links", newTarget[LinkCollection]()},
	{"spec-examples/2112-resource-views-1.json", "/links/items/*", newTarget[LinkRecord]()},
	{"spec-examples/2227-types-and-type-descriptors-1.json", "", newTarget[TypesInventory]()},
	{"spec-examples/2227-types-and-type-descriptors-1.json", "/items/*", newTarget[TypeSummary]()},
	{"spec-examples/2269-types-and-type-descriptors-2.json", "", newTarget[TypeDescriptor]()},
	{"spec-examples/2286-types-and-type-descriptors-3.json", "", newTarget[TypeDescriptor]()},
}

func TestFixturesRoundTripThroughTheWireTypes(t *testing.T) {
	covered := map[reflect.Type]int{}
	for _, rt := range roundTrips {
		name := rt.file + rt.path
		t.Run(name, func(t *testing.T) {
			docs := selectAll(loadJSON(t, rt.file), rt.path)
			if len(docs) == 0 {
				t.Fatalf("%s selects nothing: the fixture moved or the path is wrong", name)
			}
			for i, doc := range docs {
				original, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				target := rt.target()
				if err := Unmarshal(original, target); err != nil {
					t.Errorf("[%d] strict decode into %T: %v\n%s", i, target, err, original)
					continue
				}
				out, err := json.Marshal(target)
				if err != nil {
					t.Errorf("[%d] marshal %T: %v", i, target, err)
					continue
				}
				if got, want := canonical(t, out), canonical(t, original); !bytes.Equal(got, want) {
					t.Errorf("[%d] round trip through %T changed the document\n got: %s\nwant: %s", i, target, got, want)
				}
				covered[reflect.TypeOf(target).Elem()] += 1
			}
		})
	}

	// Every envelope the Read profile serves must be exercised by at least
	// one fixture document; a type nothing round-trips is a type nothing
	// proved.
	for _, want := range []any{
		ReadDiscovery{}, AdvertisedLimits{}, MaximumEndpointMultiplicityPolicy{},
		BeadRecord{}, LinkRecord{}, Reference{}, Attribution{}, Properties{},
		LinkCollection{}, TypesInventory{}, TypeSummary{}, TypeDescriptor{}, OwnedLinks{},
	} {
		if covered[reflect.TypeOf(want)] == 0 {
			t.Errorf("%T is round-tripped by no fixture document", want)
		}
	}
}

// TestFixturesExerciseTheShapesThatMatter pins the properties of the vendored
// corpus that make the round trip meaningful, so a re-pin that quietly loses
// one of them is noticed: a pinned external target, a pinned in-Scope target,
// an unpinned external source, explicit and wildcard owning descriptors, an
// owned-links member holding a pinned external target, and both attribution
// statuses.
func TestFixturesExerciseTheShapesThatMatter(t *testing.T) {
	reference := loadJSON(t, "fixtures/read-reference-v1.json")

	var pinnedExternal, pinnedLocal, unpinnedExternal int
	for _, doc := range selectAll(reference, "/links/*") {
		link := asMap(t, doc, "link")
		for _, end := range []string{"source", "target"} {
			raw, _ := json.Marshal(link[end])
			var ref Reference
			if err := Unmarshal(raw, &ref); err != nil {
				t.Fatalf("%s: %v", end, err)
			}
			local := strings.HasPrefix(ref.URI, "beads/")
			switch {
			case ref.Pinned() && !local:
				pinnedExternal++
			case ref.Pinned() && local:
				pinnedLocal++
			case !ref.Pinned() && !local:
				unpinnedExternal++
			}
		}
	}
	if pinnedExternal == 0 || pinnedLocal == 0 || unpinnedExternal == 0 {
		t.Errorf("reference fixture endpoints: pinned external %d, pinned local %d, unpinned external %d; each must be exercised", pinnedExternal, pinnedLocal, unpinnedExternal)
	}

	owning, wildcardOwning := 0, 0
	for _, doc := range selectAll(reference, "/typeDescriptors/*") {
		raw, _ := json.Marshal(doc)
		var td TypeDescriptor
		if err := Unmarshal(raw, &td); err != nil {
			t.Fatal(err)
		}
		if td.Describes == DescribesBead && td.OwnsOutgoing != nil {
			owning++
			if td.OwnsOutgoing.Wildcard != nil {
				wildcardOwning++
			}
			for url, decl := range td.OwnsOutgoing.Types {
				if decl.Max < 1 {
					t.Errorf("%s owns %s with max %d", td.ID, url, decl.Max)
				}
			}
		}
		if td.Describes == DescribesLink && (td.Source == nil || td.Target == nil) {
			t.Errorf("link descriptor %s lacks an endpoint constraint", td.ID)
		}
	}
	if owning == 0 {
		t.Error("no descriptor in the reference fixture owns outgoing Links")
	}

	if wildcardOwning == 0 {
		t.Error("no descriptor in the reference fixture owns outgoing Links through a wildcard")
	}

	ownedRaw, _ := json.Marshal(selectAll(reference, "/oracles/owned-links/ownedLinks")[0])
	var owned OwnedLinks
	if err := Unmarshal(ownedRaw, &owned); err != nil {
		t.Fatal(err)
	}
	pinnedOwnedTarget := false
	for typeURL, links := range owned {
		for _, link := range links {
			if link.Type != typeURL {
				t.Errorf("owned link %s has type %s under entry %s", link.ID, link.Type, typeURL)
			}
			if link.Target.Pinned() {
				pinnedOwnedTarget = true
			}
		}
	}
	if !pinnedOwnedTarget {
		t.Error("the owned-links oracle carries no pinned target")
	}

	statuses := map[AttributionStatus]bool{}
	for _, doc := range selectAll(reference, "/oracles/attribution/*") {
		raw, _ := json.Marshal(doc)
		var a Attribution
		if err := Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		if !a.Status.Valid() {
			t.Errorf("attribution status %q", a.Status)
		}
		statuses[a.Status] = true
	}
	if !statuses[AttributionClaimed] || !statuses[AttributionUnknown] {
		t.Errorf("attribution oracle statuses %v; both claimed and unknown must be exercised", statuses)
	}
}

// TestHigherProfileDiscoveryIsRefusedByTheReadType uses the spec's own
// Read+Update and Transactional discovery examples: each carries members the
// Read column of the discovery table prohibits, and the closed ReadDiscovery
// type refuses them by construction rather than by a check someone could
// forget.
func TestHigherProfileDiscoveryIsRefusedByTheReadType(t *testing.T) {
	for _, tc := range []struct{ file, prohibited string }{
		{"spec-examples/1676-scope-discovery-and-human-documentation-2.json", "operations"},
		{"spec-examples/1691-scope-discovery-and-human-documentation-3.json", "scopeEpoch"},
	} {
		var d ReadDiscovery
		err := Unmarshal(readSchemaFile(t, tc.file), &d)
		if err == nil {
			t.Errorf("%s: decoded into ReadDiscovery although it carries %q", tc.file, tc.prohibited)
			continue
		}
		if !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: refused for the wrong reason: %v", tc.file, err)
		}
	}
}
