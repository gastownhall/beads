//go:build cgo

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// AC-3 (bd-uschhs, #5850), the watch route: displayShowIssueReturn renders an
// outgoing edge whose far end has no row here in its relation section, named
// exactly and marked "(not in this database)" — a tracks edge as its section's
// only entry, a related one in RELATED — beside an ordinary local blocker, and
// prints no outgoing count warning.
func TestShowWatchRenderNamesUnresolvedDependencies(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	for _, issue := range []*types.Issue{
		{ID: "test-1", Title: "subject", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-2", Title: "local blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "test-1", DependsOnID: "test-2", Type: types.DepBlocks},
		{IssueID: "test-1", DependsOnID: "external:liveop:liveop-kmf", Type: types.DepTracks},
		{IssueID: "test-1", DependsOnID: "external:other:x", Type: types.DepRelated},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	oldStore := store
	store = s
	t.Cleanup(func() { store = oldStore })

	// captureStdout holds stdioMutex, so stderr is piped by hand inside it
	// (nesting captureStderr would take the same mutex again).
	var stderr string
	stdout := captureStdout(t, func() error {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		oldStderr := os.Stderr
		os.Stderr = w
		done := make(chan string, 1)
		go func() {
			b, _ := io.ReadAll(r)
			done <- string(b)
		}()
		issue := displayShowIssueReturn(ctx, "test-1", 0)
		os.Stderr = oldStderr
		_ = w.Close()
		stderr = <-done
		_ = r.Close()
		if issue == nil {
			t.Error("displayShowIssueReturn returned no issue")
		}
		return nil
	})

	for _, want := range []string{
		"DEPENDS ON", "test-2",
		"TRACKS", "→ external:liveop:liveop-kmf (not in this database)",
		"RELATED", "↔ external:other:x (not in this database)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("watch render missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "dependency edge(s)") || strings.Contains(stderr, "For raw edge records") {
		t.Errorf("watch render still printed the outgoing warning:\n%s", stderr)
	}
}
