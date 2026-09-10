package httpapi

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// pinnedSchemas are the schemas welded to canonical Go structs with x-go-type.
// Because the generator emits a type alias for them, the compiler cannot see
// the spec's property list at all — this test is the ONLY guard that the
// document still describes the struct it claims to describe.
//
// EVERY x-go-type-pinned schema belongs here, not just the top-level response
// bodies. Dependency, Comment and BondRef are nested inside issue bodies, so a
// field added to one of them ships on the wire just as silently — and more
// silently than an Issue field, which a populated `omitempty` tag would at
// least surface in the byte-pinned CLI protocol corpus. internal/httpapi/
// pinning.go proves those three are aliases; only this test proves the document
// still describes them.
// The import path is per-entry rather than one constant, because the canonical
// structs do not all live in one package: nine are internal/types, and the
// three that are not — the cycle pair and IssueBlocking — live in the role
// package issueops, where the results of issueops.CycleDetector and
// issueops.BlockingAnnotator are declared, and EventRecord lives in
// internal/eventsjournal, which owns the journal's published envelope for both
// front doors. Asserting the declared path per schema keeps the pin's TARGET
// checked, which is what a bare x-go-type name would not do.
var pinnedSchemas = []struct {
	name       string
	goType     string
	importPath string
	value      any
}{
	{"Issue", "types.Issue", canonicalTypesImport, types.Issue{}},
	{"IssueWithCounts", "types.IssueWithCounts", canonicalTypesImport, types.IssueWithCounts{}},
	{"IssueDetails", "types.IssueDetails", canonicalTypesImport, types.IssueDetails{}},
	{"IssueWithDependencyMetadata", "types.IssueWithDependencyMetadata", canonicalTypesImport, types.IssueWithDependencyMetadata{}},
	{"TreeNode", "types.TreeNode", canonicalTypesImport, types.TreeNode{}},
	{"Dependency", "types.Dependency", canonicalTypesImport, types.Dependency{}},
	{"Comment", "types.Comment", canonicalTypesImport, types.Comment{}},
	{"BondRef", "types.BondRef", canonicalTypesImport, types.BondRef{}},
	{"Statistics", "types.Statistics", canonicalTypesImport, types.Statistics{}},
	{"Cycle", "issueops.Cycle", canonicalRolesImport, issueops.Cycle{}},
	{"CycleMember", "issueops.CycleMember", canonicalRolesImport, issueops.CycleMember{}},
	{"IssueBlocking", "issueops.IssueBlocking", canonicalRolesImport, issueops.IssueBlocking{}},
	{"EventRecord", "eventsjournal.Record", canonicalJournalImport, eventsjournal.Record{}},
}

const (
	canonicalTypesImport = "github.com/steveyegge/beads/internal/types"
	canonicalRolesImport = "github.com/steveyegge/beads/issueops"
	// The journal record lives in neither: it is the leaf package that owns the
	// journal's cross-binary concerns, and its Record is what `bd events tail`
	// marshals.
	canonicalJournalImport = "github.com/steveyegge/beads/internal/eventsjournal"
)

// omittedProperties lists JSON field names a pinned schema deliberately does
// not document, keyed by schema name. It STARTS EMPTY and must stay that way
// unless there is a reason worth writing down here: every entry is a hole in
// the only check that the wire contract matches the struct.
var omittedProperties = map[string]map[string]string{}

// TestWireTagBijection asserts two-way set equality between the JSON tags of
// the canonical structs and the properties of the schemas pinned to them.
//
// Two-way, and reflection-based, on purpose. A round-trip fixture test is not
// a substitute: a newly added `omitempty` field that the fixture does not
// mention round-trips cleanly in both directions and the drift ships silently.
// Here, a field added to types.Issue without a spec entry fails CI, and a spec
// property with no Go field fails CI.
func TestWireTagBijection(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, pinned := range pinnedSchemas {
		t.Run(pinned.name, func(t *testing.T) {
			schema := mapAt(t, schemas, pinned.name)

			// The pin itself is part of the contract: without x-go-type the
			// generator would emit a mirror struct and this test would be
			// comparing the spec against a type nothing serializes.
			if got, _ := schema["x-go-type"].(string); got != pinned.goType {
				t.Fatalf("x-go-type = %q, want %q", got, pinned.goType)
			}
			imp := mapAt(t, schema, "x-go-type-import")
			if got, _ := imp["path"].(string); got != pinned.importPath {
				t.Fatalf("x-go-type-import.path = %q, want %q", got, pinned.importPath)
			}

			specProps := schemaProperties(t, doc, schema)
			goProps := jsonTagNames(t, reflect.TypeOf(pinned.value))

			for name, why := range omittedProperties[pinned.name] {
				if why == "" {
					t.Fatalf("omitted property %q carries no justification", name)
				}
				delete(goProps, name)
				delete(specProps, name)
			}

			if missing := diff(goProps, specProps); len(missing) > 0 {
				t.Errorf("Go fields with no spec property: %v\n"+
					"add them to the %s schema in internal/httpapi/spec/openapi.v0.yaml — they are on the wire already",
					missing, pinned.name)
			}
			if extra := diff(specProps, goProps); len(extra) > 0 {
				t.Errorf("spec properties with no Go field: %v\n"+
					"the %s schema promises fields that %s never marshals",
					extra, pinned.name, pinned.goType)
			}
		})
	}
}

// jsonTagNames collects the serialized names of a struct, walking embedded
// structs the way encoding/json does. Fields tagged `json:"-"` carry no wire
// name and are excluded by construction, which is what keeps internal-only
// fields (content hashes, row versions, routing overrides, the lite-partial
// marker) out of the contract without an allowlist entry.
//
// An exported field with NO json tag is a wire field too — encoding/json keys
// it by its Go name — so it counts under that name. Treating a missing tag as
// "not on the wire" would have left the only hole in this gate: an untagged
// field ships in every response while the schema says nothing about it.
func jsonTagNames(t *testing.T, rt reflect.Type) map[string]bool {
	t.Helper()
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", rt)
	}

	out := map[string]bool{}
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]

		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				// Promoted fields are wire fields. Shadowing (an outer field
				// with the same JSON name as an embedded one) collapses into
				// the same set entry, exactly as it collapses into one key on
				// the wire.
				for n := range jsonTagNames(t, et) {
					out[n] = true
				}
				continue
			}
			// An embedded non-struct (or embedded pointer to one) marshals
			// under its TYPE name.
			name = f.Name
		}
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// schemaProperties collects a schema's property names. allOf branches are
// unioned in (and local $refs resolved) so the check keeps working if the
// document ever composes — though it cannot today: oapi-codegen v2.6.0 drops
// x-go-type from an allOf-bearing component, which internal/httpapi/pinning.go
// turns into a build failure.
func schemaProperties(t *testing.T, doc, schema map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name := range props {
			out[name] = true
		}
	}
	if branches, ok := schema["allOf"].([]any); ok {
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("allOf branch is %T, want a mapping", raw)
			}
			for name := range schemaProperties(t, doc, resolveRef(t, doc, branch)) {
				out[name] = true
			}
		}
	}
	return out
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
