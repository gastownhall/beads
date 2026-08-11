package httpapi

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpapi/spec"
	"github.com/steveyegge/beads/internal/workapi"
)

// These tests are the spec drift gates. They are pure — no database, no
// server, no build tag — because they must run in the PR workflow's
// unconditional Go test job, not in a shard that only some pushes reach. With
// the wire structs pinned to canonical Go types via x-go-type, the compiler
// cannot see the spec at all, so if these move to a conditional CI tier the
// contract stops being checked.

var httpVerbs = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true, "patch": true,
	"head": true, "options": true, "trace": true,
}

// streamingOps are the operations whose success body is a stream rather than a
// document, and therefore the only ones exempt from the application/json rule.
//
// It is a literal here and a `streaming` column in the route table, which is
// the drift this pairing catches: TestStreamingRowsAreTheDocumentsStreamingOps
// requires the two to name the same operations, so a row that starts streaming
// without saying so in the document — or a document that promises a stream no
// row holds open — fails.
var streamingOps = map[string]bool{
	OpWatchEvents: true,
}

type specOp struct {
	path   string
	method string
	op     map[string]any
}

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		t.Fatalf("parse embedded openapi document: %v", err)
	}
	return doc
}

func mapAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := parent[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("key %q is %T, want a mapping", key, v)
	}
	return m
}

// resolveRef follows a local $ref one level, which is all this document uses.
func resolveRef(t *testing.T, doc map[string]any, node map[string]any) map[string]any {
	t.Helper()
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		t.Fatalf("only local $refs are supported, got %q", ref)
	}
	cur := doc
	parts := strings.Split(rest, "/")
	for i, part := range parts {
		next, ok := cur[part]
		if !ok {
			t.Fatalf("$ref %q: no such node %q", ref, strings.Join(parts[:i+1], "/"))
		}
		m, ok := next.(map[string]any)
		if !ok {
			t.Fatalf("$ref %q: node %q is %T, want a mapping", ref, part, next)
		}
		cur = m
	}
	return cur
}

func specOps(t *testing.T, doc map[string]any) map[string]specOp {
	t.Helper()
	out := map[string]specOp{}
	for path, item := range mapAt(t, doc, "paths") {
		methods, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("path %q is %T, want a mapping", path, item)
		}
		for method, raw := range methods {
			if !httpVerbs[strings.ToLower(method)] {
				continue
			}
			op, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s %s is %T, want a mapping", method, path, raw)
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Fatalf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			if prev, dup := out[id]; dup {
				t.Fatalf("operationId %q used twice: %s %s and %s %s",
					id, prev.method, prev.path, strings.ToUpper(method), path)
			}
			out[id] = specOp{path: path, method: strings.ToUpper(method), op: op}
		}
	}
	return out
}

func sortedCodes(codes []Code) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// TestSpecGovernance pins the document-level invariants that make the rest of
// the contract legible: one OpenAPI version, one error shape everywhere, a
// machine-readable code list on every documented failure, and no vendor
// vocabulary.
func TestSpecGovernance(t *testing.T) {
	doc := loadSpec(t)

	if got, _ := doc["openapi"].(string); got != "3.0.3" {
		t.Errorf("openapi = %q, want 3.0.3", got)
	}
	if got, _ := doc["x-bd-source"].(string); got != "spec-first" {
		t.Errorf("x-bd-source = %q, want spec-first (the document is hand-written and generates the Go types)", got)
	}
	if got, _ := mapAt(t, doc, "info")["version"].(string); got == "" {
		t.Error("info.version is empty")
	}

	// The OSS surface stays vendor-neutral: no product names, no hosted-only
	// extensions.
	if raw := string(spec.OpenAPIV0()); strings.Contains(raw, "x-gc-") {
		t.Error("document carries x-gc-* extensions; the OSS spec must stay vendor-neutral")
	}

	for id, so := range specOps(t, doc) {
		if _, ok := so.op["summary"].(string); !ok {
			t.Errorf("%s: no summary", id)
		}
		if _, ok := so.op["description"].(string); !ok {
			t.Errorf("%s: no description", id)
		}
		for status, raw := range mapAt(t, so.op, "responses") {
			node, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s %s response is %T, want a mapping", id, status, raw)
			}
			resp := resolveRef(t, doc, node)
			content := mapAt(t, resp, "content")
			if len(content) != 1 {
				t.Errorf("%s %s: %d media types, want exactly 1", id, status, len(content))
			}
			if strings.HasPrefix(status, "2") {
				if streamingOps[id] {
					// The one carve-out, and it is an allowlist rather than a
					// relaxed rule: a response held open for the life of a
					// connection cannot be a JSON document, but every OTHER
					// operation must still fail here if it grows a second media
					// type or drifts off application/json. A new streaming
					// operation costs a line in this map and a paragraph in the
					// document's Media types section, which is the point.
					if _, ok := content["text/event-stream"]; !ok {
						t.Errorf("%s %s: a streaming operation's success body is text/event-stream", id, status)
					}
					continue
				}
				if _, ok := content["application/json"]; !ok {
					t.Errorf("%s %s: success bodies are application/json", id, status)
				}
				continue
			}
			body, ok := content["application/problem+json"]
			if !ok {
				t.Errorf("%s %s: every non-2xx body is application/problem+json", id, status)
				continue
			}
			bodyMap, ok := body.(map[string]any)
			if !ok {
				t.Fatalf("%s %s: content is %T, want a mapping", id, status, body)
			}
			schema := mapAt(t, bodyMap, "schema")
			if got, _ := schema["$ref"].(string); got != "#/components/schemas/Problem" {
				t.Errorf("%s %s: schema %q, want the one Problem envelope", id, status, got)
			}
			if _, ok := resp["x-bd-codes"]; !ok {
				t.Errorf("%s %s: no x-bd-codes; every documented failure names its machine codes", id, status)
			}
		}
	}
}

// TestSpecRouteParity is the gate that the document and the server describe the
// same surface. Exact set equality, both directions: a documented operation
// with no route is a promise nothing keeps, and a route with no documentation
// is undisclosed surface. It also checks the operationId on each pair, so two
// operations cannot quietly swap paths while the set still matches.
//
// The route table IS the router — internal/httpapi/server.go builds the mux
// from it and from nothing else — so this compares what the server actually
// serves. The last step builds a real ServeMux from the table for the same
// reason: a pattern ServeMux refuses (a conflict, a malformed wildcard) is a
// panic on the first request, and it should be a test failure instead.
func TestSpecRouteParity(t *testing.T) {
	type methodPath struct{ method, path string }

	doc := loadSpec(t)
	specSet := map[methodPath]string{}
	for id, so := range specOps(t, doc) {
		specSet[methodPath{so.method, so.path}] = id
	}

	routeSet := map[methodPath]string{}
	for _, rt := range routeTable {
		key := methodPath{rt.method, rt.specPathOf()}
		if prev, dup := routeSet[key]; dup {
			t.Fatalf("%s %s is routed twice: %q and %q", key.method, key.path, prev, rt.op)
		}
		routeSet[key] = rt.op
	}

	for key, id := range specSet {
		op, ok := routeSet[key]
		if !ok {
			t.Errorf("spec documents %s %s (%s) with no route", key.method, key.path, id)
			continue
		}
		if op != id {
			t.Errorf("%s %s: route serves operation %q, the spec calls it %q", key.method, key.path, op, id)
		}
	}
	for key, op := range routeSet {
		if _, ok := specSet[key]; !ok {
			t.Errorf("route %s %s (%s) is not in the spec; undocumented surface", key.method, key.path, op)
		}
	}

	// A row that DECLARES a specPath is compared against the document by a
	// string the router never sees, so for those rows the check above proves
	// nothing about what the server serves: the pattern could grow a wrong
	// prefix or a wrong segment and this test would stay green while the
	// documented path 404s. Bound the exception instead of trusting it — the
	// pattern must be the documented path with its final segment replaced by
	// one wildcard, which is the only thing ServeMux cannot spell — and refuse
	// a declaration that is not needed at all.
	//
	// TestClaimPathReachesItsHandler (server_test.go) is the behavioral half.
	for _, rt := range routeTable {
		if rt.specPath == "" {
			continue
		}
		specDir, specLast := splitLastSegment(rt.specPath)
		patDir, patLast := splitLastSegment(rt.pattern)
		if specDir != patDir {
			t.Errorf("%s: pattern %q and declared spec path %q do not share a prefix; the documented path is not the one routed",
				rt.op, rt.pattern, rt.specPath)
		}
		if !isWildcardSegment(patLast) {
			t.Errorf("%s: pattern %q declares a different spec path, so its final segment must be a single-segment wildcard (got %q)",
				rt.op, rt.pattern, patLast)
		}
		if isWildcardSegment(specLast) {
			t.Errorf("%s: spec path %q is expressible as a ServeMux pattern; delete the specPath declaration rather than detaching the row from the router",
				rt.op, rt.specPath)
		}
	}

	// Build the server's real handler: ServeMux panics on a conflicting or
	// malformed pattern, and that belongs here rather than on the first
	// request after a deploy.
	if h := (&Server{}).handler(); h == nil {
		t.Fatal("handler() returned nil")
	}
}

// TestStreamingRowsAreTheDocumentsStreamingOps ties the route table's
// `streaming` column to the media-type carve-out above.
//
// The two describe one property of an operation from opposite ends: the column
// decides that the request gets no deadline and takes its database slot per
// read, and the document decides that clients are told to expect a stream. An
// operation that gained one without the other is either a JSON handler running
// without the backstop that bounds every other request, or a documented stream
// that the router will cut off at sixty seconds — and neither has any other
// test that can see it.
func TestStreamingRowsAreTheDocumentsStreamingOps(t *testing.T) {
	routed := map[string]bool{}
	for _, rt := range routeTable {
		if rt.streaming {
			routed[rt.op] = true
		}
		// A streaming row that still queued for the request-wide slot would hold
		// one of sixteen for the life of a connection. The pairing is stated on
		// the field and enforced here.
		if rt.streaming && !rt.bypassSemaphore {
			t.Errorf("%s streams but holds the request-wide database slot; a stream must take its slot per read", rt.op)
		}
	}

	if missing := diff(routed, streamingOps); len(missing) > 0 {
		t.Errorf("route table streams %v, the document does not describe them as streaming operations", missing)
	}
	if extra := diff(streamingOps, routed); len(extra) > 0 {
		t.Errorf("the document treats %v as streaming, but no route row is marked streaming", extra)
	}
}

func splitLastSegment(path string) (dir, last string) {
	i := strings.LastIndex(path, "/")
	return path[:i+1], path[i+1:]
}

// isWildcardSegment reports whether seg is a ServeMux single-segment wildcard.
// The multi-segment form ({x...}) is not one: it swallows the rest of the path,
// which is wider surface than any documented operation.
func isWildcardSegment(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && !strings.Contains(seg, "...")
}

// TestSpecDefaultsMatchSharedConstants keeps the document honest about the two
// limit defaults and about limit=0. The whole point of sharing constants
// between the CLI flag and the HTTP parameter is that the two surfaces cannot
// diverge; a spec that states a different number would reintroduce the
// divergence in the one place clients actually read.
func TestSpecDefaultsMatchSharedConstants(t *testing.T) {
	doc := loadSpec(t)
	ops := specOps(t, doc)

	for _, tc := range []struct {
		opID string
		want int
	}{
		{OpListIssues, workapi.DefaultListLimit},
		{OpListReadyWork, workapi.DefaultReadyLimit},
	} {
		so, ok := ops[tc.opID]
		if !ok {
			t.Fatalf("operation %q missing from the spec", tc.opID)
		}
		limit := specParam(t, so, "limit")
		schema := mapAt(t, limit, "schema")

		got, ok := schema["default"].(int)
		if !ok {
			t.Fatalf("%s: limit has no integer default (%T)", tc.opID, schema["default"])
		}
		if got != tc.want {
			t.Errorf("%s: spec documents limit default %d, shared constant is %d", tc.opID, got, tc.want)
		}
		if lo, ok := schema["minimum"].(int); !ok || lo != 0 {
			t.Errorf("%s: limit minimum = %v, want 0 (limit=0 is a legal, meaningful value)", tc.opID, schema["minimum"])
		}

		// limit=0 means unlimited on both surfaces. If this phrasing ever
		// changes, change it deliberately — clients depend on the behavior
		// and this is the only place the wire documents it.
		desc, _ := limit["description"].(string)
		if !strings.Contains(desc, "`0` means unlimited") {
			t.Errorf("%s: limit description does not document that `0` means unlimited", tc.opID)
		}
		if !strings.Contains(desc, "--allow-non-loopback") {
			t.Errorf("%s: limit description does not document the non-loopback refusal of limit=0", tc.opID)
		}
	}

	// The anchor bound on ALL THREE dependency-collection reads. Each bounds
	// the same thing — how many issues one call may ask about — from the same
	// maxDependencyAnchors, and each says so in prose. Nothing kept the
	// document's number and the constant together until this: the bound is
	// enforced in the handler, so a spec that drifted to a different maxItems
	// would be refused by the server it documents, at a number no reader could
	// have predicted.
	//
	// THE THIRD ROW IS THE POINT. listBlockingAnnotations enforces the same
	// constant (blocking.go) and declares the same maxItems, and it predates
	// this gate — so a two-operation loop would have pinned the copies that
	// happened to be in the slice that wrote it and left the oldest one free
	// to drift. Every operation reading maxDependencyAnchors belongs here, and
	// the next one to do so must be added.
	for _, opID := range []string{OpListDependencies, OpCountDependencyEdges, OpListBlockingAnnotations} {
		so, ok := ops[opID]
		if !ok {
			t.Fatalf("operation %q missing from the spec", opID)
		}
		schema := mapAt(t, specParam(t, so, "issue_id"), "schema")
		if got, ok := schema["maxItems"].(int); !ok || got != maxDependencyAnchors {
			t.Errorf("%s: issue_id maxItems = %v, want the shared bound %d", opID, schema["maxItems"], maxDependencyAnchors)
		}
		// At least one is required on both, and the schema is where a
		// generated client learns it.
		if got, ok := schema["minItems"].(int); !ok || got != 1 {
			t.Errorf("%s: issue_id minItems = %v, want 1", opID, schema["minItems"])
		}
	}
}

// capabilityToken matches one backticked capability token in the
// `capabilities` description. The shape — a lowercase resource, a dot, a
// lowerCamel verb — is what every token on this surface is, and it is narrow
// enough that no other backticked word in that paragraph can be mistaken for
// one.
var capabilityToken = regexp.MustCompile("`([a-z]+\\.[a-zA-Z]+)`")

// TestSpecCapabilityVocabularyMatchesTheRouteTable is the gate for the ONE
// piece of per-operation plumbing that had no gate.
//
// The `capabilities` description enumerates every token explicitly, in prose,
// and prose is not generated from anything: an operation could land with its
// route row, its handler, its problem codes and its spec path all correct and
// still never appear in the list a client is told to check. Nothing would fail.
// The document would simply be wrong about the one member it tells clients to
// dispatch on, and the drift would be discovered by a client, not by CI.
//
// Set equality both ways: a token in the prose with no implemented route is a
// capability advertised to readers and not to clients, and a route whose token
// the prose omits is an operation the document forgets it has.
//
// ORDER IS NOT CHECKED. The prose groups tokens by resource for a reader;
// Capabilities() sorts them for a client. Both are deliberate, and pinning one
// to the other would be a rule about paragraph layout.
func TestSpecCapabilityVocabularyMatchesTheRouteTable(t *testing.T) {
	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ContextResponse")
	caps := mapAt(t, mapAt(t, schema, "properties"), "capabilities")
	desc, _ := caps["description"].(string)
	if desc == "" {
		t.Fatal("the capabilities property has no description; it is where the token vocabulary is published")
	}

	documented := map[string]bool{}
	for _, m := range capabilityToken.FindAllStringSubmatch(desc, -1) {
		documented[m[1]] = true
	}
	served := map[string]bool{}
	for _, token := range Capabilities() {
		served[token] = true
	}

	if missing := diff(served, documented); len(missing) > 0 {
		t.Errorf("this build serves capabilities the document does not enumerate: %v\n"+
			"add them to the `capabilities` description in internal/httpapi/spec/openapi.v0.yaml — "+
			"that paragraph is the vocabulary a client is told to check", missing)
	}
	if extra := diff(documented, served); len(extra) > 0 {
		t.Errorf("the document enumerates capabilities no implemented route contributes: %v", extra)
	}
}

func specParam(t *testing.T, so specOp, name string) map[string]any {
	t.Helper()
	raw, ok := so.op["parameters"].([]any)
	if !ok {
		t.Fatalf("%s %s has no parameters", so.method, so.path)
	}
	for _, p := range raw {
		param, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("%s %s: parameter is %T, want a mapping", so.method, so.path, p)
		}
		if got, _ := param["name"].(string); got == name {
			return param
		}
	}
	t.Fatalf("%s %s: no %q parameter", so.method, so.path, name)
	return nil
}

// TestSpecStatusCodesMatchHandlerTable is the rule that keeps the error
// vocabulary from growing by accident: every documented status+code pair is
// permanent wire surface, so the spec may document exactly what the mapping in
// problem.go can produce, and no more.
//
// The Host-header middleware's 400 invalid_argument is reachable on every
// route and is documented once at the document level rather than per
// operation, so it is absent from both sides here by construction.
func TestSpecStatusCodesMatchHandlerTable(t *testing.T) {
	doc := loadSpec(t)
	ops := specOps(t, doc)

	// There are no 501 stubs left. The check stays because it is the guard
	// against one reappearing silently: 501 is not documented anywhere in this
	// document and `not_implemented` is deliberately absent from the frozen
	// vocabulary in problem.go, so a stub emits a status this test would
	// otherwise forbid. The transitional exemption list that used to sit here
	// was deleted with the last stub, which is what it was written to require.
	for _, rt := range routeTable {
		if !rt.implemented {
			t.Errorf("%s is a 501 stub; v0 has no undocumented statuses left, so a new stub needs an exemption block here that says why", rt.op)
		}
	}

	if len(ops) != len(operationCodes) {
		t.Errorf("spec documents %d operations, the handler table declares %d", len(ops), len(operationCodes))
	}
	for id := range ops {
		if _, ok := operationCodes[id]; !ok {
			t.Errorf("spec operation %q has no entry in the handler table", id)
		}
	}

	for id, codes := range operationCodes {
		so, ok := ops[id]
		if !ok {
			t.Errorf("handler table operation %q is not in the spec", id)
			continue
		}

		wantByStatus := map[int][]Code{}
		for _, c := range codes {
			status := c.Status()
			if status == 0 {
				t.Errorf("%s: code %q has no frozen status", id, c)
				continue
			}
			wantByStatus[status] = append(wantByStatus[status], c)
		}

		gotStatuses := map[int]bool{}
		for status, raw := range mapAt(t, so.op, "responses") {
			code, err := strconv.Atoi(status)
			if err != nil {
				t.Fatalf("%s: response key %q is not a status code", id, status)
			}
			if code >= 200 && code < 300 {
				continue
			}
			gotStatuses[code] = true

			node, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s %d: response is %T, want a mapping", id, code, raw)
			}
			resp := resolveRef(t, doc, node)
			var documented []string
			for _, c := range toStrings(t, resp["x-bd-codes"]) {
				documented = append(documented, c)
				if Code(c).Status() == 0 {
					t.Errorf("%s %d: code %q is not in the frozen vocabulary", id, code, c)
					continue
				}
				if got := Code(c).Status(); got != code {
					t.Errorf("%s: code %q is documented under %d but is frozen to %d", id, c, code, got)
				}
			}
			sort.Strings(documented)
			if want := sortedCodes(wantByStatus[code]); !equalStrings(documented, want) {
				t.Errorf("%s %d: spec documents codes %v, the handler table can emit %v", id, code, documented, want)
			}
		}

		for status := range wantByStatus {
			if !gotStatuses[status] {
				t.Errorf("%s: handler table can emit %d, the spec does not document it", id, status)
			}
		}
		for status := range gotStatuses {
			if _, ok := wantByStatus[status]; !ok {
				t.Errorf("%s: spec documents %d, no mapping row can produce it for this operation", id, status)
			}
		}
	}
}

// TestDefaultsMatchCLIFlags guards every default this document repeats from a
// cobra flag registration. A client swapping a `bd` subprocess for an HTTP call
// gets the same answer only if the two surfaces default the same way, and a
// default is the one piece of the contract nobody passes explicitly — so a
// divergence is invisible until the result sets differ.
//
// The two limits are now one constant each, in internal/workapi, which both
// surfaces read: TestSpecDefaultsMatchSharedConstants pins the document to
// workapi's numbers, and this pins the cobra flag to the same constants BY
// NAME. Asserting the name rather than the value is the stronger half of the
// chain — a flag that went back to a literal would still pass a value
// comparison on the day it was written and drift the day someone edited one
// side. `sort` has no constant to share at all; the flag string IS the source
// of truth, so that half is still a value comparison.
//
// If a flag registration is reworded this fails loudly — re-point the regex,
// and check the values still agree while you are there.
func TestDefaultsMatchCLIFlags(t *testing.T) {
	limitFlag := regexp.MustCompile(`IntP\("limit",\s*"n",\s*([^,]+),`)

	for _, tc := range []struct {
		file string
		want string
	}{
		{"../../cmd/bd/list.go", "workapi.DefaultListLimit"},
		{"../../cmd/bd/ready.go", "workapi.DefaultReadyLimit"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		m := limitFlag.FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no --limit flag registration found; re-point this guard at the CLI's default", tc.file)
		}
		if got := strings.TrimSpace(string(m[1])); got != tc.want {
			t.Errorf("%s registers --limit default %s, want the shared constant %s that this document is pinned to", tc.file, got, tc.want)
		}
	}

	// The ready sort policy. Getting this wrong changes the item SET, not just
	// the order, as soon as the limit truncates: `hybrid` demotes older
	// high-priority work that `priority` surfaces first.
	//
	// Note for anyone tempted to "correct" the spec back to hybrid: the
	// storage layer maps an EMPTY policy to hybrid
	// (internal/storage/sqlbuild/ready.go), but the CLI never sends empty —
	// the flag registers a concrete default — so that fallback is not `bd
	// ready`'s behavior and must not be this parameter's default. A handler
	// that forwards an absent `sort` as "" reintroduces the divergence with
	// the spec still saying the right thing.
	sortFlag := regexp.MustCompile(`StringP\("sort",\s*"s",\s*"([a-z]+)"`)
	src, err := os.ReadFile("../../cmd/bd/ready.go")
	if err != nil {
		t.Fatalf("read cmd/bd/ready.go: %v", err)
	}
	m := sortFlag.FindSubmatch(src)
	if m == nil {
		t.Fatalf("cmd/bd/ready.go: no --sort flag registration found; re-point this guard at the CLI's default")
	}
	cliSort := string(m[1])

	doc := loadSpec(t)
	so, ok := specOps(t, doc)[OpListReadyWork]
	if !ok {
		t.Fatalf("operation %q missing from the spec", OpListReadyWork)
	}
	schema := mapAt(t, specParam(t, so, "sort"), "schema")
	specSort, _ := schema["default"].(string)
	if specSort != cliSort {
		t.Errorf("spec documents sort default %q, `bd ready --sort` registers %q", specSort, cliSort)
	}
	if !slices.Contains(toStrings(t, schema["enum"]), specSort) {
		t.Errorf("sort default %q is not in the documented enum %v", specSort, schema["enum"])
	}
	// The third link in the chain. The two above tie the document to the CLI
	// flag; without this one the HANDLER is free to send a different policy
	// while both of them still read correctly, which is the only place a
	// wrong default actually changes what a client receives.
	if readySortDefault != cliSort {
		t.Errorf("handleReady defaults `sort` to %q, `bd ready --sort` registers %q", readySortDefault, cliSort)
	}
}

// contextResponseAllowlist is the ENTIRE field set of GET /v0/beads/context,
// frozen. The handshake is assembled from the server's own configuration, which
// is exactly the kind of struct that grows a field nobody meant to publish, so
// this list is the gate: adding a member to the response is an edit here, in
// the spec, and in review — never a side effect of something upstream growing.
//
// Deliberately absent, in this and every future version: the workspace's sync
// remote (remote URLs routinely embed credentials) and the database bind
// host/port (advertising it invites clients to bypass this API and dial the
// database directly). Both are named here rather than only in prose so that
// re-adding one costs a test edit.
var contextResponseAllowlist = []string{
	"api_version", "backend", "bd_version", "beads_dir", "capabilities",
	"database", "dolt_mode", "project_id", "repo_root", "schema_version",
}

// TestContextResponseAllowlist pins that field set from both sides: the
// generated Go struct (what the server can marshal) and the document (what
// clients are promised). ContextResponse is not x-go-type-pinned — it is new
// wire surface with no canonical struct behind it — so the bijection test does
// not cover it, and without this the document's own claim that the field set is
// enforced would be aspirational.
func TestContextResponseAllowlist(t *testing.T) {
	want := map[string]bool{}
	for _, name := range contextResponseAllowlist {
		want[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.ContextResponse{}))
	if extra := diff(goFields, want); len(extra) > 0 {
		t.Errorf("generated ContextResponse carries fields that are not on the allowlist: %v\n"+
			"a member of this response is a permanent, deliberate disclosure — add it here only with the review that implies", extra)
	}
	if missing := diff(want, goFields); len(missing) > 0 {
		t.Errorf("allowlisted fields absent from the generated ContextResponse: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ContextResponse")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, want); len(extra) > 0 {
		t.Errorf("the ContextResponse schema documents fields that are not on the allowlist: %v", extra)
	}
	if missing := diff(want, specProps); len(missing) > 0 {
		t.Errorf("allowlisted fields absent from the ContextResponse schema: %v", missing)
	}
}

// TestClaimRequestMembersMatchTheHandler is the same gate for the REQUEST side
// of the claim, where the same unpinned-surface hazard runs the other way.
//
// The handler deliberately does not decode apigen.ClaimRequest: it reads raw
// members so it can name the offending one in a refusal, which is what makes
// the schema's additionalProperties: false enforceable by a client that has
// stopped parsing prose. The price is that its accepted set is a hand-rolled
// copy of the generated struct with nothing tying the two together — so a spec
// revision adding an optional member would leave `make api-check` and every
// other spec test green while the server refused the newly documented member as
// unknown_parameter. Silent drift with all the gates passing is the one failure
// this file exists to prevent.
func TestClaimRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{claimActorMember: true}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.ClaimRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated ClaimRequest declares members the claim handler refuses as unknown: %v\n"+
			"teach claimActor to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the claim handler accepts members ClaimRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ClaimRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the ClaimRequest schema documents members the claim handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the claim handler accepts members the ClaimRequest schema does not document: %v", missing)
	}
}

// TestCloseRequestMembersMatchTheHandler is TestClaimRequestMembersMatchTheHandler
// for the close body, and it exists for the same reason: the handler decodes
// raw members so it can name the offending one, which makes the schema's
// additionalProperties: false enforceable by a client that has stopped parsing
// prose — at the cost of a hand-rolled copy of the member list with nothing
// tying it to the document. A spec revision adding an optional member would
// otherwise leave `make api-check` and every other spec test green while the
// server refused the newly documented member as unknown_parameter.
func TestCloseRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range closeRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.CloseIssueRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated CloseIssueRequest declares members the close handler refuses as unknown: %v\n"+
			"teach closeRequest to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the close handler accepts members CloseIssueRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "CloseIssueRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the CloseIssueRequest schema documents members the close handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the close handler accepts members the CloseIssueRequest schema does not document: %v", missing)
	}
}

// TestBatchCloseRequestMembersMatchTheHandler is the batch create's gate for
// the batch close body, at BOTH of its levels. Same reason as there: the
// handler decodes raw members so it can name the offending one by index, which
// costs a hand-rolled copy of two member lists with nothing tying them to the
// document.
func TestBatchCloseRequestMembersMatchTheHandler(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, tc := range []struct {
		schema   string
		accepted []string
		goType   any
	}{
		{schema: "BatchCloseRequest", accepted: batchCloseRequestMembers, goType: apigen.BatchCloseRequest{}},
		{schema: "BatchCloseItem", accepted: batchCloseItemMembers, goType: apigen.BatchCloseItem{}},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			accepted := map[string]bool{}
			for _, name := range tc.accepted {
				accepted[name] = true
			}
			goFields := jsonTagNames(t, reflect.TypeOf(tc.goType))
			if extra := diff(goFields, accepted); len(extra) > 0 {
				t.Errorf("generated %s declares members the batchClose handler refuses as unknown: %v", tc.schema, extra)
			}
			if missing := diff(accepted, goFields); len(missing) > 0 {
				t.Errorf("the batchClose handler accepts members %s does not declare: %v", tc.schema, missing)
			}
			specProps := schemaProperties(t, doc, mapAt(t, schemas, tc.schema))
			if extra := diff(specProps, accepted); len(extra) > 0 {
				t.Errorf("the %s schema documents members the batchClose handler refuses: %v", tc.schema, extra)
			}
			if missing := diff(accepted, specProps); len(missing) > 0 {
				t.Errorf("the batchClose handler accepts members the %s schema does not document: %v", tc.schema, missing)
			}
		})
	}
}

// TestCloseOutcomeCodesAreExactlyWhatTheProjectionEmits holds the per-item
// vocabulary to the document, which no other gate covers: `code` on an outcome
// lives inside a 200 BODY, so TestSpecStatusCodesMatchHandlerTable — which
// compares problem documents — cannot see it at all.
//
// An item code the projection emits and the document does not name is
// undisclosed wire surface on the one operation whose refusals do not travel as
// problems.
func TestCloseOutcomeCodesAreExactlyWhatTheProjectionEmits(t *testing.T) {
	// The codes closeOutcome can produce, read off the function's own arms.
	emitted := map[string]bool{
		string(CodeNotFound):    true,
		string(CodeNotClosable): true,
		string(CodeInternal):    true,
	}
	for code := range emitted {
		if Code(code).Status() == 0 {
			t.Errorf("outcome code %q is not in the frozen vocabulary", code)
		}
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "CloseOutcome")
	described := mapAt(t, mapAt(t, schema, "properties"), "code")["description"]
	prose, _ := described.(string)
	if prose == "" {
		t.Fatal("CloseOutcome.code carries no description; the item vocabulary is documented nowhere else")
	}
	for code := range emitted {
		// `internal` is the fail-closed arm for a refusal no vocabulary here
		// names, and the document does not enumerate it for the same reason no
		// operation enumerates the generic 500: it means the server is broken,
		// not that the client sent something.
		if code == string(CodeInternal) {
			continue
		}
		if !strings.Contains(prose, "`"+code+"`") {
			t.Errorf("closeOutcome emits %q and CloseOutcome.code does not name it", code)
		}
	}
}

// TestClaimNextRequestMembersMatchTheHandler is the claim's gate for the
// claimNext body. The body is one member, and the point of the test is the
// NEGATIVE half: this operation's filter lives in the query string, so a
// documented body member the handler refuses — or, worse, a filter member added
// to the schema that would have to be a second spelling of the ready
// vocabulary — fails here rather than shipping.
func TestClaimNextRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range claimNextRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.ClaimNextRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated ClaimNextRequest declares members the claimNext handler refuses as unknown: %v", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the claimNext handler accepts members ClaimNextRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ClaimNextRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the ClaimNextRequest schema documents members the claimNext handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the claimNext handler accepts members the ClaimNextRequest schema does not document: %v", missing)
	}
}

// TestClaimNextAdmitsExactlyTheListingsFilters is the guarantee readyFilters
// exists for, asserted at the DOCUMENT level rather than trusted to the shared
// function.
//
// The handler cannot drift — it calls readyFilters itself — but the SPEC can,
// and a document that published a filter the handler never decodes would tell a
// client its request was narrowed when it was not. The two operations must
// admit one set: a claim answering a different question than the listing shows
// would hand an agent work the listing never offered it.
//
// The deliberate differences are the listing's own PAGE knobs, which are not
// filters: they bound or shape what one request returns rather than selecting
// which rows are eligible, so a claim has no use for them.
// issueops.ValidateClaimNextRequest refuses each one by VALUE, and because
// readyFilters does not decode them, the HTTP door refuses them earlier still
// as an unknown parameter. Each is asserted as an absence from claimNext rather
// than merely left off the comparison, because a documented parameter that is
// always a 400 is a trap either way.
//
// `sort` is deliberately NOT on that list. It picks WHICH row comes first, so
// it changes which one a claim takes, and both operations must admit it.
func TestClaimNextAdmitsExactlyTheListingsFilters(t *testing.T) {
	doc := loadSpec(t)
	names := func(path, method string) map[string]bool {
		op := mapAt(t, mapAt(t, mapAt(t, doc, "paths"), path), method)
		params, _ := op["parameters"].([]any)
		out := map[string]bool{}
		for _, raw := range params {
			p, _ := raw.(map[string]any)
			name, _ := p["name"].(string)
			out[name] = true
		}
		return out
	}
	listing := names("/v0/beads/ready", "get")
	claimNext := names("/v0/beads/issues:claimNext", "post")

	pageKnobs := map[string]string{
		"limit": "bounds the page; a claim takes one row however large the pool it scanned",
		"brief": "projects the rows a page returns; a claim refetches its winning row whole, so there is nothing to project",
	}
	for name, why := range pageKnobs {
		if !listing[name] {
			t.Fatalf("the listing no longer publishes `%s`; this test's exception for it is stale (%s)", name, why)
		}
		delete(listing, name)
		if claimNext[name] {
			t.Errorf("claimNext publishes `%s`; it refuses one by value (%s), and a documented parameter that is always a 400 is a trap", name, why)
		}
	}

	if extra := diff(claimNext, listing); len(extra) > 0 {
		t.Errorf("claimNext publishes parameters the ready listing does not: %v\n"+
			"the two must admit one filter vocabulary, or a claim answers a different question than the listing shows", extra)
	}
	if missing := diff(listing, claimNext); len(missing) > 0 {
		t.Errorf("the ready listing publishes parameters claimNext does not: %v\n"+
			"the handler decodes them through readyFilters either way, so the document is understating what it accepts.\n"+
			"If one of these is a page knob rather than a filter, add it to pageKnobs above with its reason", missing)
	}
}

// TestReleaseRequestMembersMatchTheHandler is the close's gate for the release
// body, and it exists for the same reason. It matters a little more here than
// there because two of the three members are GUARDS: a documented guard the
// server refuses as unknown, or an undocumented one it honors, both change who
// is allowed to release whose claim.
func TestReleaseRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range releaseRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.ReleaseIssueRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated ReleaseIssueRequest declares members the release handler refuses as unknown: %v\n"+
			"teach releaseRequest to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the release handler accepts members ReleaseIssueRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ReleaseIssueRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the ReleaseIssueRequest schema documents members the release handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the release handler accepts members the ReleaseIssueRequest schema does not document: %v", missing)
	}
}

// TestSetSettingRequestMembersMatchTheHandler is the release's gate for the
// settings write body.
//
// The member this is really guarding is the one that is NOT there. The key is
// the path's, and a `key` member arriving in the body — documented or honored —
// would give one request two anchors, so both halves of the check matter: the
// handler refuses it as unknown, and the schema must not promise it.
func TestSetSettingRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range setSettingRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.SetSettingRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated SetSettingRequest declares members the settings write refuses as unknown: %v", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the settings write accepts members SetSettingRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "SetSettingRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the SetSettingRequest schema documents members the settings write refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the settings write accepts members the SetSettingRequest schema does not document: %v", missing)
	}

	if accepted["key"] || specProps["key"] {
		t.Error("SetSettingRequest publishes `key`; the anchor is the path parameter and must have one spelling")
	}
}

// TestRemovedSettingPublishesNothingButTheKey pins the removal's response shape
// against the document from the SERVER's side, which the schema alone cannot do:
// `RemovedSetting` is unpinned, so nothing else fails when a member is added to
// the Go type and to the schema together.
//
// Both absences are the contract. `removed` cannot be honest — the storage seam
// discards the affected-row count on every implementation — and `value` would
// publish, on the one settings operation that withholds nothing, exactly the
// credential `GET /v0/beads/config/{key}` redacts.
func TestRemovedSettingPublishesNothingButTheKey(t *testing.T) {
	if got := jsonTagNames(t, reflect.TypeOf(apigen.RemovedSetting{})); len(got) != 1 || !got["key"] {
		t.Errorf("RemovedSetting declares %v, want `key` alone", got)
	}
	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "RemovedSetting")
	if got := schemaProperties(t, doc, schema); len(got) != 1 || !got["key"] {
		t.Errorf("the RemovedSetting schema documents %v, want `key` alone", got)
	}
}

// TestAddCommentRequestMembersMatchTheHandler is the release's gate for the
// comment body.
//
// The member it is really guarding is `author`. Every other body on this surface
// spells its provenance `actor`, so `author` is the one member here a reflex
// edit would "fix" — and the two are not interchangeable: `actor` attributes a
// mutation and `author` IS part of the row, echoed back by every read of the
// thread. Renamed on either side alone, this fails.
func TestAddCommentRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range addCommentRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.AddCommentRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated AddCommentRequest declares members the add-comment handler refuses as unknown: %v", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the add-comment handler accepts members AddCommentRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "AddCommentRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the AddCommentRequest schema documents members the add-comment handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the add-comment handler accepts members the AddCommentRequest schema does not document: %v", missing)
	}

	// The issue is the PATH's, and the schema must not publish a second spelling
	// of it: a body carrying `issue_id` beside a path `{id}` is one request with
	// two anchors and a question about what to do when they disagree.
	if accepted["issue_id"] || specProps["issue_id"] {
		t.Error("AddCommentRequest publishes `issue_id`; the anchor is the path parameter and must have one spelling")
	}
}

// TestUpdateRequestMembersMatchTheHandler is the claim's and the close's gate
// for the update body, at BOTH of its levels.
//
// It matters more here than anywhere else on the surface: `patch` is the widest
// member list published, and the handler's accepted set is a hand-rolled copy
// of it because presence has to be observable. A spec revision adding an
// optional patch member would otherwise leave `make api-check` and every other
// spec test green while the server refused the newly documented member as
// unknown_parameter — and the reverse, a member the handler honors that the
// document never promised, is undisclosed write surface on a mutation.
func TestUpdateRequestMembersMatchTheHandler(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, tc := range []struct {
		schema   string
		accepted []string
		goType   reflect.Type
	}{
		{"UpdateIssueRequest", updateRequestMembers, reflect.TypeOf(apigen.UpdateIssueRequest{})},
		{"IssuePatchBody", issuePatchMembers, reflect.TypeOf(apigen.IssuePatchBody{})},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			accepted := map[string]bool{}
			for _, name := range tc.accepted {
				accepted[name] = true
			}

			goFields := jsonTagNames(t, tc.goType)
			if extra := diff(goFields, accepted); len(extra) > 0 {
				t.Errorf("generated %s declares members the update handler refuses as unknown: %v\n"+
					"teach the handler to honor them, or the document promises a member the server turns down", tc.schema, extra)
			}
			if missing := diff(accepted, goFields); len(missing) > 0 {
				t.Errorf("the update handler accepts members %s does not declare: %v", tc.schema, missing)
			}

			specProps := schemaProperties(t, doc, mapAt(t, schemas, tc.schema))
			if extra := diff(specProps, accepted); len(extra) > 0 {
				t.Errorf("the %s schema documents members the update handler refuses: %v", tc.schema, extra)
			}
			if missing := diff(accepted, specProps); len(missing) > 0 {
				t.Errorf("the update handler accepts members the %s schema does not document: %v", tc.schema, missing)
			}
		})
	}
}

// TestCreateIssueRequestMembersMatchTheHandler is the update's gate for the
// single create, at ALL THREE of its levels.
//
// It carries the weight the batch create's narrow item did not. That operation
// publishes nine members and its handler's copy of the list is small enough to
// eyeball; this one publishes the whole create vocabulary, so a spec revision
// adding an optional member would leave `make api-check` and every other spec
// test green while the server refused the newly documented member as
// unknown_parameter — and the reverse, a member the handler honors that the
// document never promised, is undisclosed write surface on a mutation that can
// mint a row at an id the caller chose.
func TestCreateIssueRequestMembersMatchTheHandler(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, tc := range []struct {
		schema   string
		accepted []string
		goType   reflect.Type
	}{
		{"CreateIssueRequest", createRequestMembers, reflect.TypeOf(apigen.CreateIssueRequest{})},
		{"CreateIssueDependency", createDependencyMembers, reflect.TypeOf(apigen.CreateIssueDependency{})},
		{"CreateIssueWaitsFor", createWaitsForMembers, reflect.TypeOf(apigen.CreateIssueWaitsFor{})},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			accepted := map[string]bool{}
			for _, name := range tc.accepted {
				accepted[name] = true
			}

			goFields := jsonTagNames(t, tc.goType)
			if extra := diff(goFields, accepted); len(extra) > 0 {
				t.Errorf("generated %s declares members the create handler refuses as unknown: %v\n"+
					"teach the handler to honor them, or the document promises a member the server turns down", tc.schema, extra)
			}
			if missing := diff(accepted, goFields); len(missing) > 0 {
				t.Errorf("the create handler accepts members %s does not declare: %v", tc.schema, missing)
			}

			specProps := schemaProperties(t, doc, mapAt(t, schemas, tc.schema))
			if extra := diff(specProps, accepted); len(extra) > 0 {
				t.Errorf("the %s schema documents members the create handler refuses: %v", tc.schema, extra)
			}
			if missing := diff(accepted, specProps); len(missing) > 0 {
				t.Errorf("the create handler accepts members the %s schema does not document: %v", tc.schema, missing)
			}
		})
	}
}

// TestCreateIssueRequestPublishesNoNullableMember pins the create side of the
// rule TestNullablePatchMembersAreExactlyTheDocumentsNullableOnes pins for the
// patch: on this body the nullable set is EMPTY, and the handler refuses an
// explicit null on every member but `metadata`.
//
// The two can drift in the direction that matters. A member marked nullable
// here would promise a clear on a create — a create has nothing to clear, so
// the promise could only be kept by writing a zero value the caller did not ask
// for — while the handler would keep answering 400. Asserting the empty set is
// what makes the four members that ARE nullable on `IssuePatchBody` a statement
// about patching rather than about the field.
func TestCreateIssueRequestPublishesNoNullableMember(t *testing.T) {
	doc := loadSpec(t)
	for _, schema := range []string{"CreateIssueRequest", "CreateIssueDependency", "CreateIssueWaitsFor"} {
		node := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), schema)
		for name, raw := range mapAt(t, node, "properties") {
			prop, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s property %q is %T, want a mapping", schema, name, raw)
			}
			if nullable, _ := prop["nullable"].(bool); nullable {
				t.Errorf("%s.%s is documented nullable and the create handler refuses a null on it; "+
					"a create has nothing to clear, so a nullable member here promises a write nobody asked for", schema, name)
			}
		}
	}
}

// TestReopenRequestMembersMatchTheHandler is the claim's and the close's gate
// for the reopen body. The reopen decodes raw members for the same reason its
// mirror does — so a refusal can name the offending one, which is what makes
// the schema's additionalProperties: false enforceable by a client that has
// stopped parsing prose — and pays the same price: a hand-rolled copy of the
// member list with nothing tying it to the document.
func TestReopenRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range reopenRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.ReopenIssueRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated ReopenIssueRequest declares members the reopen handler refuses as unknown: %v\n"+
			"teach reopenRequest to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the reopen handler accepts members ReopenIssueRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ReopenIssueRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the ReopenIssueRequest schema documents members the reopen handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the reopen handler accepts members the ReopenIssueRequest schema does not document: %v", missing)
	}
}

// TestDeleteRequestMembersMatchTheHandler is the reopen's gate for the delete
// body, and it is new in the slice that gave that body a sixth member.
//
// The gap it closes is the same one every gate in this file closes, on the
// operation where it costs the most. `deleteMembers` is a hand-rolled copy of
// the schema — the handler reads raw members so a refusal can name the
// offending one, which on THIS operation is the difference between orphaning a
// dependent and deleting it — and nothing tied that copy to the document.
// A member documented and not honored is refused as `unknown_parameter` with
// every other gate green; a member honored and not documented is undisclosed
// input on an irreversible write.
func TestDeleteRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range deleteMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.DeleteIssuesRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated DeleteIssuesRequest declares members the delete handler refuses as unknown: %v\n"+
			"teach deleteRequest to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the delete handler accepts members DeleteIssuesRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "DeleteIssuesRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the DeleteIssuesRequest schema documents members the delete handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the delete handler accepts members the DeleteIssuesRequest schema does not document: %v", missing)
	}
}

// TestCompareAndSetMetadataRequestMembersMatchTheHandler is the reopen's gate
// for the compare-and-set body, and it earns its place twice over: this handler
// reads raw members not only to name an offending one but because the OMISSION
// of `expected` and `value` is meaningful, so it can never decode into the
// generated struct and let the compiler hold the two lists together.
func TestCompareAndSetMetadataRequestMembersMatchTheHandler(t *testing.T) {
	accepted := map[string]bool{}
	for _, name := range metadataCASRequestMembers {
		accepted[name] = true
	}

	goFields := jsonTagNames(t, reflect.TypeOf(apigen.CompareAndSetMetadataRequest{}))
	if extra := diff(goFields, accepted); len(extra) > 0 {
		t.Errorf("generated CompareAndSetMetadataRequest declares members the handler refuses as unknown: %v\n"+
			"teach compareAndSetMetadataRequest to honor them, or the document promises a member the server turns down", extra)
	}
	if missing := diff(accepted, goFields); len(missing) > 0 {
		t.Errorf("the compare-and-set handler accepts members CompareAndSetMetadataRequest does not declare: %v", missing)
	}

	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "CompareAndSetMetadataRequest")
	specProps := schemaProperties(t, doc, schema)
	if extra := diff(specProps, accepted); len(extra) > 0 {
		t.Errorf("the CompareAndSetMetadataRequest schema documents members the handler refuses: %v", extra)
	}
	if missing := diff(accepted, specProps); len(missing) > 0 {
		t.Errorf("the compare-and-set handler accepts members the CompareAndSetMetadataRequest schema does not document: %v", missing)
	}
}

// TestDependencyRequestMembersMatchTheHandlers is the same gate for the two
// dependency bodies, and for the add's NESTED level — the update's table-driven
// form because, exactly as there, the body has a level below it.
//
// `edges[i]` is where this matters most on the graph side. It is the only
// member list on the surface checked inside a loop, its refusals are reported
// as `edges[i].member` rather than by bare name, and a member the document grew
// there would be refused per edge with an index attached — a failure that reads
// like a data problem in one row rather than like version skew, which is the
// slowest possible way to discover a spec revision.
func TestDependencyRequestMembersMatchTheHandlers(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, tc := range []struct {
		schema   string
		accepted []string
		goType   reflect.Type
	}{
		{"AddDependenciesRequest", addDependenciesMembers, reflect.TypeOf(apigen.AddDependenciesRequest{})},
		{"DependencyEdge", dependencyEdgeMembers, reflect.TypeOf(apigen.DependencyEdge{})},
		{"RemoveDependencyRequest", removeDependencyMembers, reflect.TypeOf(apigen.RemoveDependencyRequest{})},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			accepted := map[string]bool{}
			for _, name := range tc.accepted {
				accepted[name] = true
			}

			goFields := jsonTagNames(t, tc.goType)
			if extra := diff(goFields, accepted); len(extra) > 0 {
				t.Errorf("generated %s declares members the handler refuses as unknown: %v\n"+
					"teach the handler to honor them, or the document promises a member the server turns down", tc.schema, extra)
			}
			if missing := diff(accepted, goFields); len(missing) > 0 {
				t.Errorf("the handler accepts members %s does not declare: %v", tc.schema, missing)
			}

			specProps := schemaProperties(t, doc, mapAt(t, schemas, tc.schema))
			if extra := diff(specProps, accepted); len(extra) > 0 {
				t.Errorf("the %s schema documents members the handler refuses: %v", tc.schema, extra)
			}
			if missing := diff(accepted, specProps); len(missing) > 0 {
				t.Errorf("the handler accepts members the %s schema does not document: %v", tc.schema, missing)
			}
		})
	}
}

// TestApplyBatchRequestMembersMatchTheHandler is the same gate for the apply
// body, at ALL TEN of its levels — the dependency add's table-driven form,
// because this body nests four deep and every level is a hand-rolled member
// list.
//
// It carries the most weight of any row in this file, for a reason specific to
// this operation. The item is a TAGGED SINGLE-SHAPE OBJECT rather than a schema
// alternation, so the generated type makes all four payload members
// constructible at once and the compiler can see nothing about which one
// belongs with which tag; the handler is the only thing enforcing it, and its
// accepted set at each level is a copy of the document with nothing tying the
// two together. A spec revision adding an optional member at any level would
// otherwise leave `make api-check` and every other spec test green while the
// server refused the newly documented member as unknown_parameter — and the
// reverse, a member the handler honors that the document never promised, is
// undisclosed write surface on the widest mutation this API has.
func TestApplyBatchRequestMembersMatchTheHandler(t *testing.T) {
	doc := loadSpec(t)
	schemas := mapAt(t, mapAt(t, doc, "components"), "schemas")

	for _, tc := range []struct {
		schema   string
		accepted []string
		goType   reflect.Type
	}{
		{"ApplyBatchRequest", applyBatchRequestMembers, reflect.TypeOf(apigen.ApplyBatchRequest{})},
		{"ApplyItem", applyItemMembers, reflect.TypeOf(apigen.ApplyItem{})},
		{"Ref", applyRefMembers, reflect.TypeOf(apigen.Ref{})},
		{"ApplyCreateItem", applyCreateItemMembers, reflect.TypeOf(apigen.ApplyCreateItem{})},
		{"ApplyUpdateItem", applyUpdateItemMembers, reflect.TypeOf(apigen.ApplyUpdateItem{})},
		{"ApplyPatchBody", applyPatchMembers, reflect.TypeOf(apigen.ApplyPatchBody{})},
		{"ApplyLabelPatch", applyLabelPatchMembers, reflect.TypeOf(apigen.ApplyLabelPatch{})},
		{"ApplyMetadataPatch", applyMetadataPatchMembers, reflect.TypeOf(apigen.ApplyMetadataPatch{})},
		{"ApplyCloseItem", applyCloseItemMembers, reflect.TypeOf(apigen.ApplyCloseItem{})},
		{"ApplyDepAddItem", applyDepAddItemMembers, reflect.TypeOf(apigen.ApplyDepAddItem{})},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			accepted := map[string]bool{}
			for _, name := range tc.accepted {
				accepted[name] = true
			}

			goFields := jsonTagNames(t, tc.goType)
			if extra := diff(goFields, accepted); len(extra) > 0 {
				t.Errorf("generated %s declares members the apply handler refuses as unknown: %v\n"+
					"teach the handler to honor them, or the document promises a member the server turns down", tc.schema, extra)
			}
			if missing := diff(accepted, goFields); len(missing) > 0 {
				t.Errorf("the apply handler accepts members %s does not declare: %v", tc.schema, missing)
			}

			specProps := schemaProperties(t, doc, mapAt(t, schemas, tc.schema))
			if extra := diff(specProps, accepted); len(extra) > 0 {
				t.Errorf("the %s schema documents members the apply handler refuses: %v", tc.schema, extra)
			}
			if missing := diff(accepted, specProps); len(missing) > 0 {
				t.Errorf("the apply handler accepts members the %s schema does not document: %v", tc.schema, missing)
			}
		})
	}
}

// TestApplyItemKindsAreExactlyTheDocumentsEnum pins the TAG itself, which no
// other gate can see.
//
// The item's four payload members are checked above, and its `kind` is checked
// as a member name — but the VALUES the tag may take are an enum in the document
// and a map in the handler, and a value in one and not the other is exactly the
// drift this spelling invites. A kind the document promises and the handler
// refuses is an operation a client cannot reach; a kind the handler accepts and
// the document omits is undisclosed surface on the widest mutation here.
//
// It also pins that every tag names a payload member the schema declares, which
// is the whole content of "exactly one must be present and it must be the one
// `kind` names".
func TestApplyItemKindsAreExactlyTheDocumentsEnum(t *testing.T) {
	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ApplyItem")
	documented := map[string]bool{}
	for _, value := range toStrings(t, mapAt(t, mapAt(t, schema, "properties"), "kind")["enum"]) {
		documented[value] = true
	}

	served := map[string]bool{}
	for kind := range applyItemKinds {
		served[string(kind)] = true
	}
	if missing := diff(documented, served); len(missing) > 0 {
		t.Errorf("the document promises item kinds the handler refuses: %v", missing)
	}
	if extra := diff(served, documented); len(extra) > 0 {
		t.Errorf("the handler accepts item kinds the document does not declare: %v", extra)
	}

	// The refusal that spells the vocabulary has to spell the same one.
	spelled := map[string]bool{}
	for _, name := range applyKindNames() {
		spelled[name] = true
	}
	if missing := diff(served, spelled); len(missing) > 0 {
		t.Errorf("the kind vocabulary the refusal prints omits %v", missing)
	}
	if extra := diff(spelled, served); len(extra) > 0 {
		t.Errorf("the refusal prints kinds nothing accepts: %v", extra)
	}

	// Every tag must name a payload member the item schema actually declares,
	// or the handler would demand a member a client has no way to send.
	properties := schemaProperties(t, doc, schema)
	for kind, member := range applyItemKinds {
		if !properties[member] {
			t.Errorf("kind %q names payload member %q, which the ApplyItem schema does not document", kind, member)
		}
	}
}

// TestApplyPatchNullableMembersAreExactlyTheDocumentsNullableOnes is
// TestNullablePatchMembersAreExactlyTheDocumentsNullableOnes for the apply
// operation's own patch, and it exists for the same reason with one addition:
// this body has TWO patch schemas now, and a member marked nullable in one and
// not the other would let the same field be clearable through one operation and
// not the other with nothing saying so.
func TestApplyPatchNullableMembersAreExactlyTheDocumentsNullableOnes(t *testing.T) {
	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "ApplyPatchBody")

	documented := map[string]bool{}
	for name, raw := range mapAt(t, schema, "properties") {
		prop, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("property %q is %T, want a mapping", name, raw)
		}
		if nullable, _ := prop["nullable"].(bool); nullable {
			documented[name] = true
		}
	}

	if missing := diff(documented, applyNullablePatchMembers); len(missing) > 0 {
		t.Errorf("the document marks these apply-patch members nullable and the handler refuses a null on them: %v\n"+
			"a clear the contract promises would be answered with a 400", missing)
	}
	if extra := diff(applyNullablePatchMembers, documented); len(extra) > 0 {
		t.Errorf("the handler clears these apply-patch members on an explicit null and the document does not declare them nullable: %v\n"+
			"that is an unannounced clear on a field nobody agreed could be cleared", extra)
	}
}

// TestNullablePatchMembersAreExactlyTheDocumentsNullableOnes pins the closed set
// on which explicit `null` CLEARS rather than refuses.
//
// The two sides can drift silently and in the worst direction: a member the
// document marks nullable but the handler does not would refuse a clear the
// contract promises, and a member the handler treats as nullable but the
// document does not would let a null through as an unannounced clear on a field
// nobody agreed could be cleared.
func TestNullablePatchMembersAreExactlyTheDocumentsNullableOnes(t *testing.T) {
	doc := loadSpec(t)
	schema := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), "IssuePatchBody")

	documented := map[string]bool{}
	for name, raw := range mapAt(t, schema, "properties") {
		prop, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("property %q is %T, want a mapping", name, raw)
		}
		if nullable, _ := prop["nullable"].(bool); nullable {
			documented[name] = true
		}
	}

	if missing := diff(documented, nullablePatchMembers); len(missing) > 0 {
		t.Errorf("the document marks these patch members nullable and the handler refuses a null on them: %v\n"+
			"a clear the contract promises would be answered with a 400", missing)
	}
	if extra := diff(nullablePatchMembers, documented); len(extra) > 0 {
		t.Errorf("the handler clears these patch members on an explicit null and the document does not declare them nullable: %v\n"+
			"that is an unannounced clear on a field nobody agreed could be cleared", extra)
	}
}

// TestCustomMethodRowsDeclareTheirDocumentedPath bounds the routing exception
// from the side TestSpecRouteParity cannot see. That test compares DECLARED
// spec paths, so it stays green whatever the customMethod field says; the
// dispatcher, meanwhile, chooses a row purely by that field. If the two
// disagree — a row whose specPath ends in `:close` while its customMethod is
// `:reopen` — the documented path would reach the wrong operation with every
// gate passing.
//
// It also pins the invariant the dispatcher depends on: no two rows on one
// pattern may claim the same suffix, and a row that shares a pattern must
// declare one.
func TestCustomMethodRowsDeclareTheirDocumentedPath(t *testing.T) {
	claimed := map[string]string{}
	patterns := map[string]int{}
	for _, rt := range routeTable {
		patterns[rt.method+" "+rt.pattern]++
	}
	for _, rt := range routeTable {
		key := rt.method + " " + rt.pattern
		if rt.customMethod == "" {
			if patterns[key] > 1 {
				t.Errorf("%s shares the pattern %q with another row but declares no customMethod; "+
					"the dispatcher has no way to choose between them", rt.op, rt.pattern)
			}
			continue
		}
		if rt.specPath == "" {
			t.Errorf("%s declares customMethod %q but no specPath; the router cannot spell the documented path, "+
				"so it must be declared", rt.op, rt.customMethod)
			continue
		}
		if !strings.HasSuffix(rt.specPath, rt.customMethod) {
			t.Errorf("%s: documented path %q does not end in the customMethod %q the dispatcher matches on; "+
				"the documented path would reach a different operation",
				rt.op, rt.specPath, rt.customMethod)
		}
		if prev, dup := claimed[key+rt.customMethod]; dup {
			t.Errorf("%s and %s both claim the suffix %q on %q; the dispatcher would hand every such request to the first",
				prev, rt.op, rt.customMethod, rt.pattern)
		}
		claimed[key+rt.customMethod] = rt.op
	}
}

func toStrings(t *testing.T, v any) []string {
	t.Helper()
	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("value is %T, want a sequence", v)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("sequence item is %T, want a string", item)
		}
		out = append(out, s)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSpecSecurityMatchesRouteTable is the auth half of route/spec parity: an
// operation declares the bearer scheme in the document exactly when its route
// row is not auth-exempt. Without this the exemption column and the document's
// `security` blocks are two independent statements about the same thing, and
// the one clients read could quietly become the wrong one.
//
// Authentication is a DEPLOYMENT posture — a server started with no token file
// never emits a 401 — so the spec's job here is to say which operations are
// guarded when it is enabled, not to claim it always is. The scheme is declared
// per operation rather than at the document level for the same reason the
// exemption is a route-table column: healthz is exempt, and a top-level
// `security` block with a per-operation override would state that twice.
func TestSpecSecurityMatchesRouteTable(t *testing.T) {
	doc := loadSpec(t)
	ops := specOps(t, doc)

	schemes := mapAt(t, mapAt(t, doc, "components"), "securitySchemes")
	bearer := mapAt(t, schemes, specBearerScheme)
	if got, _ := bearer["type"].(string); got != "http" {
		t.Errorf("securityScheme %s type = %q, want http", specBearerScheme, got)
	}
	if got, _ := bearer["scheme"].(string); got != "bearer" {
		t.Errorf("securityScheme %s scheme = %q, want bearer", specBearerScheme, got)
	}
	if _, ok := doc["security"]; ok {
		t.Error("the document declares a top-level `security` block; the exemption is per operation, so declaring it globally states the policy twice")
	}

	for _, rt := range routeTable {
		so, ok := ops[rt.op]
		if !ok {
			t.Errorf("route %q is not in the spec", rt.op)
			continue
		}
		declared := specDeclaresBearer(t, so)
		if rt.authExempt && declared {
			t.Errorf("%s is auth-exempt in the route table but declares %s in the spec", rt.op, specBearerScheme)
		}
		if !rt.authExempt && !declared {
			t.Errorf("%s is guarded by the route table but declares no security in the spec", rt.op)
		}

		// And the 401 travels with the declaration, so a generated client of a
		// guarded operation always has the status in its response set.
		_, has401 := mapAt(t, so.op, "responses")["401"]
		if declared != has401 {
			t.Errorf("%s declares security = %v but documents a 401 = %v; the two must move together", rt.op, declared, has401)
		}
	}
}

func specDeclaresBearer(t *testing.T, so specOp) bool {
	t.Helper()
	raw, ok := so.op["security"]
	if !ok {
		return false
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s: security is %T, want a sequence", so.path, raw)
	}
	for _, item := range list {
		req, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s: security item is %T, want a mapping", so.path, item)
		}
		if _, ok := req[specBearerScheme]; ok {
			return true
		}
	}
	return false
}
