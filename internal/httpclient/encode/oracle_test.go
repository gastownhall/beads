// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/oracle_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// The oracle: a real bd serve, bound to loopback, answering from roles that
// record the request the PRODUCTION decoder built and nothing else.
//
// A real server rather than a call into the decoding helpers, because the
// helpers are unexported and — more to the point — because the properties this
// gate needs are the SERVER's, not a function's: the route table has to accept
// the path, the Host policy has to admit the request, and the
// unknown-parameter rule has to run. A parameter this client sends that the
// operation does not publish is a 400 from that rule, and the 400 is half the
// assertion.
//
// The WHOLE role set because httpapi.Listen requires it rather than a partial
// one — a Config missing one role would bind, answer every other route, and fail
// that one with a nil dereference on a live server. Four of them record and the
// rest are interface stubs this surface never dials, and a stub that IS dialed
// by accident panics rather than answering, which is the loud failure a nil
// would not have been.

type oracle struct {
	base string

	mu         sync.Mutex
	ready      issueops.ReadyRequest
	list       issueops.ListRequest
	get        issueops.GetRequest
	count      issueops.ReadyRequest
	query      issueops.QueryRequest
	issueCount issueops.CountRequest
	issueGroup issueops.CountByGroupRequest
	visited    map[string]bool
}

func startOracle(t *testing.T) *oracle {
	t.Helper()
	o := &oracle{visited: map[string]bool{}}

	srv, err := httpapi.Listen(httpapi.Config{
		Addr:   "127.0.0.1:0",
		Stdout: io.Discard,
		Stderr: io.Discard,

		Reader:       recordingReader{o},
		ReadyCounter: recordingCounter{o},
		Querier:      recordingQuerier{o},
		Counter:      recordingIssueCounter{o},

		Claimer:           stubClaimer{},
		ReadyClaimer:      stubReadyClaimer{},
		Releaser:          stubReleaser{},
		Lifecycle:         stubLifecycle{},
		Settings:          stubSettings{},
		Stats:             stubStats{},
		CycleDetector:     stubCycles{},
		EdgeReader:        stubEdges{},
		GraphCounter:      stubGraphCounter{},
		BatchGetter:       stubBatchGetter{},
		Relations:         stubRelations{},
		Commenter:         stubCommenter{},
		BlockingAnnotator: stubBlocking{},
		TreeWalker:        stubTree{},
		Sweeper:           stubSweeper{},
		Deleter:           stubDeleter{},
		BatchCreator:      stubBatchCreator{},
		BatchCloser:       stubBatchCloser{},
		DependencyEditor:  stubDependencyEditor{},
		MetadataCAS:       stubMetadataCAS{},
		BatchApplier:      stubBatchApplier{},
		Memories:          stubMemories{},
	})
	if err != nil {
		t.Fatalf("bind the oracle: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	o.base = "http://" + srv.Addr()
	return o
}

func (o *oracle) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ready, o.list, o.get, o.count, o.query = issueops.ReadyRequest{}, issueops.ListRequest{}, issueops.GetRequest{}, issueops.ReadyRequest{}, issueops.QueryRequest{}
	o.issueCount, o.issueGroup = issueops.CountRequest{}, issueops.CountByGroupRequest{}
	o.visited = map[string]bool{}
}

// dial issues the encoded request against the operation's route and reports the
// status.
func (o *oracle) dial(t *testing.T, op Op, encoded Encoded) int {
	t.Helper()

	var path string
	switch op {
	case OpListReadyWork:
		path = "/v0/beads/ready"
	case OpCountReadyWork:
		path = "/v0/beads/ready:count"
	case OpListIssues:
		path = "/v0/beads/issues"
	case OpQueryIssues:
		path = "/v0/beads/issues:query"
	case OpCountIssues:
		path = "/v0/beads/issues:count"
	case OpGetIssue:
		if len(encoded.PathIDs) != 1 {
			t.Fatalf("getIssue needs exactly one path id, got %v", encoded.PathIDs)
		}
		path = "/v0/beads/issues/" + url.PathEscape(encoded.PathIDs[0])
	default:
		t.Fatalf("no route for operation %q", op)
	}

	target := o.base + path
	if query := encoded.Params.Encode(); query != "" {
		target += "?" + query
	}

	resp, err := http.Get(target) //nolint:gosec // a loopback oracle bound by this test
	if err != nil {
		t.Fatalf("dial %s: %v", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Logf("%s -> %d %s", target, resp.StatusCode, body)
	}
	return resp.StatusCode
}

// captured returns the request the server's decoder built for the operation.
//
// It keys on the SHAPE as well as the operation, because countIssues answers
// two of them on one route: `group_by` picks between the scalar method and the
// bucketed one, and the two roles are handed different request types. Reading
// the scalar one back for a grouped case would compare the caller's dimension
// against a request that has nowhere to hold it.
func (o *oracle) captured(t *testing.T, op Op, shape string) any {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.visited[visitKey(op, shape)] {
		t.Fatalf("the %s/%s handler never reached its role; the request was refused before decoding", op, shape)
	}
	switch {
	case op == OpListReadyWork:
		return o.ready
	case op == OpCountReadyWork:
		return o.count
	case op == OpListIssues:
		return o.list
	case op == OpQueryIssues:
		return o.query
	case op == OpCountIssues && shape == "byGroup":
		return o.issueGroup
	case op == OpCountIssues:
		return o.issueCount
	case op == OpGetIssue:
		return o.get
	}
	t.Fatalf("nothing captured for operation %q shape %q", op, shape)
	return nil
}

// visitKey names the SERVER-side arm a client shape reaches.
//
// A client shape is usually invisible from here — the two bridges map a legacy
// filter onto an operation the server answers with one handler and one role
// method, so `workFilterBridge` and the primary listing are the same arm. The
// count is the exception the parameter carries: `group_by` picks between
// Counter.Count and Counter.CountByGroup, which are two methods handed two
// request types, so that one shape gets a key of its own.
func visitKey(op Op, shape string) string {
	if op == OpCountIssues && shape == "byGroup" {
		return string(op) + "/byGroup"
	}
	return string(op)
}

type recordingReader struct{ o *oracle }

func (r recordingReader) Ready(_ context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	r.o.ready, r.o.visited[visitKey(OpListReadyWork, "")] = req, true
	return issueops.IssuePage{Items: []*issueops.IssueWithCounts{}}, nil
}

func (r recordingReader) List(_ context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	r.o.list, r.o.visited[visitKey(OpListIssues, "")] = req, true
	return issueops.IssuePage{Items: []*issueops.IssueWithCounts{}}, nil
}

func (r recordingReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	r.o.get, r.o.visited[visitKey(OpGetIssue, "")] = req, true
	return &issueops.IssueDetails{}, nil
}

type recordingCounter struct{ o *oracle }

func (c recordingCounter) CountReady(_ context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	c.o.mu.Lock()
	defer c.o.mu.Unlock()
	c.o.count, c.o.visited[visitKey(OpCountReadyWork, "")] = req, true
	return issueops.ReadyCountResult{}, nil
}

type recordingQuerier struct{ o *oracle }

func (q recordingQuerier) Query(_ context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	q.o.mu.Lock()
	defer q.o.mu.Unlock()
	q.o.query, q.o.visited[visitKey(OpQueryIssues, "")] = req, true
	return issueops.IssuePage{Items: []*issueops.IssueWithCounts{}}, nil
}

type recordingIssueCounter struct{ o *oracle }

func (c recordingIssueCounter) Count(_ context.Context, req issueops.CountRequest) (issueops.CountResult, error) {
	c.o.mu.Lock()
	defer c.o.mu.Unlock()
	c.o.issueCount, c.o.visited[visitKey(OpCountIssues, "")] = req, true
	return issueops.CountResult{}, nil
}

func (c recordingIssueCounter) CountByGroup(_ context.Context, req issueops.CountByGroupRequest) (issueops.CountByGroupResult, error) {
	c.o.mu.Lock()
	defer c.o.mu.Unlock()
	c.o.issueGroup, c.o.visited[visitKey(OpCountIssues, "byGroup")] = req, true
	return issueops.CountByGroupResult{Groups: map[string]int{}}, nil
}

// The roles this gate never dials. Each embeds its interface, so the
// Config is complete and a call that should not happen panics on the nil
// embedded value instead of answering something plausible.
type (
	stubClaimer          struct{ issueops.Claimer }
	stubReadyClaimer     struct{ issueops.ReadyClaimer }
	stubReleaser         struct{ issueops.Releaser }
	stubMetadataCAS      struct{ issueops.MetadataCAS }
	stubBatchApplier     struct{ issueops.BatchApplier }
	stubLifecycle        struct{ issueops.Lifecycle }
	stubSettings         struct{ issueops.WorkspaceConfig }
	stubStats            struct{ issueops.StatsReporter }
	stubCycles           struct{ issueops.CycleDetector }
	stubEdges            struct{ issueops.EdgeReader }
	stubGraphCounter     struct{ issueops.GraphCounter }
	stubBatchGetter      struct{ issueops.BatchGetter }
	stubRelations        struct{ issueops.Relations }
	stubCommenter        struct{ issueops.Commenter }
	stubBlocking         struct{ issueops.BlockingAnnotator }
	stubTree             struct{ issueops.TreeWalker }
	stubSweeper          struct{ issueops.Sweeper }
	stubDeleter          struct{ issueops.Deleter }
	stubBatchCreator     struct{ issueops.BatchCreator }
	stubBatchCloser      struct{ issueops.BatchCloser }
	stubDependencyEditor struct{ issueops.DependencyEditor }
	stubMemories         struct{ memoryops.Memories }
)
