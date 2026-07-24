//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// cpIDs projects comment IDs in result order.
func cpIDs(cs []*types.Comment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func cpEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGetIssueCommentsPageEmbedded is the embedded-backend twin of the dolt
// suite's page-walk property: a keyset walk with a page size smaller than a
// same-second group reproduces GetIssueComments exactly (order + content, no
// drop/dup), and the wisp route pages wisp_comments. Comments are seeded through
// the public ImportIssueComment with a controlled created_at so several share a
// second, exercising the (created_at, id) tie-break; the external test package
// shares no connection with the store, so writes go through store ops.
func TestGetIssueCommentsPageEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "cp")
	ctx := t.Context()

	iss := &types.Issue{ID: "cp-1", Title: "cp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// 25 comments in 5 same-second groups of 5 (group size > page size 4).
	const n, groupSize, pageSize = 25, 5, 4
	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i/groupSize) * time.Second)
		if _, err := te.store.ImportIssueComment(ctx, iss.ID, "tester", fmt.Sprintf("c%d", i), at); err != nil {
			t.Fatalf("ImportIssueComment %d: %v", i, err)
		}
	}

	full, err := te.store.GetIssueComments(ctx, iss.ID)
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(full) != n {
		t.Fatalf("full read = %d comments, want %d", len(full), n)
	}

	// A same-second collision was actually exercised (adjacent equal created_at).
	sawCollision := false
	for i := 1; i < len(full); i++ {
		if full[i].CreatedAt.Equal(full[i-1].CreatedAt) {
			sawCollision = true
			if full[i].ID <= full[i-1].ID {
				t.Fatalf("same-second group not id-ascending at %d: %q <= %q", i, full[i].ID, full[i-1].ID)
			}
		}
	}
	if !sawCollision {
		t.Fatalf("no same-second created_at collision in the seeded thread — tie-break not exercised")
	}

	// Keyset walk (page size < group size) equals the full read exactly.
	var collected []string
	seen := map[string]bool{}
	var after storage.CommentPageCursor
	for i := 0; i < 1000; i++ {
		page, err := te.store.GetIssueCommentsPage(ctx, iss.ID, after, pageSize)
		if err != nil {
			t.Fatalf("GetIssueCommentsPage(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > pageSize {
			t.Fatalf("page %d size = %d, want <= %d", i, len(page), pageSize)
		}
		for _, c := range page {
			if seen[c.ID] {
				t.Fatalf("duplicate id %q across pages", c.ID)
			}
			seen[c.ID] = true
			collected = append(collected, c.ID)
		}
		last := page[len(page)-1]
		after = storage.CommentPageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if want := cpIDs(full); !cpEqual(collected, want) {
		t.Fatalf("keyset walk = %v,\nwant full read %v", collected, want)
	}

	// First page (zero cursor) is the first pageSize in order; a cursor on the
	// last comment yields an empty page.
	first, err := te.store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{}, pageSize)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(first): %v", err)
	}
	if got, want := cpIDs(first), cpIDs(full)[:pageSize]; !cpEqual(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}
	lastC := full[len(full)-1]
	end, err := te.store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{CreatedAt: lastC.CreatedAt, ID: lastC.ID}, pageSize)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(past-the-end): %v", err)
	}
	if len(end) != 0 {
		t.Fatalf("past-the-end page = %v, want empty", cpIDs(end))
	}

	// Empty thread -> empty page.
	empty := &types.Issue{ID: "cp-empty", Title: "cp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, empty, "tester"); err != nil {
		t.Fatalf("CreateIssue(empty): %v", err)
	}
	emptyPage, err := te.store.GetIssueCommentsPage(ctx, empty.ID, storage.CommentPageCursor{}, 10)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(empty): %v", err)
	}
	if len(emptyPage) != 0 {
		t.Fatalf("empty thread page = %v, want empty", cpIDs(emptyPage))
	}

	// Wisp route: an active wisp pages wisp_comments and matches its full read.
	wisp := &types.Issue{ID: "cp-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue(wisp): %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := te.store.ImportIssueComment(ctx, wisp.ID, "tester", fmt.Sprintf("w%d", i), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("ImportIssueComment(wisp) %d: %v", i, err)
		}
	}
	wispFull, err := te.store.GetIssueComments(ctx, wisp.ID)
	if err != nil {
		t.Fatalf("GetIssueComments(wisp): %v", err)
	}
	if len(wispFull) != 6 {
		t.Fatalf("wisp full read = %d, want 6", len(wispFull))
	}
	var wispWalk []string
	after = storage.CommentPageCursor{}
	for i := 0; i < 1000; i++ {
		page, err := te.store.GetIssueCommentsPage(ctx, wisp.ID, after, 2)
		if err != nil {
			t.Fatalf("GetIssueCommentsPage(wisp page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			wispWalk = append(wispWalk, c.ID)
		}
		last := page[len(page)-1]
		after = storage.CommentPageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if want := cpIDs(wispFull); !cpEqual(wispWalk, want) {
		t.Fatalf("wisp keyset walk = %v, want %v", wispWalk, want)
	}
}
