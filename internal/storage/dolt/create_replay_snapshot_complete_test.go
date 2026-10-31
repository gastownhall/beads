package dolt

// SnapshotCreateInputs must undo EVERY write the real create path makes into
// caller-owned state — struct fields AND pointees. This runs the real issueops
// create path against a real Dolt server (the store-internal path and the
// transaction path cmd/bd replays), rolls the SQL back, restores, and
// deep-compares with a deep copy taken before the create. It fails the day
// the create path starts writing somewhere the snapshot does not restore.

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func deepCopyValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.New(v.Elem().Type())
		n.Elem().Set(deepCopyValue(v.Elem()))
		return n
	case reflect.Struct:
		n := reflect.New(v.Type()).Elem()
		n.Set(v) // shallow, including unexported fields (time.Time etc.)
		for i := 0; i < v.NumField(); i++ {
			if !n.Field(i).CanSet() {
				continue
			}
			n.Field(i).Set(deepCopyValue(v.Field(i)))
		}
		return n
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			n.Index(i).Set(deepCopyValue(v.Index(i)))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		n := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			n.SetMapIndex(k, deepCopyValue(v.MapIndex(k)))
		}
		return n
	default:
		return v
	}
}

func createReplayFixtureIssues(t *testing.T) []*types.Issue {
	t.Helper()
	est := 30
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	ext := "gh-1"
	return []*types.Issue{
		{
			Title: "auto-id durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Labels:           []string{"zeta", "alpha", "alpha"},
			Metadata:         json.RawMessage(`{"b":1,"a":2}`),
			EstimatedMinutes: &est,
			DueAt:            &due,
			ExternalRef:      &ext,
			Dependencies: []*types.Dependency{
				{DependsOnID: "test-anchor", Type: types.DepBlocks},
				{DependsOnID: "test-nosuch", Type: types.DepRelated},
			},
			Comments: []*types.Comment{
				{Author: "a", Text: "fresh"},
				{ID: "c-imported", Author: "b", Text: "imported", CreatedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)},
			},
		},
		{
			// closed without closed_at: PrepareIssueForInsert allocates one
			Title: "closed auto", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeBug,
		},
		{
			ID: "test-anchor.7", Title: "hierarchical explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "test-anchor", Type: types.DepParentChild}},
		},
	}
}

func assertCreateInputsRestored(t *testing.T, label string, before, after []*types.Issue) {
	t.Helper()
	for i := range before {
		if !reflect.DeepEqual(before[i], after[i]) {
			b, _ := json.Marshal(before[i])
			a, _ := json.Marshal(after[i])
			t.Errorf("%s: issue %d not restored\nbefore: %s\nafter:  %s", label, i, b, a)
		}
	}
}

func TestSnapshotCreateInputsUndoesTheRealCreatePath(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-anchor", Title: "anchor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, counter := range []bool{false, true} {
		mode := "hash"
		if counter {
			mode = "counter"
			if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
				t.Fatal(err)
			}
		}
		for _, wisp := range []bool{false, true} {
			// (a) the store-internal path (withRetryTx bodies): issueops directly.
			issues := createReplayFixtureIssues(t)
			if wisp {
				for _, is := range issues {
					is.Ephemeral = true
				}
			}
			before := deepCopyValue(reflect.ValueOf(issues)).Interface().([]*types.Issue)
			restore := issueops.SnapshotCreateInputs(issues)
			tx, err := base.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, cerr := issueops.CreateIssuesInTxWithResult(ctx, tx, issues, "tester", storage.BatchCreateOptions{SkipDependencyValidationErrors: true})
			_ = tx.Rollback()
			if cerr != nil {
				t.Fatalf("%s/wisp=%v create: %v", mode, wisp, cerr)
			}
			if issues[0].ID == "" || issues[0].Dependencies[0].IssueID == "" || issues[0].Comments[0].ID == "" || issues[1].ClosedAt == nil {
				t.Fatalf("%s/wisp=%v: the create path wrote nothing back; test is vacuous", mode, wisp)
			}
			restore()
			assertCreateInputsRestored(t, mode+"/issueops/wisp="+map[bool]string{true: "y", false: "n"}[wisp], before, issues)

			// (b) the transaction path cmd/bd replays (RunInTransaction + tx.CreateIssues).
			issues = createReplayFixtureIssues(t)
			if wisp {
				for _, is := range issues {
					is.Ephemeral = true
				}
			}
			before = deepCopyValue(reflect.ValueOf(issues)).Interface().([]*types.Issue)
			restore = issueops.SnapshotCreateInputs(issues)
			sentinel := errors.New("roll back")
			err = base.RunInTransaction(ctx, "test: snapshot restore", func(tx storage.Transaction) error {
				if err := tx.CreateIssues(ctx, issues, "tester"); err != nil {
					return err
				}
				return sentinel
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("%s/wisp=%v tx create: %v", mode, wisp, err)
			}
			restore()
			assertCreateInputsRestored(t, mode+"/tx/wisp="+map[bool]string{true: "y", false: "n"}[wisp], before, issues)
		}
	}
}
