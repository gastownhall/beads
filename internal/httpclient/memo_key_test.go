package httpclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestDefaultIssueReadsShareOneMemoKey (mc-5dkzp): every default-shaped read
// of one issue that bd show makes — and every resolve-then-get command with it
// — must be the SAME request. The per-command read memo is keyed by the full
// URL, so a read that grows a parameter of its own (even one spelling its
// default, like brief_deps=false) silently brings the extra round trip back.
// This pins the five reads to one memo key without a server.
func TestDefaultIssueReadsShareOneMemoKey(t *testing.T) {
	s, w := recordingStore(t)
	ctx := context.Background()
	const id = "bd-1"
	// The id resolver's exact probe (utils.ResolvePartialID over an
	// exact-only store).
	if _, err := s.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{id}}); err != nil {
		t.Fatal(err)
	}
	// cmd/bd/routed.go's fetch of the resolved id.
	if _, err := s.GetIssue(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Text show's dependency and label sections.
	if _, err := s.GetDependenciesWithMetadata(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLabels(ctx, id); err != nil {
		t.Fatal(err)
	}
	// bd show --json's detail read with default flags.
	rd, err := s.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rd.Get(ctx, issueops.GetRequest{ID: id}); err != nil {
		t.Fatal(err)
	}

	if len(w.requests) != 5 {
		t.Fatalf("dispatched %d requests (%v), want 5", len(w.requests), w.dispatched)
	}
	want := "getIssue " + http.MethodGet + " /v0/beads/issues/" + id + "?"
	for i, r := range w.requests {
		if got := r.Op + " " + r.Method + " " + r.Path + "?" + r.Query.Encode(); got != want {
			t.Errorf("read %d dispatched %q; every default issue read must be %q (one memo key)", i, got, want)
		}
	}
}
