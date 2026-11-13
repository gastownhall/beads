package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The fakes below follow the package's established pattern: the interface is
// embedded nil so a call the test did not intend panics rather than passing
// quietly, and exactly the methods the create body reaches are overridden.

type gateCounterRepo struct {
	ChildCounterSQLRepository
	nexted     []string
	advanced   []string
	advanceErr error
}

func (r *gateCounterRepo) NextChildID(_ context.Context, parentID string, _ ChildCounterOpts) (string, error) {
	r.nexted = append(r.nexted, parentID)
	return parentID + ".1", nil
}

func (r *gateCounterRepo) AdvanceForChild(_ context.Context, childID string) error {
	r.advanced = append(r.advanced, childID)
	return r.advanceErr
}

type gateIssueRepo struct {
	IssueSQLRepository
	inserted  []string
	insertErr error
}

func (r *gateIssueRepo) Insert(_ context.Context, issue *types.Issue, _ string, _ InsertIssueOpts) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.inserted = append(r.inserted, issue.ID)
	return nil
}

type gateDepRepo struct {
	DependencySQLRepository
	inserted []string
}

func (r *gateDepRepo) Insert(_ context.Context, dep *types.Dependency, _ string, _ DepInsertOpts) error {
	r.inserted = append(r.inserted, dep.IssueID)
	return nil
}

type gateLabelRepo struct {
	LabelSQLRepository
	inserted []string
}

func (r *gateLabelRepo) Insert(_ context.Context, issueID, label, _ string, _ LabelOpts) error {
	r.inserted = append(r.inserted, issueID+":"+label)
	return nil
}

func newGatedUseCase(counter *gateCounterRepo, issue *gateIssueRepo) *issueUseCaseImpl {
	return &issueUseCaseImpl{
		issueRepo:   issue,
		depRepo:     &gateDepRepo{},
		labelRepo:   &gateLabelRepo{},
		counterRepo: counter,
	}
}

// TestCallerNamedChildCounterGating pins WHERE the shared create body advances
// the parent's child counter for a caller-named child (bd-qp2au6, #4750):
// after a successful insert, for both caller-named forms (ExplicitID and a
// preset Issue.ID), never for a minted or failed one, and with the advance's
// error stopping the create before any later write.
func TestCallerNamedChildCounterGating(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit id advances after insert", func(t *testing.T) {
		counter, issue := &gateCounterRepo{}, &gateIssueRepo{}
		u := newGatedUseCase(counter, issue)
		if _, err := u.create(ctx, CreateIssueParams{
			Issue:      &types.Issue{Title: "named", IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "bd-gate.8",
		}, "tester", false); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got, want := strings.Join(counter.advanced, ","), "bd-gate.8"; got != want {
			t.Fatalf("advanced %q, want %q", got, want)
		}
		if len(counter.nexted) != 0 || len(issue.inserted) != 1 {
			t.Fatalf("nexted=%v inserted=%v, want no mint and one insert", counter.nexted, issue.inserted)
		}
	})

	t.Run("preset issue id advances after insert", func(t *testing.T) {
		counter, issue := &gateCounterRepo{}, &gateIssueRepo{}
		u := newGatedUseCase(counter, issue)
		if _, err := u.create(ctx, CreateIssueParams{
			Issue: &types.Issue{ID: "bd-gate2.4", Title: "preset", IssueType: types.TypeTask, Priority: 2},
		}, "tester", false); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got, want := strings.Join(counter.advanced, ","), "bd-gate2.4"; got != want {
			t.Fatalf("advanced %q, want %q", got, want)
		}
	})

	t.Run("minted child is not advanced again", func(t *testing.T) {
		counter, issue := &gateCounterRepo{}, &gateIssueRepo{}
		u := newGatedUseCase(counter, issue)
		if _, err := u.create(ctx, CreateIssueParams{
			Issue:    &types.Issue{Title: "minted", IssueType: types.TypeTask, Priority: 2},
			ParentID: "bd-gate3",
		}, "tester", false); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(counter.advanced) != 0 {
			t.Fatalf("advanced %v, want none: the mint already moved the counter", counter.advanced)
		}
		if got, want := strings.Join(counter.nexted, ","), "bd-gate3"; got != want {
			t.Fatalf("nexted %q, want %q", got, want)
		}
		if len(issue.inserted) != 1 {
			t.Fatalf("inserted %v, want the minted child", issue.inserted)
		}
	})

	t.Run("failed insert never advances", func(t *testing.T) {
		counter, issue := &gateCounterRepo{}, &gateIssueRepo{insertErr: errors.New("insert refused")}
		u := newGatedUseCase(counter, issue)
		if _, err := u.create(ctx, CreateIssueParams{
			Issue:      &types.Issue{Title: "named", IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "bd-gate4.8",
		}, "tester", false); err == nil {
			t.Fatal("create must fail with the insert's error")
		}
		if len(counter.advanced) != 0 {
			t.Fatalf("advanced %v, want none: a refused insert owes no counter", counter.advanced)
		}
	})

	t.Run("advance failure stops the create before later writes", func(t *testing.T) {
		counter := &gateCounterRepo{advanceErr: errors.New("counter reconcile refused")}
		issue := &gateIssueRepo{}
		labels := &gateLabelRepo{}
		u := &issueUseCaseImpl{issueRepo: issue, depRepo: &gateDepRepo{}, labelRepo: labels, counterRepo: counter}
		_, err := u.create(ctx, CreateIssueParams{
			Issue:      &types.Issue{Title: "named", IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "bd-gate5.8",
			Labels:     []string{"late"},
		}, "tester", false)
		if err == nil {
			t.Fatal("create must fail with the advance's error")
		}
		if !strings.Contains(err.Error(), "bd-gate5.8") {
			t.Fatalf("create error %q must name the child whose counter failed", err)
		}
		if len(labels.inserted) != 0 {
			t.Fatalf("labels %v inserted, want none: the create stops at the advance", labels.inserted)
		}
	})
}
