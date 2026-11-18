package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file pins the THREE-ITEM STATE-CHANGE SHAPE bd set-state submits as one
// BatchApplier request (GH#5637): create the event, add its parent-child edge
// to the subject, then patch the subject's labels. The command used to make
// the event create and the label patch two separately committed lifecycle
// calls, so a refused patch left a committed event for a transition that never
// happened. One request is one transaction, so the cases here state the
// all-or-nothing promise for exactly this shape, on every backend, including
// the per-item template waiver (UpdateItem.AllowTemplate) the shape needs
// because bd set-state has always edited templates.

// stateChangeBatchRequest is the request bd set-state composes, with an
// explicit event id so a case can read the event's row back by id.
func stateChangeBatchRequest(eventID, subject, addLabel, removeLabel string, allowTemplate bool) publicops.ApplyBatchRequest {
	event := &types.Issue{
		ID:          eventID,
		Title:       "State change: patrol → " + addLabel,
		Description: "Changed patrol",
		Status:      types.StatusClosed,
		Priority:    4,
		IssueType:   types.TypeEvent,
	}
	patch := publicops.LabelPatch{Add: []string{addLabel}}
	if removeLabel != "" {
		patch.Remove = []string{removeLabel}
	}
	return publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Provenance:    "bd: set-state " + subject,
		Items: []publicops.ApplyItem{
			batchApplyCreate("event", event),
			batchApplyDepAdd(publicops.Ref{Key: "event"}, publicops.Ref{ID: subject}, types.DepParentChild, ""),
			{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{
				Target:        publicops.Ref{ID: subject},
				Patch:         publicops.IssuePatch{Labels: patch},
				AllowTemplate: allowTemplate,
			}},
		},
	}
}

// stateChangeLabelCount counts one label on one durable row, read off the raw
// labels table.
func stateChangeLabelCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id, label string) int {
	t.Helper()
	return batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{id, label})
}

// stateChangeParentEdges counts the parent-child edges from an event to its
// subject across both dependency tables.
func stateChangeParentEdges(t *testing.T, ctx context.Context, fixture BatchApplyFixture, event, subject string) int {
	t.Helper()
	const query = `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = 'parent-child'
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND type = 'parent-child'
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?)`
	return batchApplyCount(t, ctx, fixture, query, []any{event, subject, event, subject})
}

// stateChangeSeedSubject seeds a durable subject carrying patrol:active, and
// fatals if the seed did not land that label: every case below asserts the
// label is still there after a refusal, which would pass vacuously on a seed
// that never wrote it.
func stateChangeSeedSubject(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id string, template bool) {
	t.Helper()
	issue := batchApplyIssue(id, id)
	issue.IsTemplate = template
	issue.Labels = []string{"patrol:active"}
	if err := fixture.CreateIssue(ctx, issue, "apply-seed"); err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
	if got := stateChangeLabelCount(t, ctx, fixture, id, "patrol:active"); got != 1 {
		t.Fatalf("seeded %s carries patrol:active %d times, want 1", id, got)
	}
}

// RunBatchApplyStateChangeOnATemplateHonorsAllowTemplate pins
// UpdateItem.AllowTemplate on the state-change shape. Without it the update
// item refuses with *TemplateReadOnlyError wrapped in the update's *ItemError
// and the event and edge ahead of it roll back; with it the whole request lands
// and the template is still a template afterwards — the waiver stands one
// guard down for one item and edits nothing about the row's identity.
func RunBatchApplyStateChangeOnATemplateHonorsAllowTemplate(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	template := fixture.IssuePrefix + "-sctmpl-template"
	refusedEvent := fixture.IssuePrefix + "-sctmpl-refused"
	landedEvent := fixture.IssuePrefix + "-sctmpl-landed"
	stateChangeSeedSubject(t, ctx, fixture, template, true)

	_, err := fixture.BatchApplier.ApplyBatch(ctx,
		stateChangeBatchRequest(refusedEvent, template, "patrol:muted", "patrol:active", false))
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("state change on template %s without AllowTemplate: error = %v, want an *ItemError", template, err)
	}
	if itemErr.Kind != publicops.ItemUpdate || itemErr.Index != 2 {
		t.Errorf("ItemError = %#v, want the update item at index 2", itemErr)
	}
	var refusal *publicops.TemplateReadOnlyError
	if !errors.As(err, &refusal) || refusal.IssueID != template {
		t.Errorf("state change on template = %v, want *TemplateReadOnlyError naming %s", err, template)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", refusedEvent, 0)
	assertBatchApplyRowCount(t, ctx, fixture, "wisps", refusedEvent, 0)
	assertBatchApplyEdgeCount(t, ctx, fixture, refusedEvent, template, 0)
	if got := stateChangeLabelCount(t, ctx, fixture, template, "patrol:active"); got != 1 {
		t.Errorf("%s carries patrol:active %d times after the refusal, want 1", template, got)
	}
	if got := stateChangeLabelCount(t, ctx, fixture, template, "patrol:muted"); got != 0 {
		t.Errorf("%s carries patrol:muted %d times after the refusal, want 0", template, got)
	}

	result := batchApplyMust(t, ctx, fixture,
		stateChangeBatchRequest(landedEvent, template, "patrol:muted", "patrol:active", true))
	if got := result.Keys["event"]; got != landedEvent {
		t.Errorf("Keys[event] = %q, want %q", got, landedEvent)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", landedEvent, 1)
	if got := batchApplyColumn(t, ctx, fixture, "issue_type", landedEvent); got != string(types.TypeEvent) {
		t.Errorf("%s issue_type = %q, want %q", landedEvent, got, types.TypeEvent)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", landedEvent); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q, want %q", landedEvent, got, types.StatusClosed)
	}
	if got := stateChangeParentEdges(t, ctx, fixture, landedEvent, template); got != 1 {
		t.Errorf("parent-child edges %s -> %s = %d, want 1", landedEvent, template, got)
	}
	if got := stateChangeLabelCount(t, ctx, fixture, template, "patrol:active"); got != 0 {
		t.Errorf("%s carries patrol:active %d times after the waived state change, want 0", template, got)
	}
	if got := stateChangeLabelCount(t, ctx, fixture, template, "patrol:muted"); got != 1 {
		t.Errorf("%s carries patrol:muted %d times after the waived state change, want 1", template, got)
	}
	var isTemplate bool
	if err := fixture.QueryScalar(ctx, "SELECT is_template FROM issues WHERE id = ?", []any{template}, &isTemplate); err != nil {
		t.Fatalf("reading is_template for %s: %v", template, err)
	}
	if !isTemplate {
		t.Errorf("%s is no longer a template after an AllowTemplate state change", template)
	}
}

// RunBatchApplyStateChangeRefusedAtTheEventOrItsEdgeWritesNothing pins the
// other half of the state-change promise: when the event create (item 0) or
// its parent-child edge (item 1) is refused, ApplyBatch returns that item's
// *ItemError, the subject's labels are untouched, and no event, edge or
// history entry survives. Each refusal is deterministic on every backend: the
// create takes an occupied id (ErrAlreadyExists), and the edge runs from a
// dotted grandchild id to its grandparent, which the dotted-id hierarchy rule
// refuses only once the create has minted it.
func RunBatchApplyStateChangeRefusedAtTheEventOrItsEdgeWritesNothing(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	subject := fixture.IssuePrefix + "-screfuse"
	occupied := fixture.IssuePrefix + "-screfuse-occupied"
	stateChangeSeedSubject(t, ctx, fixture, subject, false)
	batchApplySeedIssue(t, ctx, fixture, subject+".1", types.StatusOpen)
	batchApplySeedIssue(t, ctx, fixture, occupied, types.StatusOpen)
	batchApplySettle(t, ctx, fixture)
	observeHistory := fixture.CountHistory != nil && fixture.CommitPending != nil

	for _, refused := range []struct {
		name, event string
		index       int
		kind        publicops.ItemKind
		want        error
	}{
		{"the event create", occupied, 0, publicops.ItemCreate, publicops.ErrAlreadyExists},
		{"the parent edge", subject + ".1.1", 1, publicops.ItemDepAdd, publicops.ErrValidation},
	} {
		t.Run(refused.name, func(t *testing.T) {
			historyBefore := 0
			if observeHistory {
				historyBefore = batchApplyHistory(t, ctx, fixture)
			}
			_, err := fixture.BatchApplier.ApplyBatch(ctx,
				stateChangeBatchRequest(refused.event, subject, "patrol:muted", "patrol:active", false))
			if !errors.Is(err, refused.want) {
				t.Errorf("error = %v, want %v", err, refused.want)
			}
			var itemErr *publicops.ItemError
			if !errors.As(err, &itemErr) {
				t.Fatalf("error = %v, want an *ItemError naming %s", err, refused.name)
			}
			if itemErr.Index != refused.index || itemErr.Kind != refused.kind {
				t.Errorf("ItemError = %#v, want Index %d and Kind %q", itemErr, refused.index, refused.kind)
			}
			if got := stateChangeLabelCount(t, ctx, fixture, subject, "patrol:active"); got != 1 {
				t.Errorf("%s carries patrol:active %d times after the refusal, want 1", subject, got)
			}
			if got := stateChangeLabelCount(t, ctx, fixture, subject, "patrol:muted"); got != 0 {
				t.Errorf("%s carries patrol:muted %d times after the refusal, want 0", subject, got)
			}
			if got := batchApplyCount(t, ctx, fixture,
				"SELECT (SELECT COUNT(*) FROM issues WHERE id = ? AND issue_type = 'event') + (SELECT COUNT(*) FROM wisps WHERE id = ?)",
				[]any{refused.event, refused.event}); got != 0 {
				t.Errorf("%d event row(s) %s survive the refusal, want none", got, refused.event)
			}
			if got := stateChangeParentEdges(t, ctx, fixture, refused.event, subject); got != 0 {
				t.Errorf("%d parent-child edge(s) %s -> %s survive the refusal, want none", got, refused.event, subject)
			}
			if observeHistory {
				if got := batchApplyHistory(t, ctx, fixture) - historyBefore; got != 0 {
					t.Errorf("the refused state change recorded %d history entries, want none", got)
				}
			}
		})
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", subject+".1.1", 0)
}
