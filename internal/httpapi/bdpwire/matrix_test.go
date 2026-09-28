package bdpwire

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The vendored executable Read matrix (packages/conformance/matrices/
// read-v1.json at the pin) and its metadata catalog are P2's target: the
// plan's exit for serving is "the pinned matrix green". What P0 can already
// hold them to is that the wire this package carries is the wire the matrix
// asserts on: every schema it validates a response against is a definition
// bound here, every problem it expects agrees with the closed table, and
// every JSON pointer it reads out of a discovery document resolves through
// ReadDiscovery.

type matrixStep struct {
	scenario string
	step     map[string]any
}

// matrixSteps flattens each scenario's `requests` (plain HTTP plans) and
// `actions` (harness-driven plans) into one list; both carry the same
// target/assertions shape.
func matrixSteps(t *testing.T, manifest map[string]any) []matrixStep {
	t.Helper()
	var steps []matrixStep
	for _, s := range asSlice(t, manifest["scenarios"], "scenarios") {
		scenario := asMap(t, s, "scenario")
		id := asString(t, scenario["id"], "scenario id")
		for _, key := range []string{"requests", "actions"} {
			list, ok := scenario[key]
			if !ok {
				continue
			}
			for _, step := range asSlice(t, list, id+"."+key) {
				steps = append(steps, matrixStep{scenario: id, step: asMap(t, step, id+" step")})
			}
		}
	}
	return steps
}

func TestMatrixAndCatalogAgreeOnTheReadScenarios(t *testing.T) {
	catalog := asMap(t, loadJSON(t, "conformance/read-v1.catalog.json"), "catalog")
	manifest := asMap(t, loadJSON(t, "conformance/read-v1.matrix.json"), "matrix")

	ids := func(doc map[string]any, what string) map[string]string {
		out := map[string]string{}
		for _, s := range asSlice(t, doc["scenarios"], what) {
			scenario := asMap(t, s, what)
			out[asString(t, scenario["id"], what+" id")] = asString(t, scenario["requiredProfile"], what+" profile")
		}
		return out
	}
	catalogIDs, manifestIDs := ids(catalog, "catalog"), ids(manifest, "matrix")
	if missing := diff(catalogIDs, manifestIDs); len(missing) > 0 {
		t.Errorf("catalog scenarios with no matrix plan: %v", missing)
	}
	if extra := diff(manifestIDs, catalogIDs); len(extra) > 0 {
		t.Errorf("matrix plans with no catalog scenario: %v", extra)
	}
	for id, profile := range manifestIDs {
		if profile != string(ProfileRead) {
			t.Errorf("%s requires profile %q in a Read matrix", id, profile)
		}
	}
	if got := manifest["catalogId"]; got != "read-v1" {
		t.Errorf("matrix catalogId = %v", got)
	}
	t.Logf("pinned Read matrix: %d scenarios", len(manifestIDs))
}

func TestMatrixSchemaAssertionsNameCarriedDefinitions(t *testing.T) {
	defs := loadBundleDefs(t)
	manifest := asMap(t, loadJSON(t, "conformance/read-v1.matrix.json"), "matrix")
	asserted := map[string]bool{}
	for _, ms := range matrixSteps(t, manifest) {
		for _, a := range asSlice(t, ms.step["assertions"], ms.scenario+" assertions") {
			assertion := asMap(t, a, "assertion")
			if assertion["kind"] != "json-schema" {
				continue
			}
			ref := asString(t, assertion["schema"], ms.scenario+" schema")
			name := strings.TrimPrefix(ref, "#/$defs/")
			if name == ref {
				t.Errorf("%s: schema assertion %q is not a bundle definition reference", ms.scenario, ref)
				continue
			}
			if _, ok := defs[name]; !ok {
				t.Errorf("%s: asserts against #/$defs/%s, which the vendored bundle lacks", ms.scenario, name)
			}
			binding, ok := defsToGo[name]
			if !ok || !servesAsBody(binding.goType) {
				t.Errorf("%s: asserts a response body against #/$defs/%s, which no type here carries as a served body", ms.scenario, name)
			}
			asserted[name] = true
		}
	}
	if len(asserted) == 0 {
		t.Fatal("the matrix carries no json-schema assertions; the vendored file is not the executable manifest")
	}
	t.Logf("matrix validates responses against: %v", sortedKeys(asserted))
}

// servesAsBody reports whether a bound Go type is the shape of a whole
// response body: an envelope struct, or the open Properties map — the body of
// the `view=properties` read, which the matrix validates against
// #/$defs/properties directly.
func servesAsBody(rt reflect.Type) bool {
	rt = derefPointer(rt)
	switch rt.Kind() {
	case reflect.Struct:
		return true
	case reflect.Map:
		return rt.Key().Kind() == reflect.String
	}
	return false
}

// TestMatrixProblemExpectationsAgreeWithTheTable finds every expectation
// object in the matrix that names a problem code and a retry disposition —
// direct `equals` literals and the probe rows of the reads-after-deletion
// plans — and holds it to readProblemTable.
func TestMatrixProblemExpectationsAgreeWithTheTable(t *testing.T) {
	manifest := loadJSON(t, "conformance/read-v1.matrix.json")
	found := 0
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch n := v.(type) {
		case map[string]any:
			codeValue, hasCode := n["code"].(string)
			retryValue, hasRetry := n["retry"].(string)
			if hasCode && hasRetry {
				found++
				code := ReadProblemCode(codeValue)
				if !code.Valid() {
					t.Errorf("%s: problem code %q is not in the Read table", path, codeValue)
				} else {
					if RetryDisposition(retryValue) != code.Retry() {
						t.Errorf("%s: expects retry %q for %s, table says %q", path, retryValue, code, code.Retry())
					}
					if status, ok := n["status"].(json.Number); ok && status.String() != strconv.Itoa(code.Status()) {
						t.Errorf("%s: expects status %s for %s, table says %d", path, status, code, code.Status())
					}
					if typ, ok := n["type"].(string); ok && typ != code.Type() {
						t.Errorf("%s: expects type %q for %s, table says %q", path, typ, code, code.Type())
					}
				}
				if mediaType, ok := n["mediaType"].(string); ok && mediaType != ProblemMediaType {
					t.Errorf("%s: expects media type %q, want %q", path, mediaType, ProblemMediaType)
				}
			}
			for _, k := range sortedKeys(n) {
				walk(path+"/"+k, n[k])
			}
		case []any:
			for i, item := range n {
				walk(path+"/"+strconv.Itoa(i), item)
			}
		}
	}
	walk("", manifest)
	if found == 0 {
		t.Fatal("the matrix carries no problem expectations")
	}
	t.Logf("matrix problem expectations checked against the table: %d", found)
}

// TestMatrixDiscoveryPointersResolveThroughReadDiscovery takes every
// json-pointer assertion made on a discovery response (a step whose target is
// the captured service-desc binding) and resolves it through the Go type's
// JSON tags: a member the matrix reads (the limits) is one ReadDiscovery
// serves, and a member the matrix requires ABSENT (`exists: false`) is one
// the Read type either has no field for (`operations`, which is Read+Update
// and up) or can leave off the wire (`aliases`, present exactly when the
// authority serves alias resolution — omitempty is what makes the
// no-aliases realization representable).
func TestMatrixDiscoveryPointersResolveThroughReadDiscovery(t *testing.T) {
	manifest := asMap(t, loadJSON(t, "conformance/read-v1.matrix.json"), "matrix")
	var present, absent int
	for _, ms := range matrixSteps(t, manifest) {
		target, ok := ms.step["target"].(map[string]any)
		if !ok || target["binding"] != ServiceDescRel {
			continue
		}
		for _, a := range asSlice(t, ms.step["assertions"], ms.scenario+" assertions") {
			assertion := asMap(t, a, "assertion")
			if assertion["kind"] != "json-pointer" {
				continue
			}
			pointer := asString(t, assertion["pointer"], ms.scenario+" pointer")
			resolves, omittable := resolveThroughTags(t, reflect.TypeOf(ReadDiscovery{}), pointer)
			if exists, ok := assertion["exists"].(bool); ok && !exists {
				absent++
				if resolves && !omittable {
					t.Errorf("%s: %s must be absent from this Read discovery document, but ReadDiscovery always serves it", ms.scenario, pointer)
				}
				continue
			}
			present++
			if !resolves {
				t.Errorf("%s: pointer %s does not resolve through ReadDiscovery", ms.scenario, pointer)
			}
		}
	}
	if present == 0 || absent == 0 {
		t.Fatalf("the matrix makes %d present and %d absent json-pointer assertions on a discovery response; both kinds are expected", present, absent)
	}
	t.Logf("discovery pointers checked against ReadDiscovery: %d present, %d absent", present, absent)
}

// resolveThroughTags walks an RFC 6901 pointer through a Go type — struct
// members by JSON tag, arrays by index, maps by any key — and reports whether
// it resolves and, if so, whether the member it lands on can be left off the
// wire (its final struct hop carries omitempty, or it is a map entry).
func resolveThroughTags(t *testing.T, rt reflect.Type, pointer string) (resolves, omittable bool) {
	t.Helper()
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		rt = derefPointer(rt)
		switch rt.Kind() {
		case reflect.Struct:
			fields, _ := jsonFields(t, rt)
			f, ok := fields[token]
			if !ok {
				return false, false
			}
			rt, omittable = f.field.Type, f.omitempty
		case reflect.Slice:
			if _, err := strconv.Atoi(token); err != nil {
				return false, false
			}
			rt, omittable = rt.Elem(), false
		case reflect.Map:
			rt, omittable = rt.Elem(), true
		default:
			return false, false
		}
	}
	return true, omittable
}
