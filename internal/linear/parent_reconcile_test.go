package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// linearMockHandler is a tiny GraphQL mock that routes by query keyword.
// Queries it understands:
//   - "IssueByIdentifier" → returns issues[identifier] (or empty nodes)
//   - "issueUpdate"       → records the update and returns the (notional) issue
//
// All other queries return an empty data object.
//
// Fidelity gap (acceptable for unit tests): the real client's
// FetchIssueByIdentifier filters by team UUID AND issue number; this mock
// suffix-scans the `number.eq` value against issue identifiers and ignores
// team. That's fine here because we only care about parent-reconcile
// semantics, not the team-routing logic of FetchIssueByIdentifier itself.
type linearMockHandler struct {
	t           *testing.T
	issues      map[string]*Issue          // identifier → issue (for fetch)
	updates     map[string]json.RawMessage // child UUID → input JSON (recorded)
	fetches     map[string]int             // identifier → fetch call count
	failNextRL  bool                       // when true, next fetch returns ErrRateLimitExhausted
	rateLimited int                        // count of rate-limited responses returned
}

func newLinearMock(t *testing.T) *linearMockHandler {
	return &linearMockHandler{
		t:       t,
		issues:  map[string]*Issue{},
		updates: map[string]json.RawMessage{},
		fetches: map[string]int{},
	}
}

func (h *linearMockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req GraphQLRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.t.Fatalf("mock: bad request JSON: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")

	// If failNextRL is set, return rate-limit-exhausted on this single
	// response (header drives the circuit breaker in Execute). The breaker
	// only arms when the reset header carries a valid future timestamp, so
	// send one alongside the low remaining count.
	if h.failNextRL {
		h.failNextRL = false
		h.rateLimited++
		w.Header().Set("X-RateLimit-Requests-Remaining", "1") // < DefaultRateLimitFloor (100)
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
		return
	}

	switch {
	case strings.Contains(req.Query, "IssueByIdentifier"):
		// FetchIssueByIdentifier filters by team + number; the mock keys
		// directly by identifier extracted from the filter for simplicity.
		filter, _ := req.Variables["filter"].(map[string]interface{})
		number, _ := filter["number"].(map[string]interface{})
		eq, _ := number["eq"].(float64)
		// Recover identifier by scanning issues for a matching team+number.
		// In the test fixture we just embed the number in identifier "TEAM-N".
		var found *Issue
		for ident, iss := range h.issues {
			if strings.HasSuffix(ident, "-"+itoa(int(eq))) {
				h.fetches[ident]++
				found = iss
				break
			}
		}
		nodes := []interface{}{}
		if found != nil {
			b, _ := json.Marshal(found)
			var raw map[string]interface{}
			_ = json.Unmarshal(b, &raw)
			nodes = append(nodes, raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{"nodes": nodes},
			},
		})
		return
	case strings.Contains(req.Query, "issueUpdate"):
		id, _ := req.Variables["id"].(string)
		input, _ := json.Marshal(req.Variables["input"])
		h.updates[id] = input
		// Return the (notional) updated issue. Tests don't depend on the
		// exact echo, but we round-trip the relevant fields so the cached
		// fetch is consistent if subsequent calls reference this child.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"issueUpdate": map[string]interface{}{
					"success": true,
					"issue": map[string]interface{}{
						"id":         id,
						"identifier": "ECHO",
						"title":      "echo",
						"updatedAt":  "2026-05-13T00:00:00Z",
					},
				},
			},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
}

func itoa(n int) string {
	// stdlib without importing strconv twice
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func newTestLinearTracker(t *testing.T, serverURL string) *Tracker {
	tr := &Tracker{
		teamIDs: []string{"team-uuid"},
		clients: map[string]*Client{
			"team-uuid": NewClient("test-key", "team-uuid").WithEndpoint(serverURL),
		},
	}
	return tr
}

// newTestMultiTeamTracker wires a tracker with N teams, each pointed at its
// own mock-server URL. Returns the tracker and the same teamIDs slice in
// stable order (matching Tracker.teamIDs iteration order).
func newTestMultiTeamTracker(t *testing.T, serverURLs []string) (*Tracker, []string) {
	teamIDs := make([]string, len(serverURLs))
	clients := make(map[string]*Client, len(serverURLs))
	for i, url := range serverURLs {
		id := "team-" + itoa(i)
		teamIDs[i] = id
		clients[id] = NewClient("test-key", id).WithEndpoint(url)
	}
	return &Tracker{teamIDs: teamIDs, clients: clients}, teamIDs
}

// TestReconcileParents_HappyPath verifies that a child needing its parent
// set produces exactly one fetch per unique identifier and one UpdateIssue
// call carrying the parent's UUID.
func TestReconcileParents_HappyPath(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-child", Identifier: "TEAM-1"}  // child has no parent yet
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-parent", Identifier: "TEAM-2"} // parent
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-2"},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 1 || stats.Skipped != 0 || len(stats.Errors) != 0 {
		t.Fatalf("stats = %+v, want Updated=1 Skipped=0", stats)
	}
	if len(mock.updates) != 1 {
		t.Fatalf("expected 1 issueUpdate call, got %d", len(mock.updates))
	}
	// The update target should be the child's UUID.
	input, ok := mock.updates["uuid-child"]
	if !ok {
		t.Fatalf("expected update on uuid-child, got updates: %v", mock.updates)
	}
	if !strings.Contains(string(input), `"parentId":"uuid-parent"`) {
		t.Errorf("update input missing parent UUID: %s", input)
	}
}

// TestReconcileParents_IdempotentSkip verifies that when the child's
// remote parent already matches, no UpdateIssue is issued.
func TestReconcileParents_IdempotentSkip(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-child", Identifier: "TEAM-1",
		Parent: &Parent{ID: "uuid-parent", Identifier: "TEAM-2"}}
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-parent", Identifier: "TEAM-2"}
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-2"},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 0 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want Updated=0 Skipped=1", stats)
	}
	if len(mock.updates) != 0 {
		t.Errorf("expected no updates (idempotent), got %d", len(mock.updates))
	}
}

// TestReconcileParents_RewiresWrongParent verifies that a child currently
// pointing at a different parent gets its parent rewired (Linear-side state
// took precedence over local intent — bd-ena's intended behavior).
func TestReconcileParents_RewiresWrongParent(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-child", Identifier: "TEAM-1",
		Parent: &Parent{ID: "uuid-WRONG-parent", Identifier: "TEAM-99"}}
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-parent", Identifier: "TEAM-2"}
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-2"},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 1 {
		t.Errorf("Updated = %d, want 1 (wrong parent should be rewired)", stats.Updated)
	}
	input := mock.updates["uuid-child"]
	if !strings.Contains(string(input), `"parentId":"uuid-parent"`) {
		t.Errorf("expected rewire to uuid-parent, got: %s", input)
	}
}

// TestReconcileParents_FetchCaching verifies that a parent referenced by
// multiple children is fetched only once. (Cuts API calls in the common
// case where one epic has many sub-issues.)
func TestReconcileParents_FetchCaching(t *testing.T) {
	mock := newLinearMock(t)
	for i, ident := range []string{"TEAM-10", "TEAM-11", "TEAM-12"} {
		_ = i
		mock.issues[ident] = &Issue{ID: "uuid-" + ident, Identifier: ident}
	}
	mock.issues["TEAM-99"] = &Issue{ID: "uuid-parent", Identifier: "TEAM-99"}
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	links := []ParentLink{
		{ChildIdentifier: "TEAM-10", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-11", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-12", ParentIdentifier: "TEAM-99"},
	}
	stats, err := tr.ReconcileParents(context.Background(), links, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 3 {
		t.Errorf("Updated = %d, want 3", stats.Updated)
	}
	if mock.fetches["TEAM-99"] != 1 {
		t.Errorf("parent fetched %d times, want 1 (caching broken)", mock.fetches["TEAM-99"])
	}
}

// TestReconcileParents_MissingChildOrParent verifies that links with
// unresolvable identifiers go to NotFound rather than failing the pass.
func TestReconcileParents_MissingChildOrParent(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-child", Identifier: "TEAM-1"}
	// TEAM-99 (parent) and TEAM-2 (child for second link) are intentionally
	// absent.
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-99"}, // parent missing
		{ChildIdentifier: "TEAM-2", ParentIdentifier: "TEAM-1"},  // child missing
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0", stats.Updated)
	}
	if len(stats.NotFound) != 2 {
		t.Errorf("NotFound = %v, want 2 entries", stats.NotFound)
	}
	if len(mock.updates) != 0 {
		t.Errorf("expected no updates when targets missing, got %d", len(mock.updates))
	}
}

// TestReconcileParents_EmptyLinks short-circuits cleanly.
func TestReconcileParents_EmptyLinks(t *testing.T) {
	tr := &Tracker{} // no client needed
	stats, err := tr.ReconcileParents(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 0 || stats.Skipped != 0 {
		t.Errorf("stats = %+v, want zero", stats)
	}
}

// TestReconcileParents_MultiTeam verifies cross-team probing: a child
// living on team B and a parent on team A both resolve, and the update
// goes to the team-client that owns the child (not the primary team's
// client). This guards against the silent-fallback bug in
// clientForExternalID where probe failures route to the primary client.
func TestReconcileParents_MultiTeam(t *testing.T) {
	mockA := newLinearMock(t)
	mockA.issues["TEAM-1"] = &Issue{ID: "uuid-A1-parent", Identifier: "TEAM-1"}
	srvA := httptest.NewServer(mockA)
	defer srvA.Close()

	mockB := newLinearMock(t)
	mockB.issues["TEAM-2"] = &Issue{ID: "uuid-B2-child", Identifier: "TEAM-2"}
	srvB := httptest.NewServer(mockB)
	defer srvB.Close()

	tr, _ := newTestMultiTeamTracker(t, []string{srvA.URL, srvB.URL})
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-2", ParentIdentifier: "TEAM-1"},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", stats.Updated)
	}
	// Update must hit team B's mock (where the child lives), not team A's.
	if _, ok := mockB.updates["uuid-B2-child"]; !ok {
		t.Errorf("expected update on team B mock for uuid-B2-child, got: %v / %v",
			mockA.updates, mockB.updates)
	}
	if len(mockA.updates) != 0 {
		t.Errorf("expected no updates on team A (parent's team), got: %v", mockA.updates)
	}
}

// TestReconcileParents_AbortsOnRateLimit verifies that hitting the rate-
// limit circuit breaker stops the pass immediately instead of grinding
// through the remaining links and accumulating failures.
func TestReconcileParents_AbortsOnRateLimit(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-child1", Identifier: "TEAM-1"}
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-child2", Identifier: "TEAM-2"}
	mock.issues["TEAM-99"] = &Issue{ID: "uuid-parent", Identifier: "TEAM-99"}
	mock.failNextRL = true // first request returns ErrRateLimitExhausted
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-2", ParentIdentifier: "TEAM-99"},
	}, false)
	if err == nil {
		t.Fatal("expected error from rate-limit abort, got nil")
	}
	if !strings.Contains(err.Error(), "rate limit") && !strings.Contains(err.Error(), "RateLimitExhausted") {
		t.Errorf("expected rate-limit error, got: %v", err)
	}
	// Pass should bail out — at most the first child was processed; subsequent
	// links should never reach IssueUpdate.
	if len(mock.updates) > 0 {
		t.Errorf("expected 0 updates after rate-limit abort, got %d", len(mock.updates))
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (no updates after abort)", stats.Updated)
	}
	if mock.rateLimited != 1 {
		t.Errorf("expected exactly 1 rate-limited response (single failure → abort), got %d", mock.rateLimited)
	}
}

// TestReconcileParents_BlankIdentifiersSkipped — defensive guard against
// callers that build links with empty fields.
func TestReconcileParents_BlankIdentifiersSkipped(t *testing.T) {
	mock := newLinearMock(t)
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "", ParentIdentifier: "TEAM-2"},
		{ChildIdentifier: "TEAM-1", ParentIdentifier: ""},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0", stats.Updated)
	}
	if len(mock.updates) != 0 || len(mock.fetches) != 0 {
		t.Errorf("expected no API calls for blank links, fetches=%v updates=%v",
			mock.fetches, mock.updates)
	}
}

// TestReconcileParents_DryRunNoMutations is the bd-5zh acceptance: when
// dryRun=true, the read-only fetches still run (Skipped, NotFound, and
// Mutations populate correctly) but ZERO IssueUpdate calls fire. WouldUpdate
// counts the mutations that would have been issued; Updated stays 0.
func TestReconcileParents_DryRunNoMutations(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-c1", Identifier: "TEAM-1"} // needs parent
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-c2", Identifier: "TEAM-2", //
		Parent: &Parent{ID: "uuid-p", Identifier: "TEAM-99"}} // already correct
	mock.issues["TEAM-3"] = &Issue{ID: "uuid-c3", Identifier: "TEAM-3"} // needs parent
	mock.issues["TEAM-99"] = &Issue{ID: "uuid-p", Identifier: "TEAM-99"}
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-2", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-3", ParentIdentifier: "TEAM-99"},
	}, true)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0 in dry-run", stats.Updated)
	}
	if stats.WouldUpdate != 2 {
		t.Errorf("WouldUpdate = %d, want 2 (TEAM-1 + TEAM-3 need parent set)", stats.WouldUpdate)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (TEAM-2 already correct)", stats.Skipped)
	}
	if len(mock.updates) != 0 {
		t.Errorf("dry-run must not issue IssueUpdate; got %d updates: %v", len(mock.updates), mock.updates)
	}
	if len(stats.Mutations) != 2 {
		t.Fatalf("Mutations = %v, want 2 entries", stats.Mutations)
	}
	// Mutations should carry the original identifiers in order of input.
	if stats.Mutations[0].ChildIdentifier != "TEAM-1" || stats.Mutations[0].ParentIdentifier != "TEAM-99" {
		t.Errorf("Mutations[0] = %+v, want TEAM-1 → TEAM-99", stats.Mutations[0])
	}
	if stats.Mutations[1].ChildIdentifier != "TEAM-3" || stats.Mutations[1].ParentIdentifier != "TEAM-99" {
		t.Errorf("Mutations[1] = %+v, want TEAM-3 → TEAM-99", stats.Mutations[1])
	}
}

// TestReconcileParents_DryRunIdempotentSkipUnchanged verifies that when
// every link is already correct, dry-run produces zero WouldUpdate and
// matches wet-run's Skipped count.
func TestReconcileParents_DryRunIdempotentSkipUnchanged(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-c", Identifier: "TEAM-1",
		Parent: &Parent{ID: "uuid-p", Identifier: "TEAM-2"}}
	mock.issues["TEAM-2"] = &Issue{ID: "uuid-p", Identifier: "TEAM-2"}
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-2"},
	}, true)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.WouldUpdate != 0 {
		t.Errorf("WouldUpdate = %d, want 0 (already correct)", stats.WouldUpdate)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
	if len(stats.Mutations) != 0 {
		t.Errorf("Mutations = %v, want empty (no changes needed)", stats.Mutations)
	}
}

// TestReconcileParents_DryRunMissingTargetsNotFound verifies that dry-run
// still surfaces NotFound entries for unresolvable identifiers, so the user
// gets the same visibility into orphan targets as wet-run.
func TestReconcileParents_DryRunMissingTargetsNotFound(t *testing.T) {
	mock := newLinearMock(t)
	mock.issues["TEAM-1"] = &Issue{ID: "uuid-c", Identifier: "TEAM-1"}
	// TEAM-99 (parent) intentionally absent.
	server := httptest.NewServer(mock)
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-99"},
	}, true)
	if err != nil {
		t.Fatalf("ReconcileParents err: %v", err)
	}
	if stats.WouldUpdate != 0 || stats.Updated != 0 {
		t.Errorf("Would/Updated = %d/%d, want 0/0", stats.WouldUpdate, stats.Updated)
	}
	if len(stats.NotFound) != 1 || stats.NotFound[0] != "TEAM-99" {
		t.Errorf("NotFound = %v, want [TEAM-99]", stats.NotFound)
	}
}

// TestReconcileParents_DryRunMatchesWetRunMutationSet verifies that the
// Mutations list is identical between dry-run and wet-run for the same
// input. This is the property that makes dry-run a trustworthy pre-flight:
// what you see is what you'll get.
func TestReconcileParents_DryRunMatchesWetRunMutationSet(t *testing.T) {
	makeMock := func() *linearMockHandler {
		m := newLinearMock(t)
		m.issues["TEAM-1"] = &Issue{ID: "uuid-c1", Identifier: "TEAM-1"}
		m.issues["TEAM-2"] = &Issue{ID: "uuid-c2", Identifier: "TEAM-2"}
		m.issues["TEAM-99"] = &Issue{ID: "uuid-p", Identifier: "TEAM-99"}
		return m
	}
	links := []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-99"},
		{ChildIdentifier: "TEAM-2", ParentIdentifier: "TEAM-99"},
	}

	// Dry-run
	dryMock := makeMock()
	drySrv := httptest.NewServer(dryMock)
	defer drySrv.Close()
	dryTr := newTestLinearTracker(t, drySrv.URL)
	dryStats, err := dryTr.ReconcileParents(context.Background(), links, true)
	if err != nil {
		t.Fatalf("dry: %v", err)
	}

	// Wet-run
	wetMock := makeMock()
	wetSrv := httptest.NewServer(wetMock)
	defer wetSrv.Close()
	wetTr := newTestLinearTracker(t, wetSrv.URL)
	wetStats, err := wetTr.ReconcileParents(context.Background(), links, false)
	if err != nil {
		t.Fatalf("wet: %v", err)
	}

	if dryStats.WouldUpdate != wetStats.Updated {
		t.Errorf("dry WouldUpdate=%d vs wet Updated=%d (must match)",
			dryStats.WouldUpdate, wetStats.Updated)
	}
	if len(dryStats.Mutations) != len(wetStats.Mutations) {
		t.Fatalf("Mutations length differs: dry=%d wet=%d",
			len(dryStats.Mutations), len(wetStats.Mutations))
	}
	for i := range dryStats.Mutations {
		if dryStats.Mutations[i] != wetStats.Mutations[i] {
			t.Errorf("Mutations[%d] differ: dry=%+v wet=%+v",
				i, dryStats.Mutations[i], wetStats.Mutations[i])
		}
	}
	// Sanity: wet-run actually issued updates, dry-run did not.
	if len(dryMock.updates) != 0 {
		t.Errorf("dry mock saw %d updates; expected 0", len(dryMock.updates))
	}
	if len(wetMock.updates) != 2 {
		t.Errorf("wet mock saw %d updates; expected 2", len(wetMock.updates))
	}
}

// TestReconcileParents_WetRunFailureDoesNotRecordMutation verifies that
// when a wet-run UpdateIssue call fails (e.g. transient API error), the
// failed mutation is NOT appended to stats.Mutations. The contract is
// that Mutations reflects state actually propagated to Linear in wet-run,
// so callers can trust it for post-sync reporting.
func TestReconcileParents_WetRunFailureDoesNotRecordMutation(t *testing.T) {
	// Custom handler: succeeds on fetch, returns success=false on update.
	updateAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "IssueByIdentifier"):
			filter, _ := req.Variables["filter"].(map[string]interface{})
			number, _ := filter["number"].(map[string]interface{})
			eq, _ := number["eq"].(float64)
			id := "uuid-" + itoa(int(eq))
			ident := "TEAM-" + itoa(int(eq))
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"id": id, "identifier": ident,
								"createdAt": "2026-05-22T00:00:00Z", "updatedAt": "2026-05-22T00:00:00Z"},
						},
					},
				},
			})
		case strings.Contains(req.Query, "issueUpdate"):
			updateAttempts++
			// Return success=false to surface as an error to the client.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueUpdate": map[string]interface{}{"success": false, "issue": nil},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
		}
	}))
	defer server.Close()

	tr := newTestLinearTracker(t, server.URL)
	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "TEAM-1", ParentIdentifier: "TEAM-2"},
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents: %v", err)
	}
	if updateAttempts != 1 {
		t.Errorf("expected 1 update attempt, got %d", updateAttempts)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (API call failed)", stats.Updated)
	}
	if len(stats.Mutations) != 0 {
		t.Errorf("Mutations = %v, want empty (no successful mutation)", stats.Mutations)
	}
	if len(stats.Errors) != 1 {
		t.Errorf("Errors = %v, want 1 entry", stats.Errors)
	}
}
