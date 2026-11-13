package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestDepListAcceptsMultipleArgs(t *testing.T) {
	// Verify the cobra command accepts multiple args (MinimumNArgs(1)).
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{"id1", "id2", "id3"}); err != nil {
		t.Fatalf("depListCmd should accept multiple args: %v", err)
	}
}

func TestDepListRequiresAtLeastOneArg(t *testing.T) {
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("depListCmd should reject zero args")
	}
}

func TestDepListAcceptsSingleArg(t *testing.T) {
	cmd := depListCmd
	if err := cmd.Args(cmd, []string{"id1"}); err != nil {
		t.Fatalf("depListCmd should accept single arg: %v", err)
	}
}

// bulkProbeStore counts every call the batch resolution could make, so the
// tests below pin the MECHANISM — one bulk read — and not just the answer.
// Any per-id GetIssue or SearchIssues call is the pre-fix behavior returning:
// two queries per argument, ~60ms each on dolt, and a 780-id batch that
// measures 44-70s where one bulk read measures milliseconds.
type bulkProbeStore struct {
	storage.DoltStorage
	issues    map[string]*types.Issue
	err       error // forces the bulk read to fail
	reader    issueops.EdgeReader
	bulkReqs  [][]string
	perID     int
	edgeReads int
}

func (s *bulkProbeStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	s.bulkReqs = append(s.bulkReqs, append([]string(nil), ids...))
	if s.err != nil {
		return nil, s.err
	}
	var out []*types.Issue
	for _, id := range ids {
		if issue, ok := s.issues[id]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (s *bulkProbeStore) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	s.perID++
	return nil, errors.New("no issue found")
}

func (s *bulkProbeStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	s.perID++
	return nil, nil
}

func TestDepListBatchResolvesExactIDsInOneBulkRead(t *testing.T) {
	store := &bulkProbeStore{issues: map[string]*types.Issue{
		"bd-1": {ID: "bd-1"},
		"bd-2": {ID: "bd-2"},
		"bd-3": {ID: "bd-3"},
	}}
	// bd-missing exists nowhere and bd-1 is repeated: the bulk read must see
	// each distinct arg once, and only exact hits may become anchors.
	anchors := bulkLocalDepListAnchors(context.Background(), store,
		[]string{"bd-1", "bd-2", "bd-missing", "bd-1", "bd-3"})

	if len(store.bulkReqs) != 1 {
		t.Fatalf("bulk reads = %d, want exactly 1", len(store.bulkReqs))
	}
	want := []string{"bd-1", "bd-2", "bd-missing", "bd-3"}
	if len(store.bulkReqs[0]) != len(want) {
		t.Fatalf("bulk ids = %v, want %v (duplicates collapsed)", store.bulkReqs[0], want)
	}
	if len(anchors) != 3 {
		t.Fatalf("anchors = %d, want 3 (the missing id must fall through to routing)", len(anchors))
	}
	if _, ok := anchors["bd-missing"]; ok {
		t.Fatal("bd-missing must not anchor from the bulk answer")
	}
	for id, anchor := range anchors {
		if anchor.fullID != id || anchor.store != storage.DoltStorage(store) {
			t.Fatalf("anchor %q = %+v, want fullID %q on the local store", id, anchor, id)
		}
	}
	if store.perID != 0 {
		t.Fatalf("per-id lookups = %d, want 0: two queries per argument is the regression", store.perID)
	}
}

func TestDepListSingleArgStaysOnRoutingPath(t *testing.T) {
	// The fix is scoped to the batch path; a single id must not pay (or
	// change behavior on) the bulk read.
	store := &bulkProbeStore{issues: map[string]*types.Issue{"bd-1": {ID: "bd-1"}}}
	if anchors := bulkLocalDepListAnchors(context.Background(), store, []string{"bd-1"}); anchors != nil {
		t.Fatalf("single arg returned %+v, want nil (no bulk path)", anchors)
	}
	if len(store.bulkReqs) != 0 {
		t.Fatalf("bulk reads = %d, want 0 for a single arg", len(store.bulkReqs))
	}
}

func TestDepListBatchFallsBackWhenBulkReadFails(t *testing.T) {
	// A failing bulk read degrades to per-id resolution — the shape of
	// today's command — and never becomes a new failure mode.
	store := &bulkProbeStore{err: errors.New("database is locked")}
	if anchors := bulkLocalDepListAnchors(context.Background(), store, []string{"bd-1", "bd-2"}); anchors != nil {
		t.Fatalf("bulk failure returned %+v, want nil so the per-argument loop answers", anchors)
	}
}

// EdgeReader serves the batch "down" path: readDepListEdges asks each anchor
// store once for its edges, so a command-level drive can see that the bulk
// anchors reached the role.
func (s *bulkProbeStore) EdgeReader() (issueops.EdgeReader, error) {
	s.edgeReads++
	if s.reader == nil {
		return nil, errors.New("bulkProbeStore: no edge reader")
	}
	return s.reader, nil
}

// batchEdgeRecorder answers one bulk edge read per call, recording the anchors
// it was asked about and returning a stored edge for each id in edges.
type batchEdgeRecorder struct {
	edges map[string][]*types.Dependency
	calls [][]string
}

func (r *batchEdgeRecorder) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	r.calls = append(r.calls, append([]string(nil), req.IDs...))
	result := issueops.EdgeReadResult{Anchors: make([]issueops.AnchorEdges, 0, len(req.IDs))}
	for _, id := range req.IDs {
		result.Anchors = append(result.Anchors, issueops.AnchorEdges{ID: id, Edges: r.edges[id]})
	}
	return result, nil
}

// TestDepListBatchCommandConsumesBulkAnswer drives the COMMAND, not the
// helper. The store-level tests above pin what bulkLocalDepListAnchors
// returns; none of them can see the call site. Removing the bulk lookup from
// depListCmd's loop (`if anchor, ok := bulk[arg]; ok` -> `ok && false`)
// switches the whole 44-70s -> 0.3s claim off and stayed green on all three:
// the command fell back to the unchanged per-argument path and every test
// still passed because none of them ran the command. This test closes that
// mutant: a batch of exact ids runs depListCmd.RunE end to end and asserts
// ZERO per-id SearchIssues/GetIssue calls — the one bulk answer must be what
// the command resolves from — plus that the anchors it produced reached the
// edge role and the printer, so the zero cannot be reached by doing nothing.
func TestDepListBatchCommandConsumesBulkAnswer(t *testing.T) {
	saveAndRestoreGlobals(t)
	resetCommandContext()
	prevJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = prevJSON }()

	recorder := &batchEdgeRecorder{edges: map[string][]*types.Dependency{
		"bd-1": {{IssueID: "bd-1", DependsOnID: "bd-9", Type: types.DepBlocks}},
	}}
	probe := &bulkProbeStore{
		issues: map[string]*types.Issue{
			"bd-1": {ID: "bd-1"},
			"bd-2": {ID: "bd-2"},
			"bd-3": {ID: "bd-3"},
		},
		reader: recorder,
	}
	store = probe

	// depListCmd is package-level: pin the flags this path reads and put
	// whatever another test left behind back afterwards.
	prevDir, _ := depListCmd.Flags().GetString("direction")
	prevType, _ := depListCmd.Flags().GetString("type")
	defer func() {
		_ = depListCmd.Flags().Set("direction", prevDir)
		_ = depListCmd.Flags().Set("type", prevType)
	}()
	if err := depListCmd.Flags().Set("direction", "down"); err != nil {
		t.Fatal(err)
	}
	if err := depListCmd.Flags().Set("type", ""); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() error {
		return depListCmd.RunE(depListCmd, []string{"bd-1", "bd-2", "bd-3"})
	})

	if len(probe.bulkReqs) != 1 {
		t.Fatalf("bulk reads = %d, want exactly 1", len(probe.bulkReqs))
	}
	// The mutant pin: if the command ignores the bulk map, every argument
	// goes back through resolveAndGetIssueWithRouting and this counter moves.
	if probe.perID != 0 {
		t.Fatalf("per-id lookups = %d, want 0 under the command: the speedup is only real if the command resolves exact ids from the bulk answer", probe.perID)
	}
	// The zero must not be vacuous: the bulk anchors have to reach the edge
	// role in ONE call, in argument order.
	if len(recorder.calls) != 1 {
		t.Fatalf("edge-role calls = %d, want 1 (one bulk read feeds one bulk edge read)", len(recorder.calls))
	}
	want := []string{"bd-1", "bd-2", "bd-3"}
	if len(recorder.calls[0]) != len(want) {
		t.Fatalf("edge-role anchors = %v, want %v", recorder.calls[0], want)
	}
	for i := range want {
		if recorder.calls[0][i] != want[i] {
			t.Fatalf("edge-role anchors = %v, want %v (argument order)", recorder.calls[0], want)
		}
	}
	// And the printed answer names all three anchors, with bd-1's edge.
	for _, id := range want {
		if !strings.Contains(out, id) {
			t.Fatalf("stdout missing anchor %q; got:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "bd-9 via blocks") {
		t.Fatalf("stdout missing bd-1's stored edge; got:\n%s", out)
	}
}
