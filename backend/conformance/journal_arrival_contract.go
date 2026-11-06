package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/journalops"
)

// This file holds the ARRIVAL contract: the commit-ordered create sequence the
// journal stamps on a wisp-plane row (types.Issue.ArrivalSeq). It rides the
// Journal role's fixture because the property is the journal's — the value IS
// the seq of the row's create record — and because the one case that matters
// most (RunJournalArrivalRecoversCreateOrderAfterTruncation) needs the prune
// that fixture already carries.
//
// WHAT THE STAMP IS FOR. A consumer that rebaselines after the journal was
// pruned past its cursor has rows and no records. Without a stamp the only
// order those rows have is created_at, which is the writer's clock at INSERT
// time and one-second precision, so it cannot reproduce the order the creates
// committed in. The stamp can, because it is the record's seq, and the seq is
// drawn from the commit-ordered counter.
//
// THE CASES ARE DECLARED IN journal_contract.go, between the pruning cases and
// RunJournalEveryMutationKindLandsARow: the role table runs cases in
// declaration order, and that one must stay last. This file holds their hooks
// and helpers.
//
// THE CONTRACT (docs/reference/events-journal.md, "Arrival order on the row"),
// as those cases pin it:
//
//   - the stamp of a wisp created while the workspace journals equals the seq
//     of its create record, and the record's post-image carries it
//     (StampsAWispCreateWithItsRecordSeq);
//   - a batch stamps in item order (OrdersABatchByItem);
//   - every read path carries it, and later post-images repeat it
//     (IsOnEveryReadPath);
//   - nothing after the create moves it (NeverChanges);
//   - nothing stamps while the journal is off, and turning it on backfills
//     nothing (OnlyWhileJournaling);
//   - durable rows never carry one, and a promotion clears it
//     (OnlyOnTheWispPlane);
//   - sorting surviving rows by it recovers the create order after every
//     record is pruned (RecoversCreateOrderAfterTruncation);
//   - it is never handed out twice, across a delete and a re-create
//     (IsNeverReused);
//   - a value a caller supplies is never stored (IgnoresSuppliedValues);
//   - a synthetic create record — an import upsert or a rename journals one
//     for a row that already exists — never moves a stamp
//     (ReimportKeepsTheStamp, RenameKeepsTheStamp), and the first create
//     record journaled for an unstamped row is the one that stamps it
//     (FirstCreateRecordStampsAnUnstampedRow).
//
// A LEG THAT CANNOT DRIVE A HOOK LEAVES IT NIL, and the case (or the part of it)
// that needs the hook skips by name. A fixture with no Arrival at all skips
// every case here.
//
// SEEDING DISCIPLINE is the journal contract's: every case takes the live head
// first and asserts about the window above it, and every id carries the
// fixture's prefix and the case's own marker.

// JournalArrivalHooks are the verbs the arrival cases drive. Every create here
// is a WISP create (ephemeral), because the wisp plane is the only one the
// stamp is defined on; durable creates go through JournalFixture.Mutations.
type JournalArrivalHooks struct {
	// CreateWisp creates one ephemeral issue with the given id and returns the
	// create's own result, which must already carry the stamp.
	CreateWisp func(ctx context.Context, id string) (*types.Issue, error)
	// CreateWispBatch creates ephemeral issues with the given ids in one
	// batch, in the order given.
	CreateWispBatch func(ctx context.Context, ids []string) error
	// CreateWispClaiming creates one ephemeral issue whose request carries
	// forged as its ArrivalSeq, a value no backend may store.
	CreateWispClaiming func(ctx context.Context, id string, forged int64) error
	// Import imports issue (an upsert, carrying whatever ArrivalSeq it holds).
	Import func(ctx context.Context, issue *types.Issue) error
	// Rename changes an existing issue's id from one to another. A leg whose
	// backend has no id-rename verb leaves it nil, and RenameKeepsTheStamp
	// skips.
	Rename func(ctx context.Context, from, to string) error
	// Promote moves an ephemeral issue onto the durable plane.
	Promote func(ctx context.Context, id string) error
	// Update mutates a scalar field of an existing wisp, and Close closes one.
	// They are the arrival hooks' own rather than JournalMutations' because a
	// leg's journal mutations may drive a durable-plane verb.
	Update func(ctx context.Context, id string) error
	Close  func(ctx context.Context, id string) error
	// Comment writes one comment on an existing wisp; AddDependency and
	// RemoveDependency add and remove one blocking edge from it.
	Comment          func(ctx context.Context, id, text string) error
	AddDependency    func(ctx context.Context, from, to string) error
	RemoveDependency func(ctx context.Context, from, to string) error
	// Reopen reopens a closed issue.
	Reopen func(ctx context.Context, id string) error
	// Get reads one issue by id.
	Get func(ctx context.Context, id string) (*types.Issue, error)
	// List lists exactly ids; brief selects the narrow (no free-form text)
	// projection.
	List func(ctx context.Context, ids []string, brief bool) ([]*types.Issue, error)
}

// ArrivalRoles are the public roles ArrivalHooksFromRoles binds the hooks to.
// Promote has no role spelling on every backend, so the leg supplies it (nil
// skips the promotion half of OnlyOnTheWispPlane). Importer may be nil: only
// the unit-of-work provider hands one out, and a store leg binds Import to its
// own import door instead (ArrivalUpsertImportHook).
type ArrivalRoles struct {
	Lifecycle        publicops.Lifecycle
	BatchCreator     publicops.BatchCreator
	Importer         publicops.Importer
	Reader           publicops.Reader
	Commenter        publicops.Commenter
	DependencyEditor publicops.DependencyEditor
	Promote          func(ctx context.Context, id string) error
}

// ArrivalHooksFromRoles binds every hook to the backend's public roles, so a
// leg that serves them wires the arrival contract in one line. The explicit
// ids carry the fixture prefix rather than the workspace's, so creates force
// the prefix.
func ArrivalHooksFromRoles(roles ArrivalRoles) *JournalArrivalHooks { return roles.hooks() }

func (roles ArrivalRoles) hooks() *JournalArrivalHooks {
	wisp := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2, Ephemeral: true}
	}
	hooks := &JournalArrivalHooks{
		Promote: roles.Promote,
		CreateWisp: func(ctx context.Context, id string) (*types.Issue, error) {
			result, err := roles.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "actor", Issue: wisp(id), ForceIDPrefix: true})
			if err != nil {
				return nil, err
			}
			return result.Issue, nil
		},
		CreateWispClaiming: func(ctx context.Context, id string, forged int64) error {
			issue := wisp(id)
			issue.ArrivalSeq = forged
			_, err := roles.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "actor", Issue: issue, ForceIDPrefix: true})
			return err
		},
		Update: func(ctx context.Context, id string) error {
			_, err := roles.Lifecycle.Update(ctx, publicops.UpdateRequest{
				Actor: "actor", IssueID: id,
				Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "renamed " + id}},
			})
			return err
		},
		Close: func(ctx context.Context, id string) error {
			_, err := roles.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "actor", IssueID: id, Reason: "done"})
			return err
		},
		Comment: func(ctx context.Context, id, text string) error {
			_, err := roles.Commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "actor", IssueID: id, Text: text})
			return err
		},
		AddDependency: func(ctx context.Context, from, to string) error {
			_, err := roles.DependencyEditor.AddDependencies(ctx, publicops.AddDependenciesRequest{
				Actor: "actor", Edges: []publicops.DependencyEdge{{IssueID: from, DependsOnID: to, Type: publicops.DepBlocks}},
			})
			return err
		},
		RemoveDependency: func(ctx context.Context, from, to string) error {
			_, err := roles.DependencyEditor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{Actor: "actor", IssueID: from, DependsOnID: to})
			return err
		},
		Reopen: func(ctx context.Context, id string) error {
			_, err := roles.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "actor", IssueID: id})
			return err
		},
		Get: func(ctx context.Context, id string) (*types.Issue, error) {
			details, err := roles.Reader.Get(ctx, publicops.GetRequest{ID: id})
			if err != nil {
				return nil, err
			}
			issue := details.Issue
			return &issue, nil
		},
		List: func(ctx context.Context, ids []string, brief bool) ([]*types.Issue, error) {
			page, err := roles.Reader.List(ctx, publicops.ListRequest{IDFilter: strings.Join(ids, ","), Brief: brief, IncludeEphemeral: true, AllFlag: true})
			if err != nil {
				return nil, err
			}
			out := make([]*types.Issue, 0, len(page.Items))
			for _, item := range page.Items {
				out = append(out, item.Issue)
			}
			return out, nil
		},
	}
	if roles.BatchCreator != nil {
		hooks.CreateWispBatch = func(ctx context.Context, ids []string) error {
			items := make([]publicops.BatchCreateItem, 0, len(ids))
			for _, id := range ids {
				items = append(items, publicops.BatchCreateItem{Issue: wisp(id)})
			}
			_, err := roles.BatchCreator.CreateBatch(ctx, publicops.CreateBatchRequest{Actor: "actor", Items: items, ForceIDPrefix: true})
			return err
		}
	}
	if roles.Importer != nil {
		hooks.Import = func(ctx context.Context, issue *types.Issue) error {
			_, err := roles.Importer.ImportBatch(ctx, publicops.ImportBatchRequest{
				Actor: "actor", Issues: []*types.Issue{issue}, SkipPrefixValidation: true,
			})
			return err
		}
	}
	return hooks
}

// ArrivalRoleSource is the accessor set ArrivalHooksFromSource reads the roles
// through — the ones every in-tree store, the unit-of-work provider and the
// http client hand out, so a leg reaches them exactly as a front door does.
type ArrivalRoleSource interface {
	IssueLifecycle() (publicops.Lifecycle, error)
	BatchCreator() (publicops.BatchCreator, error)
	IssueReader() (publicops.Reader, error)
	Commenter() (publicops.Commenter, error)
	DependencyEditor() (publicops.DependencyEditor, error)
}

// ArrivalImporterSource is the Importer accessor a source may also hand out
// (today only the unit-of-work provider does). ArrivalHooksFromSource binds
// Import through it when source has it, and leaves Import nil otherwise.
type ArrivalImporterSource interface {
	Importer() (publicops.Importer, error)
}

// ArrivalHooksFromSource is ArrivalHooksFromRoles over source's accessors,
// failing t when one of them does. promote may be nil.
func ArrivalHooksFromSource(t *testing.T, source ArrivalRoleSource, promote func(ctx context.Context, id string) error) *JournalArrivalHooks {
	t.Helper()
	lifecycle, err := source.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	creator, err := source.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	var importer publicops.Importer
	if imports, ok := source.(ArrivalImporterSource); ok {
		if importer, err = imports.Importer(); err != nil {
			t.Fatalf("Importer(): %v", err)
		}
	}
	reader, err := source.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	commenter, err := source.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	editor, err := source.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	return ArrivalRoles{
		Lifecycle: lifecycle, BatchCreator: creator, Importer: importer, Reader: reader,
		Commenter: commenter, DependencyEditor: editor, Promote: promote,
	}.hooks()
}

// ArrivalUpserter is the bulk upsert a store's `bd import` runs through when
// the store hands out no Importer role (storage.DoltStorage's
// CreateIssuesWithFullOptions, the "raw seam" on both store legs).
type ArrivalUpserter interface {
	CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error
}

// ArrivalUpsertImportHook binds JournalArrivalHooks.Import to store's bulk
// upsert, with the options `bd import` passes it that matter here: the
// fixture's ids carry its own prefix, and a re-import must land rather than be
// rejected as stale.
func ArrivalUpsertImportHook(store ArrivalUpserter) func(ctx context.Context, issue *types.Issue) error {
	return func(ctx context.Context, issue *types.Issue) error {
		return store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "actor", storage.BatchCreateOptions{
			SkipPrefixValidation:           true,
			SkipDependencyValidationErrors: true,
		})
	}
}

// ArrivalRenamer is the id-rename verb a store exposes (storage.DoltStorage's
// UpdateIssueID), with the read the rename needs to carry the row's text.
type ArrivalRenamer interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
}

// ArrivalRenameHook binds JournalArrivalHooks.Rename to store's id rename.
func ArrivalRenameHook(store ArrivalRenamer) func(ctx context.Context, from, to string) error {
	return func(ctx context.Context, from, to string) error {
		issue, err := store.GetIssue(ctx, from)
		if err != nil {
			return fmt.Errorf("reading %s before renaming it: %w", from, err)
		}
		return store.UpdateIssueID(ctx, from, to, issue, "actor")
	}
}

// arrivalForgedSeq is the value IgnoresSuppliedValues claims. No journal in a
// test reaches it, so a store that kept it is the only way it can come back.
const arrivalForgedSeq int64 = 1 << 40

// arrivalHooks returns the fixture's hooks, skipping the case when the leg
// supplies none.
func arrivalHooks(t *testing.T, fixture JournalFixture) *JournalArrivalHooks {
	t.Helper()
	if fixture.Arrival == nil {
		t.Skip("this leg supplies no arrival hooks (JournalFixture.Arrival)")
	}
	a := fixture.Arrival
	if a.CreateWisp == nil || a.Update == nil || a.Close == nil || a.Comment == nil || a.AddDependency == nil ||
		a.RemoveDependency == nil || a.Get == nil || a.List == nil {
		t.Fatal("JournalArrivalHooks.CreateWisp, Update, Close, Comment, AddDependency, RemoveDependency, Get and " +
			"List are required whenever Arrival is set")
	}
	return fixture.Arrival
}

// arrivalIDs is n ids under the case's own marker, in creation order.
func arrivalIDs(fixture JournalFixture, marker string, n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("%s-%s-%c", fixture.IssuePrefix, marker, 'a'+i))
	}
	return ids
}

func arrivalCreateWisp(t *testing.T, ctx context.Context, hooks *JournalArrivalHooks, id string) *types.Issue {
	t.Helper()
	created, err := hooks.CreateWisp(ctx, id)
	if err != nil {
		t.Fatalf("creating wisp %s: %v", id, err)
	}
	if created == nil {
		t.Fatalf("creating wisp %s returned no issue", id)
	}
	return created
}

func arrivalGet(t *testing.T, ctx context.Context, hooks *JournalArrivalHooks, id string) *types.Issue {
	t.Helper()
	issue, err := hooks.Get(ctx, id)
	if err != nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return issue
}

// arrivalFindCreateRecord is the first create record naming id in rows.
func arrivalFindCreateRecord(rows []journalops.Row, id string) (journalops.Row, bool) {
	for _, row := range rows {
		if row.Op == "create" && row.IssueID == id {
			return row, true
		}
	}
	return journalops.Row{}, false
}

func arrivalCreateRecord(t *testing.T, rows []journalops.Row, id string) journalops.Row {
	t.Helper()
	row, ok := arrivalFindCreateRecord(rows, id)
	if !ok {
		t.Fatalf("no create record for %s in %v", id, journalOpsOf(rows))
	}
	return row
}

// arrivalPostImage decodes the arrival_seq a record's post-image carries.
func arrivalPostImage(t *testing.T, row journalops.Row) int64 {
	t.Helper()
	var image struct {
		ArrivalSeq int64 `json:"arrival_seq"`
	}
	if err := json.Unmarshal([]byte(row.IssueJSON), &image); err != nil {
		t.Fatalf("decoding the post-image of record %d: %v", row.Seq, err)
	}
	return image.ArrivalSeq
}
