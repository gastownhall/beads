//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
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

// recordCountingStore counts the raw edge-record reads a render makes, the
// read #5272 adds behind the positive outbound count gap.
type recordCountingStore struct {
	storage.DoltStorage
	reads atomic.Int32
}

func (s *recordCountingStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	s.reads.Add(1)
	return s.DoltStorage.GetDependencyRecords(ctx, issueID)
}

// TestShowWatchRenderLeavesLocalOnlyOutputAlone pins the other half of #5272:
// an issue whose edges are all local, or that has none, renders exactly the
// lines it always did and makes no raw edge-record read. The unresolved
// subject is the positive control through the same wrapper, so a render that
// never reaches the counting store cannot pass the zero-read checks.
func TestShowWatchRenderLeavesLocalOnlyOutputAlone(t *testing.T) {
	testDB := filepath.Join(t.TempDir(), ".beads", "beads.db")
	s := newTestStoreWithPrefix(t, testDB, "ud")
	ctx := context.Background()

	for _, id := range []string{"ud-blocker", "ud-local", "ud-edgeless", "ud-remote"} {
		issue := &types.Issue{ID: id, Title: "Issue " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "ud-local", DependsOnID: "ud-blocker", Type: types.DepBlocks},
		{IssueID: "ud-remote", DependsOnID: "liveop-kmf", Type: types.DepBlocks},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", dep.DependsOnID, err)
		}
	}
	localDeps, err := s.GetDependenciesWithMetadata(ctx, "ud-local")
	if err != nil || len(localDeps) != 1 {
		t.Fatalf("GetDependenciesWithMetadata(ud-local) = %v, %v; want the one local blocker", localDeps, err)
	}

	counting := &recordCountingStore{DoltStorage: s}
	oldStore, oldDBPath := store, dbPath
	t.Cleanup(func() { store, dbPath = oldStore, oldDBPath })
	store, dbPath = counting, testDB

	render := func(id string) (stdout, stderr string, reads int32) {
		t.Helper()
		counting.reads.Store(0)
		stdioMutex.Lock()
		defer stdioMutex.Unlock()
		stdout, stderr = captureStdio(t, func() {
			if got := displayShowIssueReturn(ctx, id, 0); got == nil {
				t.Errorf("displayShowIssueReturn(%s) = nil", id)
			}
		})
		return stdout, stderr, counting.reads.Load()
	}

	stdout, _, reads := render("ud-remote")
	if reads != 1 || !strings.Contains(stdout, "  →   liveop-kmf (cross-repo)\n") {
		t.Fatalf("positive control: %d raw reads, stdout:\n%s\nwant 1 read and the cross-repo line", reads, stdout)
	}

	stdout, stderr, reads := render("ud-local")
	if reads != 0 {
		t.Errorf("local-only render made %d raw edge-record reads, want 0", reads)
	}
	if want := "DEPENDS ON\n" + formatDependencyLine("→", localDeps[0]) + "\n"; !strings.Contains(stdout, want) {
		t.Errorf("local-only render lacks %q:\n%s", want, stdout)
	}
	for _, label := range []string{"(cross-repo)", "(external)", "(missing)"} {
		if strings.Contains(stdout, label) {
			t.Errorf("local-only render printed an unresolved label %s:\n%s", label, stdout)
		}
	}
	if strings.Contains(stderr, "no row in this database") {
		t.Errorf("local-only render warned:\n%s", stderr)
	}

	stdout, stderr, reads = render("ud-edgeless")
	if reads != 0 {
		t.Errorf("edgeless render made %d raw edge-record reads, want 0", reads)
	}
	if strings.Contains(stdout, "DEPENDS ON") || strings.Contains(stderr, "no row in this database") {
		t.Errorf("edgeless render grew a dependency section or notice:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// faultyDepStore fails or truncates one of the three reads behind the
// unresolvable-edge disclosure, for the failure rules of #5272.
type faultyDepStore struct {
	storage.DoltStorage
	failCount, failListing, failRecords bool
	keepRecords                         int // >0: return only the first n raw records
	reads                               atomic.Int32
}

func (s *faultyDepStore) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	if s.failCount {
		return 0, errors.New("count read failed")
	}
	return s.DoltStorage.CountDependencies(ctx, issueID)
}

func (s *faultyDepStore) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	if s.failListing {
		return nil, errors.New("listing read failed")
	}
	return s.DoltStorage.GetDependenciesWithMetadata(ctx, issueID)
}

func (s *faultyDepStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	s.reads.Add(1)
	if s.failRecords {
		return nil, errors.New("raw read failed")
	}
	records, err := s.DoltStorage.GetDependencyRecords(ctx, issueID)
	if err == nil && s.keepRecords > 0 && len(records) > s.keepRecords {
		records = records[:s.keepRecords]
	}
	return records, err
}

// TestShowWatchRenderUnresolvableDepFailureRules pins #5272's failure rules on
// a subject with three unresolved edges: a failed count or listing keeps the
// base rule (no raw read, no lines, no outbound notice); a failed raw read
// prints nothing and leaves the whole measured gap to the notice; partial raw
// coverage prints what was read and the notice counts only the rest.
func TestShowWatchRenderUnresolvableDepFailureRules(t *testing.T) {
	testDB := filepath.Join(t.TempDir(), ".beads", "beads.db")
	s := newTestStoreWithPrefix(t, testDB, "ud")
	ctx := context.Background()

	subject := &types.Issue{ID: "ud-subject", Title: "Subject", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now()}
	if err := s.CreateIssue(ctx, subject, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	for _, dep := range []*types.Dependency{
		{IssueID: subject.ID, DependsOnID: "liveop-a", Type: types.DepBlocks},
		{IssueID: subject.ID, DependsOnID: "liveop-b", Type: types.DepBlocks},
		{IssueID: subject.ID, DependsOnID: "external:liveop:liveop-c", Type: types.DepTracks},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", dep.DependsOnID, err)
		}
	}
	records, err := s.GetDependencyRecords(ctx, subject.ID)
	if err != nil || len(records) != 3 {
		t.Fatalf("GetDependencyRecords = %v, %v; want the three raw edges", records, err)
	}

	oldStore, oldDBPath := store, dbPath
	t.Cleanup(func() { store, dbPath = oldStore, oldDBPath })

	for _, tc := range []struct {
		name      string
		store     *faultyDepStore
		wantReads int32
		wantLines int    // unresolved id lines printed
		wantWarn  string // "" means no outbound notice
	}{
		{"count read fails", &faultyDepStore{failCount: true}, 0, 0, ""},
		{"listing read fails", &faultyDepStore{failListing: true}, 0, 0, ""},
		{"raw read fails", &faultyDepStore{failRecords: true}, 1, 0, "has 3 dependency edge(s)"},
		{"partial raw coverage", &faultyDepStore{keepRecords: 1}, 1, 1, "has 2 dependency edge(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.store.DoltStorage = s
			store, dbPath = tc.store, testDB
			stdioMutex.Lock()
			stdout, stderr := captureStdio(t, func() {
				if got := displayShowIssueReturn(ctx, subject.ID, 0); got == nil {
					t.Errorf("displayShowIssueReturn(%s) = nil", subject.ID)
				}
			})
			stdioMutex.Unlock()

			if got := tc.store.reads.Load(); got != tc.wantReads {
				t.Errorf("raw edge-record reads = %d, want %d", got, tc.wantReads)
			}
			lines := strings.Count(stdout, " (cross-repo)\n") + strings.Count(stdout, " (external)\n")
			if lines != tc.wantLines {
				t.Errorf("printed %d unresolved id lines, want %d:\n%s", lines, tc.wantLines, stdout)
			}
			if tc.wantWarn == "" {
				if strings.Contains(stderr, "dependency edge(s)") {
					t.Errorf("outbound notice printed, want none:\n%s", stderr)
				}
			} else if !strings.Contains(stderr, tc.wantWarn) || !strings.Contains(stderr, "bd dep list") {
				t.Errorf("stderr lacks %q and the bd dep list pointer:\n%s", tc.wantWarn, stderr)
			}
		})
	}
}
