package flatfile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddIssueComment creates a new comment file under comments/{issueID}/.
// The existence check and the comment write run under lockWrite, so a
// concurrent DeleteIssue cascade cannot slip between them and leave an
// orphan comment for a deleted issue (SQL FK parity).
func (s *FlatFileStore) AddIssueComment(_ context.Context, issueID, author, text string) (*types.Comment, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	defer s.lockWrite()()

	// Verify issue exists.
	if _, err := s.readIssue(issueID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	seq := s.commentSeq.Add(1)
	comment := &types.Comment{
		ID:        fmt.Sprintf("%d-%d", now.UnixNano(), seq),
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: now,
	}

	// writeCommentFile embeds the comment ID in the filename, so two
	// comments created in the same clock tick cannot overwrite each other.
	if err := s.writeCommentFile(comment); err != nil {
		return nil, err
	}

	return comment, nil
}

// GetIssueComments returns all comments for an issue, sorted by created_at.
func (s *FlatFileStore) GetIssueComments(_ context.Context, issueID string) ([]*types.Comment, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	dir, err := safePath(s.commentsDir, issueID, "")
	if err != nil {
		return nil, err
	}
	entries, err := readDirSafe(dir)
	if err != nil {
		return nil, fmt.Errorf("flatfile: read comments dir: %w", err)
	}

	var comments []*types.Comment
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("flatfile: read comment %s: %w", e.Name(), err)
		}
		var c types.Comment
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("flatfile: unmarshal comment %s: %w", e.Name(), err)
		}
		comments = append(comments, &c)
	}

	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	return comments, nil
}

// CountIssueComments returns the number of comments for an issue. Mirrors the
// SQL surface's intentional asymmetry: the count queries only the durable
// `comments` table and is never wisp-routed, so a wisp always reports 0 even
// though GetIssueComments returns its comments.
func (s *FlatFileStore) CountIssueComments(_ context.Context, issueID string) (int64, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}

	if issue, err := s.readIssue(issueID); err == nil && issueops.IsWisp(issue) {
		return 0, nil
	}

	count, err := s.countCommentFiles(issueID)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}
