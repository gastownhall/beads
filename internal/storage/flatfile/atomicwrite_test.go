package flatfile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestWriteIssueConcurrentSamePath exercises the unique-temp-name guarantee:
// with a fixed "<path>.tmp" name, concurrent writers of the SAME issue file
// rename each other's half-written temp into place or fail ENOENT. Every
// write must succeed, the surviving file must parse, and no temp files may
// be left behind.
func TestWriteIssueConcurrentSamePath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{ID: "tmp-race", Title: "base"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			issue := &types.Issue{ID: "tmp-race", Title: fmt.Sprintf("writer %d", idx)}
			if err := s.writeIssue(issue); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("writeIssue: %v", err)
	}

	issue, err := s.readIssue("tmp-race")
	if err != nil {
		t.Fatalf("readIssue after concurrent writes: %v", err)
	}
	if !strings.HasPrefix(issue.Title, "writer ") {
		t.Errorf("title = %q, want a writer's value", issue.Title)
	}

	entries, err := os.ReadDir(s.issuesDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestTempCrashLitterGitignoredAndInvisible reproduces the crash-litter
// scenario: a process killed between temp creation and rename leaves the temp
// file behind, and the next CommitPending runs 'git add .beads/', which stages
// anything the .beads/.gitignore written at init does not cover. The earlier
// "<base>.tmp-<rand>" naming escaped that file's "*.tmp" rule (git's glob
// requires a trailing .tmp), so the junk was committed and synced to every
// peer. Oracle is git itself: the orphaned temp must be ignored under the
// exact gitignore content cmd/bd writes, and must stay invisible to
// loadAllIssues.
func TestTempCrashLitterGitignoredAndInvisible(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "TEST-1", Title: "real"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Crash between CreateTemp and rename: everything after tempFile never
	// ran, so the half-written temp simply stays in issues/.
	f, err := tempFile(filepath.Join(s.issuesDir, "TEST-1.json"))
	if err != nil {
		t.Fatalf("tempFile: %v", err)
	}
	if _, err := f.Write([]byte(`{"id":"TEST-1","title":"half-`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	litter := f.Name()

	// Invisible to issue loading: only the real issue is seen.
	issues, err := s.loadAllIssues()
	if err != nil {
		t.Fatalf("loadAllIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "TEST-1" {
		t.Errorf("loadAllIssues saw %d issue(s), want only TEST-1", len(issues))
	}

	// Ignored by the exact .beads/.gitignore that bd init and the flatfile
	// migration write (cmd/bd/init.go, cmd/bd/migrate_flatfile.go).
	repo := filepath.Dir(s.beadsDir)
	initCmd := exec.Command("git", "init", "-q")
	initCmd.Dir = repo
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitignore := "local_metadata.json\n*.tmp\nmerge_slot.json\nrepo_mtime.json\n"
	if err := os.WriteFile(filepath.Join(s.beadsDir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	check := exec.Command("git", "check-ignore", "-q", litter)
	check.Dir = repo
	if err := check.Run(); err != nil {
		t.Errorf("crash litter %s is not gitignored: 'git add .beads/' would stage and sync it", filepath.Base(litter))
	}
}
