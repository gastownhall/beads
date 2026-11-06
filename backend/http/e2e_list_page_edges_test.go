//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// recordedRequest is one request the query-recording proxy forwarded.
type recordedRequest struct {
	method, path string
	query        url.Values
}

func (r recordedRequest) String() string {
	if len(r.query) == 0 {
		return r.method + " " + r.path
	}
	return r.method + " " + r.path + "?" + r.query.Encode()
}

// queryRecorder keeps each request's decoded query beside its method and path,
// so a case can assert WHICH ids a read named rather than only how many reads a
// command made. requestCounter records paths alone, which cannot tell a read of
// the listed ids from a read of every id in the workspace.
type queryRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (q *queryRecorder) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.reqs = nil
}

func (q *queryRecorder) snapshot() []recordedRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]recordedRequest(nil), q.reqs...)
}

// startQueryRecordingProxy is startCountingProxy that also records each
// request's query.
func startQueryRecordingProxy(t *testing.T, upstream string) (string, *queryRecorder) {
	t.Helper()
	target, err := url.Parse("http://" + upstream)
	if err != nil {
		t.Fatal(err)
	}
	rec := &queryRecorder{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query()})
		rec.mu.Unlock()
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), rec
}

// TestE2E_ListTextRenderingsReadOnlyTheListedIssuesEdges pins the dependency
// read behind `bd list`'s text renderings over http — the default tree, --deps,
// a page cut by --limit, the --parent tree and --format — to ONE anchored
// listDependencies request naming exactly the listed issues.
//
// It used to be GetAllDependencyRecords, which over http enumerates every issue
// id in the workspace (all=true, 200 per page) and then reads every id's edges
// in chunks of 100: O(workspace/100) sequential requests for a 20-row page,
// 62 requests and ~6 s against a ~3.8k-issue workspace at ~50 ms a round trip.
// Every rendering reads only edges OWNED by a listed issue, so the listed ids
// are the whole question. The fixture makes the difference visible on a small
// workspace: a closed issue the default listing hides owns a parent-child edge
// onto the listed parent and a blocks edge onto a listed issue, and a read of
// the whole workspace would name it as an anchor.
//
// The ids are not dotted, so the nesting the tree prints can only have come
// from the parent-child edges this read returned: if the read failed, the tree
// would fall back to dotted-id nesting and print every issue as a root.
func TestE2E_ListTextRenderingsReadOnlyTheListedIssuesEdges(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	proxyAddr, rec := startQueryRecordingProxy(t, startE2EServer(t))
	f := newHTTPFixture(t, bin, proxyAddr)

	// One graph apply seeds the whole fixture, so setup costs two subprocesses
	// rather than one per issue and edge. An edge reads "from depends on to".
	plan := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(plan, []byte(`{
		"nodes": [
			{"key": "parent", "title": "tree parent", "type": "epic", "priority": 0},
			{"key": "a", "title": "tree child a", "type": "task", "priority": 1},
			{"key": "b", "title": "tree child b", "type": "task", "priority": 2},
			{"key": "loner", "title": "tree loner", "type": "task", "priority": 3},
			{"key": "hidden", "title": "tree hidden", "type": "task", "priority": 1}
		],
		"edges": [
			{"from_key": "a", "to_key": "parent", "type": "parent-child"},
			{"from_key": "b", "to_key": "parent", "type": "parent-child"},
			{"from_key": "b", "to_key": "a", "type": "blocks"},
			{"from_key": "loner", "to_key": "a", "type": "related"},
			{"from_key": "hidden", "to_key": "parent", "type": "parent-child"},
			{"from_key": "hidden", "to_key": "loner", "type": "blocks"}
		]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var applied struct {
		IDs map[string]string `json:"ids"`
	}
	if out := f.mustRun(nil, "create", "--graph", plan, "--json").stdout; json.Unmarshal([]byte(out), &applied) != nil || len(applied.IDs) != 5 {
		t.Fatalf("bd create --graph --json did not report the five ids: %s", out)
	}
	parent, childA, childB := applied.IDs["parent"], applied.IDs["a"], applied.IDs["b"]
	loner, hidden := applied.IDs["loner"], applied.IDs["hidden"]
	// Closed separately: the batch create cannot carry a closed row's
	// closed_at over the wire. --force because hidden is blocked by loner.
	f.mustRun(nil, "close", hidden, "--reason", "done", "--force")

	// Through the subtest's own t: f.mustRun fails the PARENT test, which a
	// subtest may not do.
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		r := runBD(t, f.bin, f.dir, nil, args...)
		if r.code != 0 {
			t.Fatalf("bd %s failed (exit %d): stdout=%s stderr=%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
		return r.stdout
	}
	// The --json listing is the budget's base: a text rendering of a page may
	// cost that page plus its ONE edge read. Its count does not depend on the
	// page size below the 200-row page, so one measurement serves every page.
	rec.reset()
	run(t, "list", "--json")
	base := len(rec.snapshot())

	listed := []string{parent, childA, childB, loner}
	for _, tc := range []struct {
		name    string
		args    []string
		anchors []string
		// page marks a rendering of the listing's page, held to the --json
		// budget; the --parent tree is a walk, not a page.
		page bool
		want []string
	}{
		{
			name: "tree", args: []string{"list"}, anchors: listed, page: true,
			want: []string{"├── ○ " + childA, "└── ○ " + childB},
		},
		{
			name: "deps all", args: []string{"list", "--deps=all"}, anchors: listed, page: true,
			want: []string{"[depends-on]", "[related]"},
		},
		{
			name: "truncated page", args: []string{"list", "--limit", "2"}, anchors: []string{parent, childA}, page: true,
			want: []string{"└── ○ " + childA},
		},
		{
			name: "parent tree", args: []string{"list", "--parent", parent}, anchors: []string{parent, childA, childB},
			want: []string{"├── ○ " + childA, "└── ○ " + childB},
		},
		{
			name: "format digraph", args: []string{"list", "--format", "digraph"}, anchors: listed, page: true,
			want: []string{childA + " " + parent, childB + " " + parent, childB + " " + childA, loner + " " + childA},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			out := run(t, tc.args...)
			reqs := rec.snapshot()
			t.Logf("bd %s: %d requests: %v", strings.Join(tc.args, " "), len(reqs), reqs)

			var edgeReads []recordedRequest
			for _, r := range reqs {
				if r.path == "/v0/beads/dependencies" {
					edgeReads = append(edgeReads, r)
				}
			}
			if len(edgeReads) != 1 {
				t.Fatalf("bd %s made %d listDependencies requests, want exactly 1 (the listed issues' edges): %v",
					strings.Join(tc.args, " "), len(edgeReads), edgeReads)
			}
			got := slices.Sorted(slices.Values(edgeReads[0].query["issue_id"]))
			want := slices.Sorted(slices.Values(tc.anchors))
			if !slices.Equal(got, want) {
				t.Errorf("bd %s read the edges of %v, want exactly the listed issues %v",
					strings.Join(tc.args, " "), got, want)
			}
			if tc.page && len(reqs) > base+1 {
				t.Errorf("bd %s made %d requests; bd list --json makes %d, and the text rendering may add only its one edge read",
					strings.Join(tc.args, " "), len(reqs), base)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("bd %s output lacks %q:\n%s", strings.Join(tc.args, " "), w, out)
				}
			}
			if strings.Contains(out, hidden) {
				t.Errorf("bd %s printed the closed issue %s, which the default listing hides:\n%s",
					strings.Join(tc.args, " "), hidden, out)
			}
		})
	}
}
