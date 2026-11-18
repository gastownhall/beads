package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// descriptionFieldsRemote is one tracker issue plus the split a
// description-fields tracker's mapper produces from its description, the way
// linear.IssueToBeads parses the section run out of it.
type descriptionFieldsRemote struct {
	TrackerIssue
	ParsedDescription  string
	AcceptanceCriteria string
	Design             string
	Notes              string
}

// foldDescriptionFields mirrors what a description-fields tracker pushes:
// the three fields rendered as trailing description sections.
func foldDescriptionFields(issue *types.Issue) string {
	text := issue.Description
	if issue.AcceptanceCriteria != "" {
		text += "\n\n## Acceptance Criteria\n" + issue.AcceptanceCriteria
	}
	if issue.Design != "" {
		text += "\n\n## Design\n" + issue.Design
	}
	if issue.Notes != "" {
		text += "\n\n## Notes\n" + issue.Notes
	}
	return text
}

// descriptionFieldsCapability implements DescriptionFieldsTracker on top of
// mockTracker the way linear.Tracker does: two issues are equal when pushing
// them sends the same description text.
type descriptionFieldsCapability struct {
	*mockTracker
}

func (c descriptionFieldsCapability) DescriptionFieldsEqual(a, b *types.Issue) bool {
	return foldDescriptionFields(a) == foldDescriptionFields(b)
}

// newDescriptionFieldsTracker serves one remote issue through a mock tracker
// whose mapper converts it to the parsed split.
func newDescriptionFieldsTracker(remote *descriptionFieldsRemote) *mockTracker {
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{remote.TrackerIssue}
	mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title:              ti.Title,
			Description:        remote.ParsedDescription,
			AcceptanceCriteria: remote.AcceptanceCriteria,
			Design:             remote.Design,
			Notes:              remote.Notes,
			Priority:           2,
			Status:             types.StatusOpen,
			IssueType:          types.TypeTask,
		}}
	}}
	return mock
}

// newDescriptionFieldsStore returns a store holding local whose pull updates
// land on local itself, like the other pure engine pull tests.
func newDescriptionFieldsStore(local *types.Issue) *pullFailureStore {
	return &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}
}

var descriptionFieldsRef = "https://test.test/EXT-1"

// TestEnginePullDescriptionFieldsLeftAloneWithoutCapability is the AC-5
// regression guard: a tracker without the DescriptionFieldsTracker capability
// keeps the pull's old field behaviour. The update writes the description and
// none of acceptance_criteria, design or notes, so an existing bead keeps its
// local values for the three fields.
func TestEnginePullDescriptionFieldsLeftAloneWithoutCapability(t *testing.T) {
	ctx := context.Background()
	ref := descriptionFieldsRef
	local := &types.Issue{
		ID:                 "bd-1",
		Title:              "title",
		Description:        "old body",
		AcceptanceCriteria: "local AC",
		Design:             "local design",
		Notes:              "local notes",
		Status:             types.StatusOpen,
		Priority:           2,
		IssueType:          types.TypeTask,
		ExternalRef:        &ref,
		CreatedAt:          time.Now().Add(-time.Hour),
		UpdatedAt:          time.Now().Add(-time.Hour),
	}
	store := newDescriptionFieldsStore(local)
	remote := &descriptionFieldsRemote{
		TrackerIssue: TrackerIssue{
			ID: "EXT-1", Identifier: "EXT-1", Title: "title",
			Description: "new body\n\n## Acceptance Criteria\nremote AC",
			UpdatedAt:   time.Now().Add(-time.Minute),
		},
		ParsedDescription:  "new body",
		AcceptanceCriteria: "remote AC",
		Design:             "remote design",
		Notes:              "remote notes",
	}
	// The plain mock implements no DescriptionFieldsEqual.
	var plain IssueTracker = newDescriptionFieldsTracker(remote)

	result, err := NewEngine(plain, store, "sync").Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 || store.commits != 1 {
		t.Fatalf("PullStats = %+v, commits = %d, want one update", result.PullStats, store.commits)
	}
	if local.Description != "new body" {
		t.Errorf("description = %q, want the pulled body", local.Description)
	}
	if local.AcceptanceCriteria != "local AC" || local.Design != "local design" || local.Notes != "local notes" {
		t.Errorf("description fields = %q/%q/%q, want the local values kept",
			local.AcceptanceCriteria, local.Design, local.Notes)
	}
}

// TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged pins the capability
// behaviour: a pull through a DescriptionFieldsTracker writes the parsed
// acceptance_criteria, design and notes of the remote description, so a
// section edited on the tracker reaches the bead's field.
func TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged(t *testing.T) {
	ctx := context.Background()
	ref := descriptionFieldsRef
	local := &types.Issue{
		ID:                 "bd-1",
		Title:              "title",
		Description:        "body",
		AcceptanceCriteria: "AC",
		Design:             "design",
		Notes:              "notes",
		Status:             types.StatusOpen,
		Priority:           2,
		IssueType:          types.TypeTask,
		ExternalRef:        &ref,
		CreatedAt:          time.Now().Add(-time.Hour),
		UpdatedAt:          time.Now().Add(-time.Hour),
	}
	store := newDescriptionFieldsStore(local)
	remote := &descriptionFieldsRemote{
		TrackerIssue: TrackerIssue{
			ID: "EXT-1", Identifier: "EXT-1", Title: "title",
			Description: "body\n\n## Acceptance Criteria\n\nAC edited in the tracker\n\n## Design\n\ndesign\n\n## Notes\n\nnotes",
			UpdatedAt:   time.Now().Add(-time.Minute),
		},
		ParsedDescription:  "body",
		AcceptanceCriteria: "AC edited in the tracker",
		Design:             "design",
		Notes:              "notes",
	}
	capable := descriptionFieldsCapability{mockTracker: newDescriptionFieldsTracker(remote)}

	result, err := NewEngine(capable, store, "sync").Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 || store.commits != 1 {
		t.Fatalf("PullStats = %+v, commits = %d, want one update", result.PullStats, store.commits)
	}
	if local.AcceptanceCriteria != "AC edited in the tracker" {
		t.Errorf("acceptance_criteria = %q, want the edited section text", local.AcceptanceCriteria)
	}
	if local.Description != "body" || local.Design != "design" || local.Notes != "notes" {
		t.Errorf("description/design/notes = %q/%q/%q, want unchanged", local.Description, local.Design, local.Notes)
	}
}

// TestEnginePullDescriptionFieldsKeepUnchangedLocalSplit pins the unchanged
// case: when the remote description text is unchanged, the pull keeps the
// bead's own split of it. A description holding a template
// "## Acceptance Criteria" heading while the field is empty must not have the
// heading parsed out of it by an echo of the last push.
func TestEnginePullDescriptionFieldsKeepUnchangedLocalSplit(t *testing.T) {
	ctx := context.Background()
	ref := descriptionFieldsRef
	templateDescription := "body\n\n## Acceptance Criteria\n- [ ] first"
	local := &types.Issue{
		ID:          "bd-1",
		Title:       "title",
		Description: templateDescription,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		CreatedAt:   time.Now().Add(-time.Hour),
		UpdatedAt:   time.Now().Add(-time.Hour),
	}
	store := newDescriptionFieldsStore(local)
	remote := &descriptionFieldsRemote{
		TrackerIssue: TrackerIssue{
			ID: "EXT-1", Identifier: "EXT-1", Title: "title",
			Description: templateDescription,
			UpdatedAt:   time.Now().Add(-time.Minute),
		},
		// What the mapper's parse of the template description yields.
		ParsedDescription:  "body",
		AcceptanceCriteria: "- [ ] first",
	}
	capable := descriptionFieldsCapability{mockTracker: newDescriptionFieldsTracker(remote)}

	result, err := NewEngine(capable, store, "sync").Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 0 || store.commits != 0 {
		t.Fatalf("PullStats = %+v, commits = %d, want the unchanged echo skipped", result.PullStats, store.commits)
	}
	if local.Description != templateDescription {
		t.Errorf("description = %q, want the template heading kept", local.Description)
	}
	if local.AcceptanceCriteria != "" {
		t.Errorf("acceptance_criteria = %q, want empty: the pull changed no text", local.AcceptanceCriteria)
	}
}
