package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// renameRefsFakeStore serves issues from a slice and records the UpdateIssue
// calls the reference scan makes. The DoltStorage embed only satisfies the
// parameter's type: the scan must not call anything else on the store, and on
// this fake anything else would panic through the nil embed.
type renameRefsFakeStore struct {
	storage.DoltStorage
	issues  []*types.Issue
	updates []renameRefsFakeUpdate
	// failUpdate names issues whose UpdateIssue fails after the call is
	// recorded, like a commit whose response was lost; listErr fails the
	// listing outright.
	failUpdate map[string]error
	listErr    error
}

// renameRefsFakeUpdate is one UpdateIssue call the fake recorded.
type renameRefsFakeUpdate struct {
	id      string
	updates map[string]interface{}
}

func (s *renameRefsFakeStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.issues, nil
}

func (s *renameRefsFakeStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	s.updates = append(s.updates, renameRefsFakeUpdate{id: id, updates: updates})
	if err, ok := s.failUpdate[id]; ok {
		return err
	}
	return nil
}

// update returns the value the scan wrote to field on id, or nil when it made
// no such call.
func (s *renameRefsFakeStore) update(id, field string) interface{} {
	for _, u := range s.updates {
		if u.id == id {
			return u.updates[field]
		}
	}
	return nil
}

func TestUpdateReferencesInAllIssues(t *testing.T) {
	t.Run("rewrites close_reason on another issue", func(t *testing.T) {
		// GH#7040: a closed issue's close_reason kept naming the old ID.
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-other", Title: "Other", Status: types.StatusClosed, CloseReason: "duplicate of bd-old"},
			},
		}
		if err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test"); err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		if got := s.update("bd-other", "close_reason"); got != "duplicate of bd-new" {
			t.Errorf("close_reason update = %v, want %q", got, "duplicate of bd-new")
		}
	})

	t.Run("rewrites the renamed issue's own text", func(t *testing.T) {
		// The identity rename carries the issue's text over verbatim, so the
		// scan must also rewrite the issue itself, once, under its new ID.
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{
					ID:                 "bd-new",
					Title:              "bd-old follow-up",
					Description:        "split out of bd-old",
					Design:             "per bd-old's design",
					Notes:              "see bd-old history",
					AcceptanceCriteria: "bd-old behavior kept",
					Status:             types.StatusClosed,
					CloseReason:        "superseded by bd-old",
				},
			},
		}
		if err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test"); err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		for field, want := range map[string]string{
			"title":               "bd-new follow-up",
			"description":         "split out of bd-new",
			"design":              "per bd-new's design",
			"notes":               "see bd-new history",
			"acceptance_criteria": "bd-new behavior kept",
			"close_reason":        "superseded by bd-new",
		} {
			if got := s.update("bd-new", field); got != want {
				t.Errorf("%s update = %v, want %q", field, got, want)
			}
		}
	})

	t.Run("preserves longer IDs that contain the old one", func(t *testing.T) {
		// \b word boundaries do not fall at ID boundaries: renaming bd-old
		// used to corrupt bd-old-v2 and rewrite inside dotted children.
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-x", Description: "bd-old-v2 and pre-bd-old and bd-old.1 track bd-old"},
			},
		}
		if err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test"); err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		const want = "bd-old-v2 and pre-bd-old and bd-old.1 track bd-new"
		if got := s.update("bd-x", "description"); got != want {
			t.Errorf("description update = %v, want %q", got, want)
		}
	})

	t.Run("makes no update call for fields without matches", func(t *testing.T) {
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-clean", Title: "No references", Description: "at all"},
			},
		}
		if err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test"); err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		if len(s.updates) != 0 {
			t.Errorf("UpdateIssue calls = %v, want none", s.updates)
		}
	})

	t.Run("report lists updated issues and fields in order", func(t *testing.T) {
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-b", Description: "refs bd-old", CloseReason: "dup of bd-old"},
				{ID: "bd-a", Title: "bd-old again"},
				{ID: "bd-new", Notes: "was bd-old"},
			},
		}
		rep := scanRenameReferences(context.Background(), s, "bd-old", "bd-new", "test")
		if len(rep.updated) != 3 {
			t.Fatalf("updated = %+v, want three issues", rep.updated)
		}
		want := []struct{ id, fields string }{
			{"bd-a", "title"},
			{"bd-b", "description, close_reason"},
			{"bd-new", "notes"},
		}
		for i, w := range want {
			if rep.updated[i].issueID != w.id {
				t.Errorf("updated[%d] = %s, want %s", i, rep.updated[i].issueID, w.id)
			}
			if got := strings.Join(rep.updated[i].fields, ", "); got != w.fields {
				t.Errorf("updated[%d] fields = %q, want %q", i, got, w.fields)
			}
		}
	})

	t.Run("wrapper joins update failures without failing the scan", func(t *testing.T) {
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-a", Title: "refs bd-old"},
				{ID: "bd-b", Notes: "refs bd-old"},
			},
			failUpdate: map[string]error{"bd-a": errors.New("boom")},
		}
		err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test")
		if err == nil || !strings.Contains(err.Error(), "bd-a") {
			t.Errorf("error = %v, want bd-a's failure joined", err)
		}
		if got := s.update("bd-b", "notes"); got != "refs bd-new" {
			t.Errorf("bd-b notes update = %v, want the scan to continue", got)
		}
	})
}

func TestPrintRenameReferenceReport(t *testing.T) {
	t.Run("lists updated issues and fields", func(t *testing.T) {
		rep := &renameReferenceReport{
			updated: []renameRefUpdated{
				{issueID: "bd-a", fields: []string{"title"}},
				{issueID: "bd-b", fields: []string{"description", "close_reason"}},
			},
		}
		var buf bytes.Buffer
		printRenameReferenceReport(&buf, rep)
		want := "Updated references:\n  bd-a: title\n  bd-b: description, close_reason\n"
		if buf.String() != want {
			t.Errorf("report = %q, want %q", buf.String(), want)
		}
	})

	t.Run("empty report prints nothing", func(t *testing.T) {
		var buf bytes.Buffer
		printRenameReferenceReport(&buf, &renameReferenceReport{})
		if buf.Len() != 0 {
			t.Errorf("empty report printed %q, want nothing", buf.String())
		}
	})

	t.Run("reports failed updates and keeps scanning", func(t *testing.T) {
		injected := errors.New("injected update failure")
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-a", Title: "refs bd-old"},
				{ID: "bd-b", Description: "refs bd-old too"},
			},
			failUpdate: map[string]error{"bd-a": injected},
		}
		rep := scanRenameReferences(context.Background(), s, "bd-old", "bd-new", "test")

		// The later issue is still updated: one failure must not stop
		// the independent updates.
		if got := s.update("bd-b", "description"); got != "refs bd-new too" {
			t.Errorf("bd-b description update = %v, want the rewrite to continue past bd-a's failure", got)
		}

		// The failed update is reported as failed, never as success: a
		// storage error may follow a committed write.
		if len(rep.updated) != 1 || rep.updated[0].issueID != "bd-b" {
			t.Errorf("updated = %+v, want only bd-b", rep.updated)
		}
		if len(rep.failed) != 1 {
			t.Fatalf("failed = %+v, want bd-a's failure", rep.failed)
		}
		if rep.failed[0].issueID != "bd-a" || rep.failed[0].err != injected {
			t.Errorf("failed[0] = %+v, want bd-a with the injected error", rep.failed[0])
		}
		if got := strings.Join(rep.failed[0].fields, ", "); got != "title" {
			t.Errorf("failed[0] fields = %q, want %q", got, "title")
		}

		var buf bytes.Buffer
		printRenameReferenceReport(&buf, rep)
		for _, want := range []string{
			"Warning: could not rewrite some references (the rename itself succeeded):",
			"  bd-a (title): injected update failure",
		} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("report %q does not contain %q", buf.String(), want)
			}
		}
	})

	t.Run("reports an unchecked scan when listing fails", func(t *testing.T) {
		s := &renameRefsFakeStore{listErr: errors.New("injected listing failure")}
		rep := scanRenameReferences(context.Background(), s, "bd-old", "bd-new", "test")
		if rep.issuesErr == nil {
			t.Fatalf("issuesErr = nil, want the listing failure recorded")
		}
		var buf bytes.Buffer
		printRenameReferenceReport(&buf, rep)
		want := "Warning: references could not be checked: failed to list issues: injected listing failure"
		if !strings.Contains(buf.String(), want) {
			t.Errorf("report %q does not contain %q", buf.String(), want)
		}
	})
}

func TestRenameIDReferences(t *testing.T) {
	renames := []struct {
		name   string
		oldID  string
		newID  string
		input  string
		output string
	}{
		{name: "standalone", oldID: "bd-old", newID: "bd-new", input: "see bd-old here", output: "see bd-new here"},
		{name: "repeated and adjacent", oldID: "bd-old", newID: "bd-new", input: "bd-old,bd-old; bd-old bd-old", output: "bd-new,bd-new; bd-new bd-new"},
		{name: "parentheses and backticks", oldID: "bd-old", newID: "bd-new", input: "(bd-old) and `bd-old`", output: "(bd-new) and `bd-new`"},
		{name: "possessive", oldID: "bd-old", newID: "bd-new", input: "bd-old's design", output: "bd-new's design"},
		{name: "url path segment", oldID: "bd-old", newID: "bd-new", input: "https://host/bd-old/path", output: "https://host/bd-new/path"},
		{name: "colon-qualified", oldID: "bd-old", newID: "bd-new", input: "ref:bd-old:", output: "ref:bd-new:"},
		{name: "sentence period kept", oldID: "bd-old", newID: "bd-new", input: "see bd-old.", output: "see bd-new."},
		{name: "ellipsis kept", oldID: "bd-old", newID: "bd-new", input: "bd-old... done", output: "bd-new... done"},
		{name: "dotted child preserved", oldID: "bd-old", newID: "bd-new", input: "bd-old.1 is a child", output: "bd-old.1 is a child"},
		{name: "nested dotted child preserved", oldID: "bd-old", newID: "bd-new", input: "bd-old.1.2 nested", output: "bd-old.1.2 nested"},
		{name: "hyphenated superstring preserved", oldID: "bd-old", newID: "bd-new", input: "bd-old-v2 upgraded", output: "bd-old-v2 upgraded"},
		{name: "leading prefix preserved", oldID: "bd-old", newID: "bd-new", input: "pre-bd-old mentioned", output: "pre-bd-old mentioned"},
		{name: "underscore adjacency preserved", oldID: "bd-old", newID: "bd-new", input: "bd-old_x literal", output: "bd-old_x literal"},
		{name: "trailing hyphen is part of the ID", oldID: "bd-old", newID: "bd-new", input: "bd-old- literal", output: "bd-old- literal"},
		{name: "case-sensitive", oldID: "bd-old", newID: "bd-new", input: "BD-OLD stays", output: "BD-OLD stays"},
		{name: "no match", oldID: "bd-old", newID: "bd-new", input: "nothing here", output: "nothing here"},
		{name: "empty text", oldID: "bd-old", newID: "bd-new", input: "", output: ""},
		{name: "hyphenated superstring with interior match", oldID: "bd-auth", newID: "bd-login", input: "bd-auth-v2 grew out of bd-auth", output: "bd-auth-v2 grew out of bd-login"},
		{name: "leading prefix with interior match", oldID: "vscode-1", newID: "vscode-2", input: "beads-vscode-1 wraps vscode-1", output: "beads-vscode-1 wraps vscode-2"},
		{name: "new ID containing the old one", oldID: "bd-old", newID: "bd-old-2", input: "bd-old and bd-old-2", output: "bd-old-2 and bd-old-2"},
	}
	for _, tt := range renames {
		t.Run(tt.name, func(t *testing.T) {
			if got := replaceIDReferences(tt.input, tt.oldID, tt.newID); got != tt.output {
				t.Errorf("replaceIDReferences(%q, %q, %q) = %q, want %q", tt.input, tt.oldID, tt.newID, got, tt.output)
			}
		})
	}
}
