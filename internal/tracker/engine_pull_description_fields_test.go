package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// foldDescriptionFields renders an issue's description text the way a
// tracker that carries acceptance criteria, design and notes inside the
// description would push it.
func foldDescriptionFields(issue *types.Issue) string {
	text := issue.Description
	if issue.AcceptanceCriteria != "" {
		text += "\n## Acceptance Criteria\n" + issue.AcceptanceCriteria
	}
	if issue.Design != "" {
		text += "\n## Design\n" + issue.Design
	}
	if issue.Notes != "" {
		text += "\n## Notes\n" + issue.Notes
	}
	return text
}

// descriptionFieldsTracker is a mock tracker implementing
// DescriptionFieldsTracker.
type descriptionFieldsTracker struct {
	*mockTracker
}

func (descriptionFieldsTracker) DescriptionFieldsEqual(a, b *types.Issue) bool {
	return foldDescriptionFields(a) == foldDescriptionFields(b)
}

// recordingFieldsTransaction applies and records the updates a pull writes,
// including the acceptance criteria, design and notes pullTestTransaction
// ignores.
type recordingFieldsTransaction struct {
	*pullTestTransaction
	updates []map[string]interface{}
}

func (t *recordingFieldsTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	recorded := make(map[string]interface{}, len(updates))
	for key, value := range updates {
		recorded[key] = value
	}
	t.updates = append(t.updates, recorded)
	if text, ok := updates["acceptance_criteria"].(string); ok {
		t.issue.AcceptanceCriteria = text
	}
	if text, ok := updates["design"].(string); ok {
		t.issue.Design = text
	}
	if text, ok := updates["notes"].(string); ok {
		t.issue.Notes = text
	}
	return t.pullTestTransaction.UpdateIssue(ctx, id, updates, actor)
}

type descriptionFieldsStore struct {
	*pureTestStore
	tx *recordingFieldsTransaction
}

func (s *descriptionFieldsStore) RunInIssueLifecycleTransaction(_ context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	return fn(s.tx)
}

func (s *descriptionFieldsStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue, nil
		}
	}
	return nil, nil
}

const descriptionFieldsRef = "https://test.test/EXT-1"

// newDescriptionFieldsLocal returns a linked local issue holding every
// description field.
func newDescriptionFieldsLocal() *types.Issue {
	ref := descriptionFieldsRef
	return &types.Issue{
		ID:                 "bd-1",
		Title:              "Fields",
		Description:        "Body",
		AcceptanceCriteria: "It works",
		Design:             "Plain",
		Notes:              "Remember",
		Priority:           2,
		Status:             types.StatusOpen,
		IssueType:          types.TypeTask,
		ExternalRef:        &ref,
		UpdatedAt:          time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

// runDescriptionFieldsPull pulls remote, which the mapper converts to
// pulled, into a store holding local, and returns the updates it wrote.
func runDescriptionFieldsPull(t *testing.T, capability bool, local, pulled *types.Issue) []map[string]interface{} {
	t.Helper()
	ctx := context.Background()
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{
		ID:          "ext-1",
		Identifier:  "EXT-1",
		URL:         descriptionFieldsRef,
		Title:       pulled.Title,
		Description: foldDescriptionFields(pulled),
		Priority:    2,
		State:       types.StatusOpen,
		UpdatedAt:   time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
	}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		issue := *pulled
		issue.ID, issue.ExternalRef = "", nil
		return &IssueConversion{Issue: &issue}
	}}
	var tr IssueTracker = mock
	if capability {
		tr = descriptionFieldsTracker{mock}
	}
	store := &descriptionFieldsStore{
		pureTestStore: newPureTestStore(local),
		tx:            &recordingFieldsTransaction{pullTestTransaction: &pullTestTransaction{issue: local}},
	}
	if _, err := NewEngine(tr, store, "sync").Sync(ctx, SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return store.tx.updates
}

// TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged checks that a pull
// from a DescriptionFieldsTracker writes the acceptance criteria, design and
// notes the remote description carries: an edited section reaches its field
// and an emptied one clears it, while the description stays as it is.
func TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged(t *testing.T) {
	local := newDescriptionFieldsLocal()
	pulled := *local
	pulled.AcceptanceCriteria = "It works, edited remotely"
	pulled.Notes = ""

	updates := runDescriptionFieldsPull(t, true, local, &pulled)
	if len(updates) != 1 {
		t.Fatalf("pull wrote %d updates, want 1 for the remote section edit", len(updates))
	}
	for key, want := range map[string]string{
		"description":         "Body",
		"acceptance_criteria": "It works, edited remotely",
		"design":              "Plain",
		"notes":               "",
	} {
		if got, ok := updates[0][key]; !ok || got != want {
			t.Errorf("update[%q] = %#v (present %v), want %q", key, got, ok, want)
		}
	}
	if local.AcceptanceCriteria != "It works, edited remotely" || local.Design != "Plain" || local.Notes != "" || local.Description != "Body" {
		t.Errorf("local after pull = %+v, want the edited criteria, the emptied notes cleared, and description and design kept", local)
	}
}

// TestEnginePullDescriptionFieldsKeepUnchangedLocalSplit checks that a pull
// whose remote text is what the local issue would push leaves the issue
// alone, even when the tracker would split that text differently: here the
// local description holds a template "## Acceptance Criteria" heading while
// its field is empty.
func TestEnginePullDescriptionFieldsKeepUnchangedLocalSplit(t *testing.T) {
	local := newDescriptionFieldsLocal()
	local.Description = "Body\n## Acceptance Criteria\nfill me in"
	local.AcceptanceCriteria, local.Design, local.Notes = "", "", ""
	pulled := *local
	pulled.Description = "Body"
	pulled.AcceptanceCriteria = "fill me in"

	if updates := runDescriptionFieldsPull(t, true, local, &pulled); len(updates) != 0 {
		t.Fatalf("pull wrote %v, want no update while the remote text is unchanged", updates)
	}
	if local.Description != "Body\n## Acceptance Criteria\nfill me in" || local.AcceptanceCriteria != "" {
		t.Fatalf("local after pull = description %q, acceptance criteria %q, want both unchanged", local.Description, local.AcceptanceCriteria)
	}
}

// TestEnginePullDescriptionFieldsLeftAloneWithoutCapability pins that a
// tracker without DescriptionFieldsTracker keeps today's pull: the update
// writes the description and none of the acceptance criteria, design or
// notes, which keep their local values even when the mapper fills them.
func TestEnginePullDescriptionFieldsLeftAloneWithoutCapability(t *testing.T) {
	local := newDescriptionFieldsLocal()
	pulled := *local
	pulled.Description = "Body, edited remotely"
	pulled.AcceptanceCriteria = "Remote criteria"
	pulled.Design = ""
	pulled.Notes = "Remote notes"

	updates := runDescriptionFieldsPull(t, false, local, &pulled)
	if len(updates) != 1 {
		t.Fatalf("pull wrote %d updates, want 1 for the remote description edit", len(updates))
	}
	if got := updates[0]["description"]; got != "Body, edited remotely" {
		t.Errorf("update[description] = %#v, want %q", got, "Body, edited remotely")
	}
	for _, key := range []string{"acceptance_criteria", "design", "notes"} {
		if got, ok := updates[0][key]; ok {
			t.Errorf("update[%q] = %#v, want the field left out of the update", key, got)
		}
	}
	if local.AcceptanceCriteria != "It works" || local.Design != "Plain" || local.Notes != "Remember" {
		t.Errorf("local fields after pull = %q, %q, %q, want them unchanged", local.AcceptanceCriteria, local.Design, local.Notes)
	}
}
