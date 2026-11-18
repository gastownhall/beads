//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// autoMintIssue is a create that names no id.
func autoMintIssue(title string) *types.Issue {
	return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// counterModeEnv is a fresh store in counter mode whose counter has minted
// cl-1, so a later explicit id cannot be hidden by the counter's first-use
// seeding.
func counterModeEnv(t *testing.T) *testEnv {
	t.Helper()
	te := newTestEnv(t, "cl")
	ctx := t.Context()
	if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode): %v", err)
	}
	if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	first := autoMintIssue("auto one")
	if err := te.store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(auto one): %v", err)
	}
	if first.ID != "cl-1" {
		t.Fatalf("first auto id = %q, want cl-1", first.ID)
	}
	return te
}

// TestCounterMintJumpsPastHeldIDs pins the counter-lag leg of bd-t4h0n7 (PR
// #6754) through every store API that mints: explicit cl-2 (issues) and cl-100
// (wisps) created after the counter's last bump hold values the counter would
// hand out next. The mint must jump past both planes' held suffixes to cl-101,
// leave both explicit rows untouched, and mint cl-102 next. The upsert-shaped
// APIs used to take cl-2 and overwrite the explicit row; the guarded ones were
// handed it and refused with ErrAlreadyExists.
func TestCounterMintJumpsPastHeldIDs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	apis := []struct {
		name string
		mint func(t *testing.T, te *testEnv, title string) (string, error)
	}{
		{"legacy single CreateIssue", func(t *testing.T, te *testEnv, title string) (string, error) {
			issue := autoMintIssue(title)
			err := te.store.CreateIssue(t.Context(), issue, "tester")
			return issue.ID, err
		}},
		{"legacy batch CreateIssues", func(t *testing.T, te *testEnv, title string) (string, error) {
			issue := autoMintIssue(title)
			err := te.store.CreateIssues(t.Context(), []*types.Issue{issue}, "tester")
			return issue.ID, err
		}},
		{"guarded single Create", func(t *testing.T, te *testEnv, title string) (string, error) {
			lifecycle, err := te.store.IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			created, err := lifecycle.Create(t.Context(), publicops.CreateRequest{Actor: "tester", Issue: autoMintIssue(title)})
			if err != nil {
				return "", err
			}
			return created.Issue.ID, nil
		}},
		{"guarded batch CreateBatch", func(t *testing.T, te *testEnv, title string) (string, error) {
			creator, err := te.store.BatchCreator()
			if err != nil {
				t.Fatalf("BatchCreator(): %v", err)
			}
			created, err := creator.CreateBatch(t.Context(), publicops.CreateBatchRequest{
				Actor: "tester", Items: []publicops.BatchCreateItem{{Issue: autoMintIssue(title)}},
			})
			if err != nil {
				return "", err
			}
			return created.Issues[0].ID, nil
		}},
	}
	for _, api := range apis {
		t.Run(api.name, func(t *testing.T) {
			te := counterModeEnv(t)
			ctx := t.Context()
			explicitTwo := &types.Issue{ID: "cl-2", Title: "explicit two", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := te.store.CreateIssue(ctx, explicitTwo, "tester"); err != nil {
				t.Fatalf("CreateIssue(explicit cl-2): %v", err)
			}
			explicitHundred := &types.Issue{ID: "cl-100", Title: "explicit hundred", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
			if err := te.store.CreateIssue(ctx, explicitHundred, "tester"); err != nil {
				t.Fatalf("CreateIssue(explicit wisp cl-100): %v", err)
			}

			id, err := api.mint(t, te, "auto two")
			if err != nil {
				t.Fatalf("auto-minted create = %v, want cl-101", err)
			}
			if id != "cl-101" {
				t.Errorf("auto-minted id = %q, want cl-101 (one past the held cl-100)", id)
			}
			assertTitle(t, ctx, te, "cl-2", "explicit two")
			assertTitle(t, ctx, te, "cl-100", "explicit hundred")
			te.assertRowExists(t, ctx, "issues", "cl-2")
			te.assertRowExists(t, ctx, "wisps", "cl-100")
			te.assertRowNotExists(t, ctx, "issues", "cl-100")

			next, err := api.mint(t, te, "auto three")
			if err != nil {
				t.Fatalf("second auto-minted create = %v, want cl-102", err)
			}
			if next != "cl-102" {
				t.Errorf("second auto-minted id = %q, want cl-102", next)
			}
		})
	}
}

// assertTitle fails t unless the issue id reads back with title.
func assertTitle(t *testing.T, ctx context.Context, te *testEnv, id, title string) {
	t.Helper()
	got, err := te.store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	if got.Title != title {
		t.Fatalf("%s title = %q, want %q: an auto-minted create overwrote it", id, got.Title, title)
	}
}

// TestCreateFailureResetsAssignedIDs pins the failure leg of bd-t4h0n7 (PR
// #6754) on the embedded single and batch creates, which run one attempt: when
// the transaction fails, the issue id, comment ids and default edge owners the
// create assigned before the failing statement go back to empty while the
// supplied ids stay, and a retry with the bad value fixed mints and lands.
func TestCreateFailureResetsAssignedIDs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	tooLong := strings.Repeat("x", 300) // past the VARCHAR(255) column

	t.Run("single", func(t *testing.T) {
		te := newTestEnv(t, "cl")
		ctx := t.Context()
		issue := autoMintIssue("minted")
		issue.Comments = []*types.Comment{
			{ID: "supplied-comment", Author: "tester", Text: "supplied"},
			{Author: "tester", Text: "derived"},
			{Author: tooLong, Text: "fails"},
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err == nil {
			t.Fatal("CreateIssue with an over-long comment author succeeded, want a failure")
		}
		if issue.ID != "" || issue.Comments[1].ID != "" {
			t.Errorf("after the failure id = %q, derived comment id = %q; want both empty", issue.ID, issue.Comments[1].ID)
		}
		if issue.Comments[0].ID != "supplied-comment" {
			t.Errorf("supplied comment id = %q, want supplied-comment", issue.Comments[0].ID)
		}

		issue.Comments[2].Author = "tester"
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("retried CreateIssue: %v", err)
		}
		if issue.ID == "" || issue.Comments[1].ID == "" {
			t.Fatalf("retry assigned id %q, derived comment id %q; want both set", issue.ID, issue.Comments[1].ID)
		}
		te.assertRowExists(t, ctx, "issues", issue.ID)
	})

	t.Run("batch", func(t *testing.T) {
		te := newTestEnv(t, "cl")
		ctx := t.Context()
		target := &types.Issue{ID: "cl-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, target, "tester"); err != nil {
			t.Fatalf("CreateIssue(target): %v", err)
		}
		minted := autoMintIssue("minted")
		minted.Comments = []*types.Comment{{Author: "tester", Text: "derived"}}
		minted.Dependencies = []*types.Dependency{{DependsOnID: "cl-target", Type: types.DepBlocks, ThreadID: tooLong}}
		explicit := &types.Issue{ID: "cl-explicit", Title: "explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Comments: []*types.Comment{{ID: "supplied-comment", Author: "tester", Text: "supplied"}}}
		issues := []*types.Issue{minted, explicit}
		if err := te.store.CreateIssues(ctx, issues, "tester"); err == nil {
			t.Fatal("CreateIssues with an over-long edge thread id succeeded, want a failure")
		}
		if minted.ID != "" || minted.Comments[0].ID != "" || minted.Dependencies[0].IssueID != "" {
			t.Errorf("after the failure id = %q, comment id = %q, edge owner = %q; want all empty",
				minted.ID, minted.Comments[0].ID, minted.Dependencies[0].IssueID)
		}
		if explicit.ID != "cl-explicit" || explicit.Comments[0].ID != "supplied-comment" {
			t.Errorf("explicit id = %q, comment id = %q; want the supplied cl-explicit, supplied-comment", explicit.ID, explicit.Comments[0].ID)
		}

		minted.Dependencies[0].ThreadID = ""
		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("retried CreateIssues: %v", err)
		}
		if minted.ID == "" || minted.Dependencies[0].IssueID != minted.ID {
			t.Fatalf("retry assigned id %q, edge owner %q; want a fresh id owning the edge", minted.ID, minted.Dependencies[0].IssueID)
		}
		te.assertRowExists(t, ctx, "issues", minted.ID)
	})
}

// TestExplicitIDCreateBehaviorUnchanged pins the explicit-id side of
// bd-t4h0n7 (PR #6754): the auto-mint fix makes only creates that name no id
// create-only, so a create that names one keeps every caller-option behavior:
// the legacy single create upserts an existing row, a ConflictSkip batch skips
// it, the guarded create refuses it, a row in the sibling plane is refused,
// and a retired id is refused as expired.
func TestExplicitIDCreateBehaviorUnchanged(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()
	explicit := func(id, title string, ephemeral bool) *types.Issue {
		return &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	}
	if err := te.store.CreateIssue(ctx, explicit("cl-held", "original", false), "tester"); err != nil {
		t.Fatalf("CreateIssue(cl-held): %v", err)
	}

	t.Run("legacy single create upserts", func(t *testing.T) {
		if err := te.store.CreateIssue(ctx, explicit("cl-upsert", "first", false), "tester"); err != nil {
			t.Fatalf("CreateIssue(cl-upsert): %v", err)
		}
		if err := te.store.CreateIssue(ctx, explicit("cl-upsert", "second", false), "tester"); err != nil {
			t.Fatalf("CreateIssue(cl-upsert) again = %v, want the upsert", err)
		}
		assertTitle(t, ctx, te, "cl-upsert", "second")
		te.assertEventCount(t, ctx, "events", "cl-upsert", "created", 1)
	})

	t.Run("ConflictSkip batch skips", func(t *testing.T) {
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicit("cl-held", "skipped", false)}, "tester",
			storage.BatchCreateOptions{ConflictSkip: true})
		if err != nil {
			t.Fatalf("ConflictSkip CreateIssuesWithFullOptions(cl-held) = %v, want a skip", err)
		}
		assertTitle(t, ctx, te, "cl-held", "original")
		te.assertEventCount(t, ctx, "events", "cl-held", "created", 1)
	})

	t.Run("guarded create refuses an existing id", func(t *testing.T) {
		lifecycle, err := te.store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		_, err = lifecycle.Create(ctx, publicops.CreateRequest{Actor: "tester", Issue: explicit("cl-held", "refused", false)})
		if !errors.Is(err, storage.ErrAlreadyExists) {
			t.Fatalf("guarded Create(cl-held) = %v, want ErrAlreadyExists", err)
		}
		assertTitle(t, ctx, te, "cl-held", "original")
	})

	t.Run("sibling plane is refused", func(t *testing.T) {
		if err := te.store.CreateIssue(ctx, explicit("cl-wisp", "wisp", true), "tester"); err != nil {
			t.Fatalf("CreateIssue(wisp cl-wisp): %v", err)
		}
		if err := te.store.CreateIssue(ctx, explicit("cl-wisp", "durable", false), "tester"); err == nil {
			t.Fatal("durable CreateIssue(cl-wisp) over a wisp succeeded, want a refusal")
		}
		assertTitle(t, ctx, te, "cl-wisp", "wisp")
		te.assertRowNotExists(t, ctx, "issues", "cl-wisp")

		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicit("cl-wisp", "durable", false)}, "tester",
			storage.BatchCreateOptions{ConflictSkip: true})
		if err != nil {
			t.Fatalf("ConflictSkip CreateIssuesWithFullOptions(cl-wisp) over a wisp = %v, want a skip", err)
		}
		assertTitle(t, ctx, te, "cl-wisp", "wisp")
		te.assertRowNotExists(t, ctx, "issues", "cl-wisp")
	})

	t.Run("retired id is refused as expired", func(t *testing.T) {
		if err := te.store.CreateIssue(ctx, explicit("cl-retired", "retired", false), "tester"); err != nil {
			t.Fatalf("CreateIssue(cl-retired): %v", err)
		}
		lifecycle, err := te.store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "tester", IssueID: "cl-retired", Reason: "done"}); err != nil {
			t.Fatalf("Close(cl-retired): %v", err)
		}
		sweeper, err := te.store.Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		if result, err := sweeper.Sweep(ctx, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: "cl-retired", RetainIDs: true}); err != nil || result.Swept != 1 {
			t.Fatalf("retaining sweep of cl-retired = %+v, %v; want Swept 1", result, err)
		}
		err = te.store.CreateIssue(ctx, explicit("cl-retired", "reborn", false), "tester")
		var expired *publicops.IdempotencyExpiredError
		if !errors.As(err, &expired) {
			t.Fatalf("CreateIssue(retired cl-retired) = %v, want *IdempotencyExpiredError", err)
		}
		te.assertRowNotExists(t, ctx, "issues", "cl-retired")

		err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicit("cl-retired", "reborn", false)}, "tester",
			storage.BatchCreateOptions{ConflictSkip: true})
		if err != nil {
			t.Fatalf("ConflictSkip CreateIssuesWithFullOptions(retired cl-retired) = %v, want a skip", err)
		}
		te.assertRowNotExists(t, ctx, "issues", "cl-retired")
	})

	t.Run("stale upsert keeps the newer stored row", func(t *testing.T) {
		if err := te.store.CreateIssue(ctx, explicit("cl-stale", "stored", false), "tester"); err != nil {
			t.Fatalf("CreateIssue(cl-stale): %v", err)
		}
		// Push the stored row's updated_at past any snapshot this test can
		// carry: RejectStaleUpserts refuses only a strictly newer stored row.
		te.exec(t, ctx, "UPDATE issues SET updated_at = '2030-01-01 00:00:00' WHERE id = 'cl-stale'")

		var rejected []string
		stale := explicit("cl-stale", "stale snapshot", false)
		stale.UpdatedAt = time.Now()
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{stale}, "tester",
			storage.BatchCreateOptions{
				RejectStaleUpserts: true,
				OnStaleRejected:    func(issueID string) { rejected = append(rejected, issueID) },
			})
		if err != nil {
			t.Fatalf("RejectStaleUpserts CreateIssuesWithFullOptions(cl-stale) = %v, want nil (rejected, not an error)", err)
		}
		if len(rejected) != 1 || rejected[0] != "cl-stale" {
			t.Errorf("OnStaleRejected = %v, want exactly [cl-stale]", rejected)
		}
		assertTitle(t, ctx, te, "cl-stale", "stored")
	})
}
