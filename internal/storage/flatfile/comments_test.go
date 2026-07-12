package flatfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// SQL reference semantics: every AddIssueComment inserts a distinct row —
// comments created in the same clock tick can never overwrite each other.
// The flatfile mechanism is the comment ID embedded in the filename; two
// distinct IDs always yield two distinct paths regardless of timestamp.
func TestAddIssueCommentFilenameIncludesID(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "cmt-1", Title: "Comments"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	c1, err := s.AddIssueComment(ctx, "cmt-1", "alice", "first")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	c2, err := s.AddIssueComment(ctx, "cmt-1", "bob", "second")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	dir, err := safePath(s.commentsDir, "cmt-1", "")
	if err != nil {
		t.Fatalf("safePath: %v", err)
	}
	entries, err := readDirSafe(dir)
	if err != nil {
		t.Fatalf("readDirSafe: %v", err)
	}
	for _, c := range []*types.Comment{c1, c2} {
		found := false
		for _, e := range entries {
			if strings.Contains(e.Name(), sanitizeFilenameComponent(c.ID)) {
				found = true
			}
		}
		if !found {
			t.Errorf("no comment file embeds ID %q; timestamp-only names collide within one clock tick", c.ID)
		}
	}

	comments, err := s.GetIssueComments(ctx, "cmt-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
}

// SQL FK parity: a comment insert racing the issue's cascade delete either
// lands before the delete (and is cascaded away) or fails with not-found —
// it can never resurrect comments/<id>/ and leave an orphan comment for a
// deleted issue. Pre-fix, AddIssueComment's existence check and write ran
// outside writeMu, so DeleteIssue could slip between them.
func TestAddIssueCommentRacingDeleteLeavesNoOrphan(t *testing.T) {
	s := newTestStore(t)

	for i := range 50 {
		id := fmt.Sprintf("race-%d", i)
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "racer"}, "t"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.AddIssueComment(ctx, id, "t", "racing comment"); err != nil && !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("AddIssueComment(%s): %v", id, err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			if err := s.DeleteIssue(ctx, id); err != nil {
				t.Errorf("DeleteIssue(%s): %v", id, err)
			}
		}()
		close(start)
		wg.Wait()

		dir := filepath.Join(s.commentsDir, id)
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read comments dir %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Fatalf("iteration %d: orphan comment files for deleted issue: %d", i, len(entries))
		}
	}
}
