//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestShowWatchRenderNamesUnresolvableDepEdges pins #5272 on the third text
// render, `bd show --watch` (displayShowIssueReturn), which reads through the
// package store rather than the command's own: a blocks edge to another
// repository's id and a tracks edge to an external reference each print by id
// in their relationship's section, labeled and with no invented issue fields,
// and since both are printed the stderr notice stays silent.
func TestShowWatchRenderNamesUnresolvableDepEdges(t *testing.T) {
	testDB := filepath.Join(t.TempDir(), ".beads", "beads.db")
	s := newTestStoreWithPrefix(t, testDB, "ud")
	ctx := context.Background()

	subject := &types.Issue{ID: "ud-subject", Title: "Subject", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()}
	if err := s.CreateIssue(ctx, subject, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	for _, dep := range []*types.Dependency{
		{IssueID: subject.ID, DependsOnID: "liveop-kmf", Type: types.DepBlocks},
		{IssueID: subject.ID, DependsOnID: "external:liveop:liveop-cap", Type: types.DepTracks},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", dep.DependsOnID, err)
		}
	}

	oldStore, oldDBPath := store, dbPath
	t.Cleanup(func() { store, dbPath = oldStore, oldDBPath })
	store, dbPath = s, testDB

	stdioMutex.Lock()
	stdout, stderr := captureStdio(t, func() {
		if got := displayShowIssueReturn(ctx, subject.ID, 0); got == nil {
			t.Errorf("displayShowIssueReturn(%s) = nil", subject.ID)
		}
	})
	stdioMutex.Unlock()

	for _, want := range []string{
		"DEPENDS ON\n  →   liveop-kmf (cross-repo)\n",
		"TRACKS\n  →   external:liveop:liveop-cap (external)\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("watch render does not name the edge as %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "no row in this database") {
		t.Errorf("watch render warned about edges it printed:\nstderr:\n%s", stderr)
	}
}
