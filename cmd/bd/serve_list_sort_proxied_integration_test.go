//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// walkPageSize is the page an HTTP client walking this operation actually uses.
// It is the value the enterprise httpstore client pages with, and it is the
// denominator in the cost this test exists to measure: a listing of N rows in
// `bd list`'s default order used to cost ceil(N/walkPageSize) requests because
// the order could not be asked for.
const walkPageSize = 200

// listSortFixtureRows is large enough that the old cost is unambiguously
// multi-page — seven walk requests at walkPageSize, measured — and small enough
// that a single `bd import` seeds it in one subprocess call.
const listSortFixtureRows = 1400

// listPage is one decoded page of GET /v0/beads/issues.
type listPage struct {
	Items      []map[string]any `json:"items"`
	HasMore    bool             `json:"has_more"`
	NextCursor *string          `json:"next_cursor"`
}

// countingPage fetches one page and reports it, so a caller can count the
// requests a strategy costs rather than assert on a number it computed itself.
func (sp *serveProcess) countingPage(t *testing.T, path string, requests *int) listPage {
	t.Helper()
	*requests++
	resp, err := sp.client.Get(sp.url(path))
	if err != nil {
		t.Fatalf("GET %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s\nstderr:\n%s", path, resp.StatusCode, raw, sp.stderr.String())
	}
	var page listPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode %s: %v\nraw: %s", path, err, raw)
	}
	return page
}

// itemPriorities projects the priority column, for asserting an order rather
// than a membership.
func itemPriorities(items []map[string]any) []int {
	out := make([]int, 0, len(items))
	for _, item := range items {
		p, _ := item["priority"].(float64)
		out = append(out, int(p))
	}
	return out
}

// TestProxiedServerListSortRetiresTheWalk is the acceptance measurement for
// serving `bd list`'s default order, taken against real Dolt through a real
// `bd serve` subprocess and a real `bd list`.
//
// THE COST IT MEASURES. Before `sort`, an HTTP client that wanted `bd list`'s
// ordering had exactly one option: page the whole result set in the served
// created order and re-sort it locally, because the cursor was a keyset
// position in that order and nothing else could be asked for. That is
// ceil(N/walkPageSize) requests plus a client-side comparator, and it is the
// only cost on this surface that gets WORSE as a project grows. This test runs
// both strategies over the same 1400-row fixture and compares the request
// counts and the delivered sequences: seven requests against one.
//
// WHY THE TWO SEQUENCES MUST BE IDENTICAL, and why that is the load-bearing
// claim rather than the request count: `bd list --sort priority` is a pure
// priority comparator applied as a STABLE sort over the walked created order,
// which yields priority ASC, then created DESC, then id ASC within ties — the
// same sequence as `bd list` with no flags, and the same sequence the served
// `sort=priority` renders in SQL. If those three ever diverge, the parameter is
// not retiring the walk, it is answering a different question faster. So the
// oracle here is `bd list --json` itself: the CLI is the reference, and both
// HTTP strategies are checked against it.
//
// THE FIXTURE IS BUILT TO BREAK A NAIVE IMPLEMENTATION. Priorities repeat
// across the whole range, and timestamps repeat WITHIN a priority in runs of
// four, so equal (priority, created_at) keys straddle every page boundary a
// walk can land on. A two-part position, or a predicate whose priority-equal
// arm lost its created_at bound, drops or repeats rows here; both look like a
// successful 200 and neither changes the row COUNT enough to notice by eye,
// which is why the assertion is the full sequence.
func TestProxiedServerListSortRetiresTheWalk(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "lstsort")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
	defer sp.shutdown(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issues := make([]*types.Issue, 0, listSortFixtureRows)
	for i := range listSortFixtureRows {
		// Priority cycles over the whole 0..4 range and the instant advances
		// only every fourth row, so each (priority, created_at) key names a run
		// of rows that a 200-row page cannot be assumed to contain whole.
		at := base.Add(time.Duration(i/4) * time.Second)
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("%s-%04d", p.prefix, i),
			Title:     fmt.Sprintf("row %04d", i),
			Status:    types.StatusOpen,
			Priority:  i % 5,
			IssueType: types.TypeTask,
			CreatedAt: at,
			UpdatedAt: at,
		})
	}
	if _, stderr, err := bdProxiedImportWithInput(t, bd, p.dir, importFixtureJSONL(t, issues), "-"); err != nil {
		t.Fatalf("seed %d issues: %v\n%s", listSortFixtureRows, err, stderr)
	}

	// THE REFERENCE. `bd list` with no sort flag, unlimited: the order this
	// whole exercise exists to reproduce over HTTP.
	reference := cliItems(t, bd, p.dir, "list", "--json", "--limit", "0")
	if len(reference) != listSortFixtureRows {
		t.Fatalf("`bd list --limit 0` returned %d rows, want the %d seeded — the fixture did not land, so every comparison below would be vacuous",
			len(reference), listSortFixtureRows)
	}
	// The reference has to actually be priority-first, or it is not the order
	// under test. A fixture whose priorities happened to be uniform would make
	// every sequence comparison below pass against a created-order server.
	if priorities := itemPriorities(reference); !sort.IntsAreSorted(priorities) {
		t.Fatalf("`bd list`'s default order is not priority-first in this fixture; the oracle is wrong, not the server")
	} else if priorities[0] == priorities[len(priorities)-1] {
		t.Fatal("every seeded row has the same priority, so a created-order answer would be indistinguishable from a priority-order one")
	}

	// THE OLD STRATEGY: page the created order to exhaustion, then apply the
	// client-side comparator. Spelled out here in full rather than described,
	// because what is being measured is its cost against the alternative's.
	oldRequests := 0
	var walked []map[string]any
	path := fmt.Sprintf("/v0/beads/issues?limit=%d", walkPageSize)
	for {
		page := sp.countingPage(t, path, &oldRequests)
		walked = append(walked, page.Items...)
		if !page.HasMore {
			break
		}
		if page.NextCursor == nil {
			t.Fatal("has_more is true but next_cursor is absent; the walk cannot continue")
		}
		path = fmt.Sprintf("/v0/beads/issues?limit=%d&cursor=%s", walkPageSize, *page.NextCursor)
		if oldRequests > listSortFixtureRows {
			t.Fatal("the created-order walk did not terminate")
		}
	}
	if len(walked) != listSortFixtureRows {
		t.Fatalf("the created-order walk delivered %d rows, want %d", len(walked), listSortFixtureRows)
	}
	// STABLE, and by priority ALONE. That is the whole client-side comparator:
	// stability is what preserves the walked created order inside a priority
	// tie, and it is why one served order answers both `bd list` and
	// `bd list --sort priority`.
	sort.SliceStable(walked, func(i, j int) bool {
		a, _ := walked[i]["priority"].(float64)
		b, _ := walked[j]["priority"].(float64)
		return a < b
	})

	// THE NEW STRATEGY: ask for the order.
	newRequests := 0
	served := sp.countingPage(t, "/v0/beads/issues?sort=priority&limit=0", &newRequests)
	if served.HasMore {
		t.Error("an unlimited page reported has_more; the walk is not actually retired")
	}

	// ceil(N/pageSize), NOT 1+ceil(N/pageSize). The extra trailing request the
	// arithmetic invites is not spent: this server over-fetches one probe row
	// per page to decide `has_more`, so the seventh full page already reports
	// has_more:false and the walk stops there instead of asking for an eighth,
	// empty one. The number here is the MEASURED cost, and it was wrong in this
	// test before the measurement corrected it — a walk cost asserted from a
	// formula rather than from a run is exactly the kind of number that gets
	// quoted for years.
	wantRequests := (listSortFixtureRows + walkPageSize - 1) / walkPageSize
	if oldRequests != wantRequests {
		t.Errorf("the created-order walk cost %d requests, want the %d this measurement is calibrated against (ceil(%d/%d))",
			oldRequests, wantRequests, listSortFixtureRows, walkPageSize)
	}
	if newRequests != 1 {
		t.Errorf("`sort=priority` cost %d requests, want 1", newRequests)
	}
	t.Logf("%d rows in `bd list`'s default order: walk-and-sort = %d requests, sort=priority = %d request",
		listSortFixtureRows, oldRequests, newRequests)

	// The three sequences, row for row.
	refIDs := itemIDs(reference)
	if got := itemIDs(served.Items); !reflect.DeepEqual(got, refIDs) {
		t.Errorf("`sort=priority` does not reproduce `bd list`'s default order\nfirst divergence at %s", firstDivergence(got, refIDs))
	}
	if got := itemIDs(walked); !reflect.DeepEqual(got, refIDs) {
		t.Errorf("the walk-and-sort strategy does not reproduce `bd list`'s default order either, so the oracle is not what this test assumes\nfirst divergence at %s",
			firstDivergence(got, refIDs))
	}

	// AND THE SERVED ORDER PAGES. One request is the point, but a client with a
	// limit still has to be able to walk this order, and a walk is where a
	// position that lost its priority half — or a predicate whose
	// priority-equal arm lost its created_at bound — stops agreeing with the
	// one-shot answer. The page is deliberately not a divisor of the equal-key
	// run length, so boundaries land inside runs.
	pagedRequests := 0
	var paged []map[string]any
	seen := map[string]bool{}
	path = "/v0/beads/issues?sort=priority&limit=97"
	for {
		page := sp.countingPage(t, path, &pagedRequests)
		for _, item := range page.Items {
			id, _ := item["id"].(string)
			if seen[id] {
				t.Fatalf("the priority walk repeated %s at request %d; the equal-key run straddles the page boundary and the position re-delivered a row",
					id, pagedRequests)
			}
			seen[id] = true
		}
		paged = append(paged, page.Items...)
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Error("has_more is false but next_cursor is present; the document makes those a biconditional")
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatal("has_more is true but next_cursor is absent; the priority walk cannot continue")
		}
		path = fmt.Sprintf("/v0/beads/issues?sort=priority&limit=97&cursor=%s", *page.NextCursor)
		if pagedRequests > listSortFixtureRows {
			t.Fatal("the priority walk did not terminate")
		}
	}
	if got := itemIDs(paged); !reflect.DeepEqual(got, refIDs) {
		t.Errorf("a cursored walk in priority order did not reproduce the one-shot answer\nfirst divergence at %s", firstDivergence(got, refIDs))
	}

	t.Run("a cursor minted in one order is refused in the other", func(t *testing.T) {
		// Against the real server, over the real wire: the token is legible
		// base64 and the two orders' positions are the same shape, so this is
		// the refusal that stands between a client and a silently
		// skipped-and-duplicated page.
		first := sp.countingPage(t, "/v0/beads/issues?sort=priority&limit=10", new(int))
		if first.NextCursor == nil {
			t.Fatal("no cursor to replay")
		}
		status, body := sp.object(t, "/v0/beads/issues?limit=10&cursor="+*first.NextCursor)
		if status != http.StatusBadRequest {
			t.Fatalf("a priority cursor under the default order: status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_cursor" {
			t.Errorf("code = %v, want invalid_cursor", body["code"])
		}
	})
}

// firstDivergence names where two id sequences part company, because a diff of
// two 1400-element slices is unreadable and the position of the first
// disagreement is what identifies the defect.
func firstDivergence(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("index %d: got %s, want %s (got %d rows, want %d)", i, got[i], want[i], len(got), len(want))
		}
	}
	if len(got) == len(want) {
		return "nowhere: the sequences are equal"
	}
	return fmt.Sprintf("index %d: one sequence ended (got %d rows, want %d); missing/extra: %s",
		min(len(got), len(want)), len(got), len(want), strings.Join(symmetricDifference(got, want), ","))
}

// symmetricDifference names the ids one sequence has and the other does not,
// bounded so a wholesale mismatch does not print 1400 ids.
func symmetricDifference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, id := range b {
		inB[id] = true
	}
	inA := make(map[string]bool, len(a))
	for _, id := range a {
		inA[id] = true
	}
	var out []string
	for _, id := range a {
		if !inB[id] {
			out = append(out, "+"+id)
		}
	}
	for _, id := range b {
		if !inA[id] {
			out = append(out, "-"+id)
		}
	}
	if len(out) > 10 {
		out = append(out[:10], "…")
	}
	return out
}
