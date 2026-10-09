//go:build cgo

// Served e2e for S6b/S6c/S6d (written fresh for OSS beads). Every case drives
// the real `bd` binary over the wire and, where the answer is a listing or a
// count, against an embedded workspace seeded the same way — parity, not just
// "it exited 0".
package bdhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// remoteRolesFixture is one workspace seeded through the CLI, whichever
// backend it selects. Titles identify rows across the two workspaces, whose
// minted ids differ.
type remoteRolesFixture struct {
	t   *testing.T
	bin string
	dir string
	ids map[string]string // title -> id
}

func (f *remoteRolesFixture) run(env []string, args ...string) bdResult {
	f.t.Helper()
	return runBD(f.t, f.bin, f.dir, env, args...)
}

func (f *remoteRolesFixture) mustRun(env []string, args ...string) bdResult {
	f.t.Helper()
	r := f.run(env, args...)
	if r.code != 0 {
		f.t.Fatalf("bd %s failed (exit %d): stdout=%s stderr=%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

func (f *remoteRolesFixture) create(title string, args ...string) string {
	f.t.Helper()
	r := f.mustRun(nil, append([]string{"create", title, "--json"}, args...)...)
	id := firstJSONField(f.t, r.stdout, "id")
	if id == "" {
		f.t.Fatalf("bd create %q --json produced no id: %s", title, r.stdout)
	}
	f.ids[title] = id
	return id
}

// seed builds the same graph in either workspace:
//
//	epic "parent"
//	  ├─ "child-a"  task, label gc:a, assignee alice, in_progress, meta k=v
//	  ├─ "child-b"  bug,  label gc:b
//	  ├─ "child-c"  task, closed
//	  └─ "child-x"  task, blocked by external:other:cap (unresolvable project)
//	"loner"         task, priority 3, a comment, depends on child-b
//	"tmpl"          task, template
func (f *remoteRolesFixture) seed() {
	f.t.Helper()
	parent := f.create("parent", "-t", "epic")
	a := f.create("child-a", "-t", "task", "-l", "gc:a", "-a", "alice")
	b := f.create("child-b", "-t", "bug", "-l", "gc:b")
	c := f.create("child-c", "-t", "task")
	x := f.create("child-x", "-t", "task", "-p", "0")
	loner := f.create("loner", "-t", "task", "-p", "3")
	f.create("tmpl", "-t", "task")
	for _, child := range []string{a, b, c, x} {
		f.mustRun(nil, "dep", "add", child, parent, "--type", "parent-child")
	}
	f.mustRun(nil, "dep", "add", x, "external:other:cap")
	f.mustRun(nil, "dep", "add", loner, b)
	f.mustRun(nil, "update", a, "-s", "in_progress", "--set-metadata", "k=v")
	f.mustRun(nil, "close", c, "--reason", "done")
	f.mustRun(nil, "comments", "add", loner, "a comment")
	f.mustRun(nil, "update", f.ids["tmpl"], "--set-metadata", "kind=template")
}

func (f *remoteRolesFixture) titlesOf(out string) []string {
	f.t.Helper()
	rows := jsonRows(f.t, out)
	titles := make([]string, 0, len(rows))
	for _, row := range rows {
		if title, ok := row["title"].(string); ok {
			titles = append(titles, title)
		}
	}
	sort.Strings(titles)
	return titles
}

// jsonRows parses a listing: a bare array, or the object envelope
// (`--skip-labels` adds a meta block) whose "issues" member holds it.
func jsonRows(t *testing.T, out string) []map[string]any {
	t.Helper()
	trimmed := []byte(strings.TrimSpace(out))
	var rows []map[string]any
	if err := json.Unmarshal(trimmed, &rows); err == nil {
		return rows
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		t.Fatalf("parse JSON listing: %v\n%s", err, out)
	}
	for _, key := range []string{"issues", "data", "items"} {
		if raw, ok := envelope[key]; ok {
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatalf("parse JSON listing member %q: %v\n%s", key, err, out)
			}
			return rows
		}
	}
	t.Fatalf("JSON listing has no row array: %s", out)
	return nil
}

// expand replaces {title} placeholders with this workspace's id for that title.
func (f *remoteRolesFixture) expand(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		for title, id := range f.ids {
			arg = strings.ReplaceAll(arg, "{"+title+"}", id)
		}
		out[i] = arg
	}
	return out
}

func newEmbeddedFixture(t *testing.T, bin string) *remoteRolesFixture {
	t.Helper()
	f := &remoteRolesFixture{t: t, bin: bin, dir: t.TempDir(), ids: map[string]string{}}
	f.mustRun(nil, "init", "--prefix", "e2e", "--quiet")
	return f
}

func newHTTPFixture(t *testing.T, bin, addr string) *remoteRolesFixture {
	t.Helper()
	f := &remoteRolesFixture{t: t, bin: bin, dir: t.TempDir(), ids: map[string]string{}}
	f.mustRun(nil, "connect", "http://"+addr, "--expect-project-id", e2eProjectID, "--json")
	return f
}

// requestCounter is a loopback reverse proxy in front of the in-process
// server that counts the requests a command makes, by path.
type requestCounter struct {
	mu    sync.Mutex
	paths []string
}

func (c *requestCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = nil
}

func (c *requestCounter) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func startCountingProxy(t *testing.T, upstream string) (string, *requestCounter) {
	t.Helper()
	return startMaskingProxy(t, upstream, nil)
}

// mcServerCapabilities is the capability list the MC production server
// (bd-enterprise 5464806e97) advertises, read off its handshake on
// 2026-10-09 (mc-http-switch DESIGN.md A3). It notably includes
// policy.external_dependencies and lacks every newer token.
var mcServerCapabilities = []string{
	"config.get", "config.list", "config.set", "config.unset",
	"dependencies.add", "dependencies.blocking", "dependencies.count", "dependencies.cycles",
	"dependencies.list", "dependencies.remove", "dependencies.tree",
	"events.list", "events.watch",
	"issues.addComment", "issues.batchApply", "issues.batchClose", "issues.batchCreate",
	"issues.casMetadata", "issues.claim", "issues.claimNext", "issues.close", "issues.count",
	"issues.create", "issues.delete", "issues.get", "issues.list", "issues.list.sort",
	"issues.query", "issues.related", "issues.release", "issues.reopen", "issues.sweep", "issues.update",
	"memories.forget", "memories.get", "memories.list", "memories.remember",
	"policy.external_dependencies", "project.enforce", "ready.count", "ready.list", "stats.get",
}

// mcContextMask rewrites the handshake into the MC server's shape: no
// wire_revision member and exactly its capability list.
func mcContextMask(body map[string]any) {
	delete(body, "wire_revision")
	caps := make([]any, len(mcServerCapabilities))
	for i, c := range mcServerCapabilities {
		caps[i] = c
	}
	body["capabilities"] = caps
}

// startMaskingProxy is startCountingProxy that also rewrites every handshake
// (GET /v0/beads/context) body through mask, when one is given.
func startMaskingProxy(t *testing.T, upstream string, mask func(map[string]any)) (string, *requestCounter) {
	t.Helper()
	target, err := url.Parse("http://" + upstream)
	if err != nil {
		t.Fatal(err)
	}
	counter := &requestCounter{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	if mask != nil {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if resp.Request.URL.Path != "/v0/beads/context" || resp.StatusCode != http.StatusOK {
				return nil
			}
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				return err
			}
			mask(body)
			if raw, err = json.Marshal(body); err != nil {
				return err
			}
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			resp.ContentLength = int64(len(raw))
			resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
			return nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.mu.Lock()
		counter.paths = append(counter.paths, r.Method+" "+r.URL.Path)
		counter.mu.Unlock()
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), counter
}

// TestE2E_ListWithGCFiltersMatchesEmbedded is S6c's list case: `bd list` used
// to refuse over http (the external-deps decorator rebuilt the reader from the
// stubbed SearchIssuesWithCounts). Every list shape gc's BdStore emits, plus
// `bd children`, must answer the same rows as an embedded workspace.
func TestE2E_ListWithGCFiltersMatchesEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	remote := newHTTPFixture(t, bin, startE2EServer(t))
	local := newEmbeddedFixture(t, bin)
	remote.seed()
	local.seed()

	base := []string{"list", "--json", "--include-infra", "--include-gates"}
	shapes := [][]string{
		append(append([]string{}, base...), "--limit", "50"),
		append(append([]string{}, base...), "--limit", "0"),
		append(append([]string{}, base...), "--limit", "2"),
		append(append([]string{}, base...), "--limit", "50", "--all"),
		append(append([]string{}, base...), "--limit", "50", "--label=gc:a"),
		append(append([]string{}, base...), "--limit", "50", "--assignee=alice"),
		append(append([]string{}, base...), "--limit", "50", "--status=in_progress"),
		append(append([]string{}, base...), "--limit", "50", "--status=closed", "--all"),
		append(append([]string{}, base...), "--limit", "50", "--type=bug"),
		append(append([]string{}, base...), "--limit", "50", "--parent", "{parent}"),
		append(append([]string{}, base...), "--limit", "50", "--parent", "{parent}", "--all"),
		append(append([]string{}, base...), "--limit", "50", "--metadata-field", "k=v"),
		append(append([]string{}, base...), "--limit", "50", "--metadata-field", "k=v", "--status=in_progress", "--assignee=alice", "--label=gc:a", "--type=task", "--parent", "{parent}"),
		append(append([]string{}, base...), "--limit", "50", "--include-templates"),
		append(append([]string{}, base...), "--limit", "50", "--skip-labels"),
		append(append([]string{}, base...), "--limit", "50", "--created-before", "2999-01-01T00:00:00Z"),
		{"list", "--json"},
		{"children", "{parent}", "--json"},
	}
	for _, shape := range shapes {
		name := strings.Join(shape, " ")
		r := remote.run(nil, remote.expand(shape)...)
		if r.code != 0 {
			t.Errorf("http: bd %s failed (exit %d): stderr=%s", name, r.code, r.stderr)
			continue
		}
		l := local.mustRun(nil, local.expand(shape)...)
		got, want := remote.titlesOf(r.stdout), local.titlesOf(l.stdout)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("bd %s: http rows %v, embedded rows %v", name, got, want)
		}
	}
}

// TestE2E_ShowJSONCountsMatchEmbedded is S6c's count case: over http the
// decorator's store-backed reader hydrated `bd show --json`'s counts from
// stubbed reads and printed zeros without an error.
func TestE2E_ShowJSONCountsMatchEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	remote := newHTTPFixture(t, bin, startE2EServer(t))
	local := newEmbeddedFixture(t, bin)
	remote.seed()
	local.seed()

	type counts struct{ deps, dependents, comments float64 }
	read := func(f *remoteRolesFixture, title string) counts {
		r := f.mustRun(nil, "show", f.ids[title], "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("parse bd show --json for %s: %v\n%s", title, err, r.stdout)
		}
		num := func(k string) float64 { v, _ := rows[0][k].(float64); return v }
		return counts{num("dependency_count"), num("dependent_count"), num("comment_count")}
	}
	nonZero := false
	for _, title := range []string{"parent", "child-b", "child-x", "loner"} {
		got, want := read(remote, title), read(local, title)
		if got != want {
			t.Errorf("bd show %s --json counts: http %+v, embedded %+v", title, got, want)
		}
		if want != (counts{}) {
			nonZero = true
		}
	}
	if !nonZero {
		t.Fatal("fixture produced no non-zero count; the parity check proves nothing")
	}
}

// TestE2E_DefaultDepTreeOverHTTP is S6c's tree case: the default (down) `bd
// dep tree` used to refuse (GetDependencyTree). It must render the same nodes
// as embedded, including the synthetic external leaf the client-side policy
// hangs off a node with an external dependency.
func TestE2E_DefaultDepTreeOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	remote := newHTTPFixture(t, bin, startE2EServer(t))
	local := newEmbeddedFixture(t, bin)
	remote.seed()
	local.seed()

	for _, title := range []string{"child-x", "loner"} {
		r := remote.run(nil, "dep", "tree", remote.ids[title])
		if r.code != 0 {
			t.Fatalf("http: bd dep tree %s failed (exit %d): stderr=%s", title, r.code, r.stderr)
		}
		l := local.mustRun(nil, "dep", "tree", local.ids[title])
		normalize := func(out string, f *remoteRolesFixture) string {
			for t, id := range f.ids {
				out = strings.ReplaceAll(out, id, "<"+t+">")
			}
			return out
		}
		if got, want := normalize(r.stdout, remote), normalize(l.stdout, local); got != want {
			t.Errorf("bd dep tree %s:\nhttp:\n%s\nembedded:\n%s", title, got, want)
		}
	}
	tree := remote.mustRun(nil, "dep", "tree", remote.ids["child-x"])
	if !strings.Contains(tree.stdout, "cap (external)") {
		t.Errorf("http dep tree of child-x lacks the external leaf: %s", tree.stdout)
	}
	if !strings.Contains(tree.stdout, remote.ids["parent"]) {
		t.Errorf("http dep tree of child-x lacks its parent: %s", tree.stdout)
	}
}

// TestE2E_ExternalDependencyBlockingStillEnforced pins that the passthrough
// kept the client-side policy against a server that does not advertise
// policy.external_dependencies: the externally blocked child-x (priority 0,
// so it sorts first) never reaches ready, a direct claim, an unforced close,
// or `bd ready --claim`; `bd list` still marks it blocked.
func TestE2E_ExternalDependencyBlockingStillEnforced(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	f := newHTTPFixture(t, bin, startE2EServer(t))
	f.seed()
	x := f.ids["child-x"]

	ready := f.mustRun(nil, "ready", "--json")
	if jsonIDSet(t, ready.stdout)[x] {
		t.Errorf("bd ready listed the externally blocked %s: %s", x, ready.stdout)
	}
	readyOne := f.mustRun(nil, "ready", "--json", "--limit", "1")
	if ids := jsonIDSet(t, readyOne.stdout); len(ids) != 1 || ids[x] {
		t.Errorf("bd ready --limit 1 = %v; want exactly one row and not %s (the widened window must still fill the page)", ids, x)
	}

	if r := f.run(nil, "update", x, "--claim", "--json"); r.code == 0 || !strings.Contains(r.stderr, "external:other:cap") {
		t.Errorf("bd update --claim of the externally blocked %s: exit %d, stderr=%s; want a refusal naming the blocker", x, r.code, r.stderr)
	}
	if r := f.run(nil, "close", x, "--json"); r.code == 0 || !strings.Contains(r.stderr, "external:other:cap") {
		t.Errorf("bd close of the externally blocked %s: exit %d, stderr=%s; want a refusal naming the blocker", x, r.code, r.stderr)
	}
	if r := f.run([]string{"CLAUDE_SESSION_ID=sess-x"}, "update", x, "-s", "closed", "--json"); r.code == 0 || !strings.Contains(r.stderr, "external:other:cap") {
		t.Errorf("bd update -s closed of the externally blocked %s: exit %d, stderr=%s; want a refusal naming the blocker", x, r.code, r.stderr)
	}

	claimed := f.mustRun(nil, "ready", "--claim", "--json")
	if id := firstJSONField(t, claimed.stdout, "id"); id == "" || id == x {
		t.Errorf("bd ready --claim claimed %q; want an unblocked issue, never %s: %s", id, x, claimed.stdout)
	}

	list := f.mustRun(nil, "list", "--json", "--limit", "50")
	for _, row := range jsonRows(t, list.stdout) {
		if row["id"] == x && row["status"] == "closed" {
			t.Errorf("child-x was closed despite the refusals: %v", row)
		}
	}

	if r := f.run(nil, "show", x, "--json"); r.code != 0 {
		t.Fatalf("bd show %s failed: %s", x, r.stderr)
	}
	if r := f.mustRun(nil, "close", x, "--force", "--json"); r.code != 0 {
		t.Fatalf("bd close --force %s failed: %s", x, r.stderr)
	}
}

// TestE2E_CloseAndReopenUnderClaudeSession is S6d: under Claude Code every bd
// process carries CLAUDE_SESSION_ID, and over http `bd update -s closed`
// refused (closed_by_session has no wire member) while `bd reopen` always
// refused (its fixed Provenance label has none either). Both must work, and
// `bd close` must still record the session its wire operation does carry.
func TestE2E_CloseAndReopenUnderClaudeSession(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	f := newHTTPFixture(t, bin, startE2EServer(t))
	session := []string{"CLAUDE_SESSION_ID=sess-s6d"}
	id := f.create("closable", "-t", "task")

	status := func() map[string]any {
		r := f.mustRun(nil, "show", id, "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("parse bd show: %v\n%s", err, r.stdout)
		}
		return rows[0]
	}

	// The MC hot shape: metadata and status in one update, session ambient.
	f.mustRun(session, "update", id, "--set-metadata", "phase=done", "--status", "closed", "--json")
	if row := status(); row["status"] != "closed" {
		t.Fatalf("after update -s closed under CLAUDE_SESSION_ID: %v", row)
	}
	reopen := f.mustRun(session, "reopen", id, "--json")
	if !strings.Contains(reopen.stdout, id) {
		t.Errorf("bd reopen --json did not report %s: %s", id, reopen.stdout)
	}
	if row := status(); row["status"] != "open" {
		t.Fatalf("after reopen: %v", row)
	}

	// An explicit --session is honored too, and says it was not recorded.
	explicit := f.mustRun(nil, "update", id, "-s", "closed", "--session", "typed-session", "--json")
	if !strings.Contains(explicit.stderr, "--session is not recorded") {
		t.Errorf("explicit --session over http gave no notice: stderr=%s", explicit.stderr)
	}
	if row := status(); row["status"] != "closed" {
		t.Fatalf("after update -s closed --session: %v", row)
	}
	f.mustRun(nil, "reopen", id, "--reason", "again", "--json")

	// bd close carries the session on its own wire member.
	f.mustRun(session, "close", id, "--json")
	row := status()
	if row["status"] != "closed" || row["closed_by_session"] != "sess-s6d" {
		t.Errorf("after bd close under CLAUDE_SESSION_ID: status=%v closed_by_session=%v; want closed/sess-s6d", row["status"], row["closed_by_session"])
	}
	f.mustRun(session, "reopen", id, "--json")
	if row := status(); row["status"] != "open" {
		t.Errorf("after the second reopen: %v", row)
	}
}

// TestE2E_ShowMissingIDIsNotFoundInOneLookup is S6b: `bd show <missing>`
// made five round trips and then reported "SearchIssueIDs not supported". It
// must classify as not found — the same exit and stdout as embedded — after a
// single issue lookup.
func TestE2E_ShowMissingIDIsNotFoundInOneLookup(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	proxyAddr, counter := startCountingProxy(t, startE2EServer(t))
	remote := newHTTPFixture(t, bin, proxyAddr)
	local := newEmbeddedFixture(t, bin)
	remote.create("present")

	for _, missing := range []string{"e2e-nope", "nope"} {
		counter.reset()
		r := remote.run(nil, "show", missing, "--json")
		paths := counter.snapshot()
		l := local.run(nil, "show", missing, "--json")
		if r.code != l.code || r.code == 0 {
			t.Errorf("bd show %s: http exit %d, embedded exit %d; want the same non-zero not-found exit", missing, r.code, l.code)
		}
		if strings.TrimSpace(r.stdout) != strings.TrimSpace(l.stdout) {
			t.Errorf("bd show %s --json stdout differs:\nhttp: %s\nembedded: %s", missing, r.stdout, l.stdout)
		}
		if !strings.Contains(r.stderr, "Issue "+missing+" not found") || strings.Contains(r.stderr, "not supported") {
			t.Errorf("bd show %s over http is not classified as not found: stderr=%s", missing, r.stderr)
		}
		lookups := 0
		for _, p := range paths {
			if strings.HasPrefix(p, "GET /v0/beads/issues/") {
				lookups++
			}
		}
		// A prefixed id is one exact lookup and nothing else: no prefix
		// vocabulary, no repeated probe, no settings read for contributor
		// auto-routing (a remote workspace configures that locally). A bare
		// hash also reads the prefix vocabulary once (behind the handshake
		// that read's capability check) and tries its prefixed spelling.
		wantLookups, maxTotal := 1, 1
		if !strings.Contains(missing, "-") {
			wantLookups, maxTotal = 2, 4
		}
		if lookups != wantLookups {
			t.Errorf("bd show %s made %d issue lookups, want %d: %v", missing, lookups, wantLookups, paths)
		}
		if len(paths) > maxTotal {
			t.Errorf("bd show %s made %d requests in all, want at most %d: %v", missing, len(paths), maxTotal, paths)
		}
	}

	// A present id still resolves, abbreviation-free.
	if r := remote.run(nil, "show", remote.ids["present"], "--json"); r.code != 0 {
		t.Errorf("bd show of a present id failed over http: %s", r.stderr)
	}
}

// TestE2E_PingOverHTTP: `bd ping` ran a one-row SearchIssues, which the http
// backend refuses; it now reads the authenticated server context.
func TestE2E_PingOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	proxyAddr, counter := startCountingProxy(t, startE2EServer(t))
	f := newHTTPFixture(t, bin, proxyAddr)

	counter.reset()
	r := f.mustRun(nil, "ping", "--json")
	if status := firstJSONField(t, r.stdout, "status"); status != "ok" {
		t.Errorf("bd ping --json status = %q: %s", status, r.stdout)
	}
	sawContext := false
	for _, p := range counter.snapshot() {
		if p == "GET /v0/beads/context" {
			sawContext = true
		}
	}
	if !sawContext {
		t.Errorf("bd ping made no context request: %v", counter.snapshot())
	}

	// A dead server is a failed ping, not a stale success.
	sidecar := filepath.Join(f.dir, ".beads", "http_target.json")
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	dead := strings.ReplaceAll(string(data), proxyAddr, "127.0.0.1:1")
	if err := os.WriteFile(sidecar, []byte(dead), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.run(nil, "ping", "--json"); r.code == 0 {
		t.Errorf("bd ping against a dead server exited 0: %s", r.stdout)
	}
}

// TestE2E_ClaimOfAbsentIDIsGCNotFound is S24-T: gc's hook claims through
// `bd update <id> --claim --json` on the work store and, on a not-found,
// escalates to its own wisp store (gascity BdStore.Claim -> isBdNotFound).
// Over http an absent id must therefore answer a not-found gc's matcher
// accepts — never a refusal — in at most two round trips, against both the
// current server and the MC server's handshake shape.
func TestE2E_ClaimOfAbsentIDIsGCNotFound(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	for _, tc := range []struct {
		name string
		mask func(map[string]any)
	}{
		{"current server", nil},
		{"MC server mask", mcContextMask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyAddr, counter := startMaskingProxy(t, addr, tc.mask)
			f := newHTTPFixture(t, bin, proxyAddr)
			present := f.create("claimable")

			counter.reset()
			r := f.run([]string{"BEADS_ACTOR=gc-hook"}, "update", "e2e-absent9", "--claim", "--json")
			paths := counter.snapshot()
			if r.code == 0 {
				t.Fatalf("bd update <absent> --claim exited 0: %s", r.stdout)
			}
			// gascity internal/beads/bdstore.go isBdNotFound, verbatim. The
			// error gc classifies carries stderr (and stdout's --json
			// envelope when stderr is empty), so stderr must match alone.
			isBdNotFound := func(msg string) bool {
				msg = strings.ToLower(msg)
				return strings.Contains(msg, "not found") ||
					strings.Contains(msg, "no issue found") ||
					strings.Contains(msg, "no issues found")
			}
			if !isBdNotFound(r.stderr) || (r.stdout != "" && !isBdNotFound(r.stdout)) {
				t.Errorf("absent-id claim is not a gc not-found: stdout=%s stderr=%s", r.stdout, r.stderr)
			}
			for _, bad := range []string{"not supported", "already claimed", "already assigned"} {
				if strings.Contains(strings.ToLower(r.stderr+r.stdout), bad) {
					t.Errorf("absent-id claim output mentions %q: stdout=%s stderr=%s", bad, r.stdout, r.stderr)
				}
			}
			if len(paths) > 2 {
				t.Errorf("absent-id claim made %d requests, want at most 2: %v", len(paths), paths)
			}

			// A present id still claims through the same shape.
			ok := f.run([]string{"BEADS_ACTOR=gc-hook"}, "update", present, "--claim", "--json")
			if ok.code != 0 || firstJSONField(t, ok.stdout, "assignee") != "gc-hook" {
				t.Errorf("claim of a present id: exit %d stdout=%s stderr=%s", ok.code, ok.stdout, ok.stderr)
			}
		})
	}
}

// TestE2E_AdvertisedExternalPolicyIsPassedThrough is the other half of the
// design 3.6 rule, against the MC server's handshake shape: a server that
// advertises policy.external_dependencies has applied it, so the client reads
// no dependency edges for it on list, ready, claim, show or close.
func TestE2E_AdvertisedExternalPolicyIsPassedThrough(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	proxyAddr, counter := startMaskingProxy(t, startE2EServer(t), mcContextMask)
	f := newHTTPFixture(t, bin, proxyAddr)
	id := f.create("plain")
	other := f.create("other")
	f.mustRun(nil, "dep", "add", id, "external:other:cap")

	counter.reset()
	f.mustRun(nil, "list", "--json", "--limit", "50")
	f.mustRun(nil, "ready", "--json", "--limit", "10")
	f.mustRun(nil, "show", id, "--json")
	f.mustRun(nil, "update", other, "--claim", "--json")
	// claim-next is the server's ONE atomic operation, not a client-side
	// read-then-claim loop.
	f.mustRun(nil, "ready", "--claim", "--json")
	var claimNexts, byID int
	for _, p := range counter.snapshot() {
		switch {
		case p == "POST /v0/beads/issues:claimNext":
			claimNexts++
		case strings.HasPrefix(p, "POST /v0/beads/issues/") && strings.HasSuffix(p, ":claim"):
			byID++
		}
	}
	if claimNexts != 1 || byID != 0 {
		t.Errorf("claimNext requests = %d, by-id claims = %d; want one atomic claim-next and no by-id claim: %v", claimNexts, byID, counter.snapshot())
	}
	f.mustRun(nil, "close", other, "--json")
	for _, p := range counter.snapshot() {
		if strings.HasPrefix(p, "GET /v0/beads/dependencies") {
			t.Errorf("client-side policy ran against a server advertising policy.external_dependencies: %s in %v", p, counter.snapshot())
			break
		}
	}
}
