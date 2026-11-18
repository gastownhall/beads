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
	// previousRefs answers external_ref history: each issue's ref at the
	// last sync ("" for none).
	previousRefs map[string]string
}

func (s *descriptionFieldsStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	ref, ok := s.previousRefs[id]
	return ref, ok, nil
}

// GetConfig answers the issue prefix a pull selecting beads by ID reads.
func (s *descriptionFieldsStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "bd", nil
	}
	return "", nil
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

// descriptionFieldsLastSync falls after the last edit of
// newDescriptionFieldsLocal's issue and before the remote's in
// newDescriptionFieldsSync, so the conflict guard admits a remote change.
var descriptionFieldsLastSync = time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

// newDescriptionFieldsSync returns an engine syncing a store holding local
// with one remote issue, EXT-1, whose description is pulled folded and
// which the mapper converts to pulled. The store has no last_sync.
func newDescriptionFieldsSync(t *testing.T, capability bool, local, pulled *types.Issue) (*Engine, *descriptionFieldsStore, *mockTracker) {
	t.Helper()
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
		previousRefs:  make(map[string]string),
	}
	return NewEngine(tr, store, "sync"), store, mock
}

// runDescriptionFieldsPull pulls remote, which the mapper converts to
// pulled, into a store holding local, and returns the updates it wrote.
// With lastSync the store holds that last_sync and local was linked before
// it; without, the pull is the first sync.
func runDescriptionFieldsPull(t *testing.T, capability bool, lastSync *time.Time, local, pulled *types.Issue) []map[string]interface{} {
	t.Helper()
	engine, store, _ := newDescriptionFieldsSync(t, capability, local, pulled)
	if lastSync != nil {
		store.localMetadata["test.last_sync"] = lastSync.Format(time.RFC3339)
		store.previousRefs[local.ID] = descriptionFieldsRef
	}
	if _, err := engine.Sync(context.Background(), SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return store.tx.updates
}

// TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged checks that a pull
// from a DescriptionFieldsTracker, admitted by the conflict guard because the
// local issue is unchanged since the last sync, writes the acceptance
// criteria, design and notes the remote description carries: an edited
// section reaches its field and an emptied one clears it, while the
// description stays as it is.
func TestEnginePullDescriptionFieldsWrittenWhenRemoteChanged(t *testing.T) {
	local := newDescriptionFieldsLocal()
	pulled := *local
	pulled.AcceptanceCriteria = "It works, edited remotely"
	pulled.Notes = ""

	updates := runDescriptionFieldsPull(t, true, &descriptionFieldsLastSync, local, &pulled)
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

	if updates := runDescriptionFieldsPull(t, true, nil, local, &pulled); len(updates) != 0 {
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

	updates := runDescriptionFieldsPull(t, false, nil, local, &pulled)
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

// issueText is the text of an issue a DescriptionFieldsTracker pushes.
type issueText struct {
	Title, Description, AcceptanceCriteria, Design, Notes string
}

func textOf(issue *types.Issue) issueText {
	return issueText{issue.Title, issue.Description, issue.AcceptanceCriteria, issue.Design, issue.Notes}
}

// TestEnginePullDescriptionFieldsKeptWhenUnguarded checks the pulls that
// cannot tell a section the remote deleted from one that never reached it: a
// first sync (no last_sync), and a pull of a stub linked to the remote issue
// after the last sync, whether it hydrates the stub or a conflict resolved
// for the remote's version admits it, in a bulk or a selective pull. There a
// field the remote text has no section for keeps its local value, and the
// same sync's push sends it, also for a stub the conflict resolution kept out
// of the push. A field the remote text carries still wins. With a push hash
// the pull fingerprints the remote without the kept fields, so the push is
// not skipped.
func TestEnginePullDescriptionFieldsKeptWhenUnguarded(t *testing.T) {
	contentEqual := func(local *types.Issue, remote *TrackerIssue) bool {
		return local.Title == remote.Title && foldDescriptionFields(local) == remote.Description
	}
	hooks := map[string]*PushHooks{
		"content compare": {ContentEqual: contentEqual},
		"push hash": {ContentEqual: contentEqual, ContentHash: func(issue *types.Issue) string {
			return issue.Title + "\x00" + foldDescriptionFields(issue)
		}},
	}
	// The stub rows link bd-1 as bd create --external-ref with
	// --acceptance/--design/--notes does, ten seconds after the last sync,
	// to a remote issue last updated at remoteUpdated.
	linkStub := func(remoteUpdated time.Time) func(*descriptionFieldsStore, *mockTracker) {
		return func(store *descriptionFieldsStore, mock *mockTracker) {
			store.localMetadata["test.last_sync"] = descriptionFieldsLastSync.Format(time.RFC3339)
			store.previousRefs["bd-1"] = "" // no ref at the last sync
			mock.issues[0].UpdatedAt = remoteUpdated
		}
	}
	stub := func() *types.Issue {
		stub := newDescriptionFieldsLocal()
		stub.Title, stub.Description, stub.AcceptanceCriteria = "stub", "stub body", "Local criteria"
		stub.UpdatedAt = descriptionFieldsLastSync.Add(10 * time.Second)
		return stub
	}
	stubRemote := func(local types.Issue) types.Issue {
		local.Title, local.Description, local.AcceptanceCriteria = "Fields", "Body", "Remote criteria"
		local.Design, local.Notes = "", ""
		return local
	}
	stubWant := issueText{Title: "Fields", Description: "Body", AcceptanceCriteria: "Remote criteria", Design: "Plain", Notes: "Remember"}
	for _, test := range []struct {
		name string
		// prepare sets up the store and the remote after
		// newDescriptionFieldsSync.
		prepare func(*descriptionFieldsStore, *mockTracker)
		// options adds to the two-way sync's options.
		options SyncOptions
		local   func() *types.Issue
		// pulled returns what the remote's text converts to.
		pulled func(local types.Issue) types.Issue
		// wantConflicts is 1 where a conflict resolution admits the pull.
		wantConflicts int
		wantUpdates   int
		want          issueText // the local issue after the sync, also pushed
	}{
		{
			// The remote text lacks only the notes: with them kept, the
			// pull has nothing to write.
			name:   "first sync",
			local:  newDescriptionFieldsLocal,
			pulled: func(local types.Issue) types.Issue { local.Notes = ""; return local },
			want:   issueText{Title: "Fields", Description: "Body", AcceptanceCriteria: "It works", Design: "Plain", Notes: "Remember"},
		},
		{
			// The remote is unchanged since the last sync, so the pull
			// hydrates the stub.
			name:        "pre-linked stub",
			prepare:     linkStub(descriptionFieldsLastSync.Add(-time.Hour)),
			local:       stub,
			pulled:      stubRemote,
			wantUpdates: 1,
			want:        stubWant,
		},
		{
			// The remote changed after the stub, so the conflict
			// resolution takes the remote's version.
			name:          "pre-linked stub, remote newer",
			prepare:       linkStub(descriptionFieldsLastSync.Add(time.Hour)),
			local:         stub,
			pulled:        stubRemote,
			wantConflicts: 1,
			wantUpdates:   1,
			want:          stubWant,
		},
		{
			// --prefer-linear takes the remote's version of a stub newer
			// than its remote's change.
			name:          "pre-linked stub, prefer remote",
			prepare:       linkStub(descriptionFieldsLastSync.Add(5 * time.Second)),
			options:       SyncOptions{ConflictResolution: ConflictExternal},
			local:         stub,
			pulled:        stubRemote,
			wantConflicts: 1,
			wantUpdates:   1,
			want:          stubWant,
		},
		{
			// The same conflict, in a pull selecting the stub by ID.
			name:          "pre-linked stub, remote newer, selected by ID",
			prepare:       linkStub(descriptionFieldsLastSync.Add(time.Hour)),
			options:       SyncOptions{IssueIDs: []string{"bd-1"}},
			local:         stub,
			pulled:        stubRemote,
			wantConflicts: 1,
			wantUpdates:   1,
			want:          stubWant,
		},
	} {
		for hooksName, pushHooks := range hooks {
			t.Run(test.name+"/"+hooksName, func(t *testing.T) {
				local := test.local()
				pulled := test.pulled(*local)
				engine, store, mock := newDescriptionFieldsSync(t, true, local, &pulled)
				if test.prepare != nil {
					test.prepare(store, mock)
				}
				engine.PushHooks = pushHooks

				opts := test.options
				opts.Pull, opts.Push = true, true
				result, err := engine.Sync(context.Background(), opts)
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if result.Stats.Conflicts != test.wantConflicts {
					t.Errorf("sync found %d conflicts, want %d", result.Stats.Conflicts, test.wantConflicts)
				}
				if len(store.tx.updates) != test.wantUpdates {
					t.Errorf("pull wrote %v, want %d updates", store.tx.updates, test.wantUpdates)
				}
				if got := textOf(local); got != test.want {
					t.Errorf("local after the sync = %+v, want %+v", got, test.want)
				}
				pushed := mock.updated["EXT-1"]
				if pushed == nil {
					t.Fatalf("push sent nothing to EXT-1, want the kept fields sent")
				}
				if got := textOf(pushed); got != test.want {
					t.Errorf("pushed issue = %+v, want %+v", got, test.want)
				}
			})
		}
	}
}
