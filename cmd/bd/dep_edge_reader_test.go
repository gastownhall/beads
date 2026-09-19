package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover `bd dep list a b c`'s two halves above the role: which store each
// resolved anchor is asked, and what the shared printer emits. Everything below
// them is the role's, pinned by backend/conformance/edge_reader_contract.go.

// edgeReaderStore is a DoltStorage whose only real method is the accessor under
// test. Every other call is a nil panic, which is the assertion: this path must
// reach storage through the role and nothing else.
type edgeReaderStore struct {
	storage.DoltStorage
	reader issueops.EdgeReader
	err    error
}

func (s *edgeReaderStore) EdgeReader() (issueops.EdgeReader, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.reader, nil
}

type fakeEdgeReader struct {
	edges   map[string][]*types.Dependency
	missing map[string]bool
	err     error

	mu    sync.Mutex
	calls [][]string
}

func (f *fakeEdgeReader) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), req.IDs...))
	f.mu.Unlock()
	if f.err != nil {
		return issueops.EdgeReadResult{}, f.err
	}
	result := issueops.EdgeReadResult{Anchors: make([]issueops.AnchorEdges, 0, len(req.IDs))}
	for _, id := range req.IDs {
		result.Anchors = append(result.Anchors, issueops.AnchorEdges{
			ID:      id,
			Edges:   f.edges[id],
			Missing: f.missing[id],
		})
	}
	return result, nil
}

func edgeReaderAnchorFor(id string, reader issueops.EdgeReader) depListAnchor {
	return depListAnchor{fullID: id, store: &edgeReaderStore{reader: reader}}
}

// TestDepListEdgesReportsTheReadFailure is the regression for a shipped defect:
// the pre-role code fell through to the hydrated neighbor listing on ANY error,
// so a transient failure turned `bd dep list a b --json` from an array of
// dependency records into an array of ISSUES, with no error and no warning.
func TestDepListEdgesReportsTheReadFailure(t *testing.T) {
	want := errors.New("dependency table is unreachable")
	reader := &fakeEdgeReader{err: want}
	anchors := []depListAnchor{
		edgeReaderAnchorFor("bd-1", reader),
		edgeReaderAnchorFor("bd-2", reader),
	}

	if _, err := readDepListEdges(context.Background(), anchors, ""); !errors.Is(err, want) {
		t.Fatalf("readDepListEdges error = %v, want the read failure; a fall-through here changes the --json shape", err)
	}
}

// TestDepListEdgesAsksEachAnchorsOwnStore pins the routed half of the same
// defect: a batch whose anchors resolved to different stores also fell through
// to the neighbor listing, so a cross-rig `bd dep list` emitted a different
// document from a same-rig one. It also pins that no store is asked for another
// store's ids — an id sent to the wrong database comes back Missing, which
// reads as "no such issue".
func TestDepListEdgesAsksEachAnchorsOwnStore(t *testing.T) {
	home := &fakeEdgeReader{edges: map[string][]*types.Dependency{
		"hq-1": {{IssueID: "hq-1", DependsOnID: "hq-2", Type: types.DepBlocks}},
	}}
	rig := &fakeEdgeReader{edges: map[string][]*types.Dependency{
		"gt-1": {{IssueID: "gt-1", DependsOnID: "gt-2", Type: types.DepRelated}},
	}}
	homeStore := &edgeReaderStore{reader: home}
	rigStore := &edgeReaderStore{reader: rig}

	anchors := []depListAnchor{
		{fullID: "hq-1", store: homeStore},
		{fullID: "gt-1", store: rigStore},
		{fullID: "hq-3", store: homeStore},
	}
	got, err := readDepListEdges(context.Background(), anchors, "")
	if err != nil {
		t.Fatalf("readDepListEdges: %v", err)
	}

	// The answer is in ARGUMENT order, not store order: the two stores were
	// called one after the other, and reassembling by store would have put
	// gt-1 last.
	wantOrder := []string{"hq-1", "gt-1", "hq-3"}
	if len(got) != len(wantOrder) {
		t.Fatalf("anchors = %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Fatalf("anchor order = %v, want %v", []string{got[0].ID, got[1].ID, got[2].ID}, wantOrder)
		}
	}

	// One call per store, each carrying only its own ids.
	if len(home.calls) != 1 || strings.Join(home.calls[0], ",") != "hq-1,hq-3" {
		t.Errorf("home store calls = %v, want one for [hq-1 hq-3]", home.calls)
	}
	if len(rig.calls) != 1 || strings.Join(rig.calls[0], ",") != "gt-1" {
		t.Errorf("rig store calls = %v, want one for [gt-1]", rig.calls)
	}
}

// TestDepListEdgesCollapsesARepeatedAnchor pins that a repeat is one entry, so
// the printed answer and the --json array cannot double-count an id the caller
// named twice.
func TestDepListEdgesCollapsesARepeatedAnchor(t *testing.T) {
	reader := &fakeEdgeReader{edges: map[string][]*types.Dependency{
		"bd-1": {{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}},
	}}
	store := &edgeReaderStore{reader: reader}
	anchors := []depListAnchor{
		{fullID: "bd-1", store: store},
		{fullID: "bd-9", store: store},
		{fullID: "bd-1", store: store},
	}

	got, err := readDepListEdges(context.Background(), anchors, "")
	if err != nil {
		t.Fatalf("readDepListEdges: %v", err)
	}
	if len(got) != 2 || got[0].ID != "bd-1" || got[1].ID != "bd-9" {
		t.Fatalf("anchors = %+v, want bd-1 then bd-9, each once", got)
	}
}

// TestDepListEdgesForwardsTheTypeFilter pins that --type reaches the role
// rather than being applied after the read. The role narrows in the query it
// runs; a filter applied here would have read every edge first.
func TestDepListEdgesForwardsTheTypeFilter(t *testing.T) {
	reader := &recordingTypeEdgeReader{}
	anchors := []depListAnchor{
		edgeReaderAnchorFor("bd-1", reader),
		edgeReaderAnchorFor("bd-2", reader),
	}
	if _, err := readDepListEdges(context.Background(), anchors, "tracks"); err != nil {
		t.Fatalf("readDepListEdges: %v", err)
	}
	if len(reader.types) != 1 || reader.types[0] != types.DependencyType("tracks") {
		t.Fatalf("role was handed types %v, want the one --type named", reader.types)
	}
}

type recordingTypeEdgeReader struct{ types []types.DependencyType }

func (r *recordingTypeEdgeReader) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	r.types = append([]types.DependencyType(nil), req.Types...)
	return issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{}}, nil
}

// TestDepListPrintsGhostAnchorsToStderr pins the Q11-approved user-visible
// change. A ghost used to print "<id> has no dependencies" on the proxied
// route, which reads as a clean graph; it now prints the warning the direct
// route has always printed for an argument it could not resolve. Keeping it off
// stdout is what leaves --json a flat array of dependency records.
func TestDepListPrintsGhostAnchorsToStderr(t *testing.T) {
	anchors := []issueops.AnchorEdges{
		{ID: "bd-1", Edges: []*types.Dependency{{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}}},
		{ID: "bd-ghost", Missing: true},
		{ID: "bd-bare"},
	}

	t.Run("human", func(t *testing.T) {
		stdout, stderr := captureDepListOutput(t, false, anchors)
		if !strings.Contains(stderr, "warning: no issue found: bd-ghost (skipped)") {
			t.Errorf("stderr = %q, want the ghost warning", stderr)
		}
		if strings.Contains(stdout, "bd-ghost") {
			t.Errorf("stdout = %q, want the ghost anchor absent from the listing", stdout)
		}
		if !strings.Contains(stdout, "bd-bare has no dependencies") {
			t.Errorf("stdout = %q, want the present-but-edgeless anchor reported as having none", stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr := captureDepListOutput(t, true, anchors)
		if !strings.Contains(stderr, "warning: no issue found: bd-ghost (skipped)") {
			t.Errorf("stderr = %q, want the ghost warning in --json mode too", stderr)
		}
		var got []*types.Dependency
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("--json output %q is not an array of dependency records: %v", stdout, err)
		}
		if len(got) != 1 || got[0].DependsOnID != "bd-2" {
			t.Fatalf("--json output = %q, want the one edge flattened across the anchors", stdout)
		}
	})
}

func captureDepListOutput(t *testing.T, asJSON bool, anchors []issueops.AnchorEdges) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldOut, oldErr, oldJSON := os.Stdout, os.Stderr, jsonOutput
	os.Stdout, os.Stderr, jsonOutput = outW, errW, asJSON
	t.Cleanup(func() { os.Stdout, os.Stderr, jsonOutput = oldOut, oldErr, oldJSON })

	printErr := printDepListEdges(anchors)
	_ = outW.Close()
	_ = errW.Close()
	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	if printErr != nil {
		t.Fatalf("printDepListEdges: %v", printErr)
	}
	return string(outBytes), string(errBytes)
}
