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
