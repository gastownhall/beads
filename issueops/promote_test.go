package issueops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// promoteFakes is the three roles issueops.Promote composes. Each embeds its
// interface so a method the entry must not call panics on the nil.
type promoteFakes struct {
	issue      *issueops.Issue
	getErr     error
	updateErr  error
	unchanged  bool
	commentErr error

	updates  []issueops.UpdateRequest
	comments []issueops.AddCommentRequest
}

type promoteReader struct {
	issueops.Reader
	f *promoteFakes
}

func (r promoteReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	if r.f.getErr != nil {
		return nil, r.f.getErr
	}
	if r.f.issue == nil || r.f.issue.ID != req.ID {
		return nil, issueops.ErrNotFound
	}
	return &issueops.IssueDetails{Issue: *r.f.issue}, nil
}

type promoteLifecycle struct {
	issueops.Lifecycle
	f *promoteFakes
}

func (l promoteLifecycle) Update(_ context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	l.f.updates = append(l.f.updates, req)
	if l.f.updateErr != nil {
		return issueops.UpdateResult{}, l.f.updateErr
	}
	promoted := *l.f.issue
	promoted.Ephemeral = false
	return issueops.UpdateResult{Issue: &promoted, Changed: !l.f.unchanged}, nil
}

type promoteCommenter struct {
	issueops.Commenter
	f *promoteFakes
}

func (c promoteCommenter) AddComment(_ context.Context, req issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	c.f.comments = append(c.f.comments, req)
	if c.f.commentErr != nil {
		return issueops.AddCommentResult{}, c.f.commentErr
	}
	return issueops.AddCommentResult{Comment: &issueops.Comment{IssueID: req.IssueID, Author: req.Author, Text: req.Text}}, nil
}

func (f *promoteFakes) roles() issueops.PromoteRoles {
	return issueops.PromoteRoles{
		Reader:    promoteReader{f: f},
		Lifecycle: promoteLifecycle{f: f},
		Commenter: promoteCommenter{f: f},
	}
}

func wisp(id string) *issueops.Issue {
	return &issueops.Issue{ID: id, Title: "a wisp", Ephemeral: true}
}

func TestPromoteMovesTheWispThroughOnePersistenceUpdateThenComments(t *testing.T) {
	f := &promoteFakes{issue: wisp("bd-w1")}
	res, err := issueops.Promote(t.Context(), f.roles(), issueops.PromoteRequest{
		Actor: "alice", IssueID: "bd-w1", Reason: "keep it", Provenance: "bd: promote bd-w1",
	})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(f.updates) != 1 {
		t.Fatalf("updates = %d, want exactly one", len(f.updates))
	}
	u := f.updates[0]
	if u.Actor != "alice" || u.IssueID != "bd-w1" || u.Provenance != "bd: promote bd-w1" {
		t.Errorf("update request = %+v, want actor alice, id bd-w1 and the caller's provenance", u)
	}
	if !u.Patch.Persistence.Set || u.Patch.Persistence.Value != issueops.PersistenceModePersistent {
		t.Errorf("update patch persistence = %+v, want set to persistent", u.Patch.Persistence)
	}
	// The update is the plane move and nothing else.
	only := issueops.IssuePatch{Persistence: u.Patch.Persistence}
	if !reflect.DeepEqual(u.Patch, only) {
		t.Errorf("update patch carries more than the persistence move: %+v", u.Patch)
	}
	if len(f.comments) != 1 {
		t.Fatalf("comments = %d, want exactly one", len(f.comments))
	}
	c := f.comments[0]
	if c.Author != "alice" || c.IssueID != "bd-w1" || c.Text != "Promoted from Level 0: keep it" {
		t.Errorf("comment = %+v, want alice's promotion comment with the reason", c)
	}
	if res.Issue == nil || res.Issue.Ephemeral || res.CommentErr != nil {
		t.Errorf("result = %+v, want the promoted row and no comment error", res)
	}
}

func TestPromotionCommentWithoutAReason(t *testing.T) {
	if got := issueops.PromotionComment(""); got != "Promoted from Level 0" {
		t.Errorf("PromotionComment(\"\") = %q", got)
	}
}

func TestPromoteRefusesARowThatIsNotAWispBeforeWriting(t *testing.T) {
	f := &promoteFakes{issue: &issueops.Issue{ID: "bd-1", Title: "durable"}}
	_, err := issueops.Promote(t.Context(), f.roles(), issueops.PromoteRequest{Actor: "alice", IssueID: "bd-1"})
	var notWisp *issueops.NotAWispError
	if !errors.As(err, &notWisp) || notWisp.ID != "bd-1" {
		t.Fatalf("err = %v, want *NotAWispError for bd-1", err)
	}
	if err.Error() != "bd-1 is not a wisp (already persistent)" {
		t.Errorf("message = %q, want the sentence bd promote always printed", err.Error())
	}
	if len(f.updates) != 0 || len(f.comments) != 0 {
		t.Errorf("wrote %d updates and %d comments, want none", len(f.updates), len(f.comments))
	}
}

func TestPromoteReportsALostRaceAsNotAWisp(t *testing.T) {
	// Another writer promoted the row between the read and the update: the
	// update is a no-op, and no second promotion comment is written.
	f := &promoteFakes{issue: wisp("bd-w2"), unchanged: true}
	_, err := issueops.Promote(t.Context(), f.roles(), issueops.PromoteRequest{Actor: "alice", IssueID: "bd-w2"})
	var notWisp *issueops.NotAWispError
	if !errors.As(err, &notWisp) {
		t.Fatalf("err = %v, want *NotAWispError", err)
	}
	if len(f.comments) != 0 {
		t.Errorf("wrote %d comments after a no-op promote, want none", len(f.comments))
	}
}

func TestPromotePassesRefusalsThroughUnchanged(t *testing.T) {
	refusal := &issueops.ErrUnsupported{Op: "Lifecycle.Update", Backend: "http", Capability: "issues.update.persistence"}
	for name, f := range map[string]*promoteFakes{
		"missing": {},
		"get":     {issue: wisp("bd-w3"), getErr: errors.New("boom")},
		"update":  {issue: wisp("bd-w3"), updateErr: refusal},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := issueops.Promote(t.Context(), f.roles(), issueops.PromoteRequest{Actor: "alice", IssueID: "bd-w3"})
			if err == nil {
				t.Fatal("no error")
			}
			switch name {
			case "missing":
				if !errors.Is(err, issueops.ErrNotFound) {
					t.Errorf("err = %v, want ErrNotFound", err)
				}
			case "update":
				var unsup *issueops.ErrUnsupported
				if !errors.As(err, &unsup) || unsup.Capability != "issues.update.persistence" {
					t.Errorf("err = %v, want the capability refusal unchanged", err)
				}
			}
			if len(f.comments) != 0 {
				t.Errorf("commented after a failed promote")
			}
		})
	}
}

func TestPromoteKeepsThePromotionWhenTheCommentFails(t *testing.T) {
	f := &promoteFakes{issue: wisp("bd-w4"), commentErr: errors.New("comment down")}
	res, err := issueops.Promote(t.Context(), f.roles(), issueops.PromoteRequest{Actor: "alice", IssueID: "bd-w4"})
	if err != nil {
		t.Fatalf("Promote: %v, want the promotion to stand", err)
	}
	if res.CommentErr == nil || res.Issue == nil {
		t.Errorf("result = %+v, want the promoted row and the comment error", res)
	}
}

func TestPromoteValidatesTheRequest(t *testing.T) {
	for name, req := range map[string]issueops.PromoteRequest{
		"no actor": {IssueID: "bd-w5"},
		"no id":    {Actor: "alice"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &promoteFakes{issue: wisp("bd-w5")}
			_, err := issueops.Promote(t.Context(), f.roles(), req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if len(f.updates) != 0 {
				t.Errorf("updated on an invalid request")
			}
		})
	}
}
