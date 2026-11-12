package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// squashDependentsReader is a molReader that answers only
// GetDependentsWithMetadata, from a fixed map, and records which ids it was
// asked about. The embedded nil interface makes any other read panic, so a
// test fails loudly if squashKeptSteps consults the blocked listing, whose
// stored is_blocked flag an older bd can leave stale, instead of the edges.
type squashDependentsReader struct {
	molReader
	dependents map[string][]*types.IssueWithDependencyMetadata
	err        error
	asked      []string
}

func (r *squashDependentsReader) GetDependentsWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	r.asked = append(r.asked, id)
	if r.err != nil {
		return nil, r.err
	}
	return r.dependents[id], nil
}

func squashDependent(id string, status types.Status, depType types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{Issue: types.Issue{ID: id, Status: status}, DependencyType: depType}
}

// TestSquashKeptStepsKeepsAClosedStepALiveConditionalFallbackWaitsOn pins
// which children bd mol squash keeps (GH#5577): a closed step whose close
// reason is not a failure, with a conditional-blocks dependent that is not
// closed and is not itself squashed away. Deleting such a step drops the edge
// and puts the fallback in ready work after a success. Every other child is
// deleted as before, and the steps that cannot hold a fallback back (open, or
// closed with a failure reason) are not read at all.
func TestSquashKeptStepsKeepsAClosedStepALiveConditionalFallbackWaitsOn(t *testing.T) {
	closed := func(id, reason string) *types.Issue {
		return &types.Issue{ID: id, Status: types.StatusClosed, CloseReason: reason}
	}
	// An arm bonded onto the step, still open: kept.
	deploy := closed("sq-deploy", "deployed ok")
	// A deferred fallback still waits: kept.
	migrate := closed("sq-migrate", "all steps complete")
	// Its fallback was closed, and only a blocks edge is live: deleted.
	verify := closed("sq-verify", "verified")
	// A failure close already released its fallback: deleted, not read.
	canary := closed("sq-canary", "failed: smoke test red")
	// Its only live conditional dependent is a step this squash deletes.
	build := closed("sq-build", "built")
	fallbackStep := &types.Issue{ID: "sq-build-fallback", Status: types.StatusOpen}
	// Squash deletes an open step as before: not read.
	pending := &types.Issue{ID: "sq-pending", Status: types.StatusOpen}

	r := &squashDependentsReader{dependents: map[string][]*types.IssueWithDependencyMetadata{
		deploy.ID: {
			squashDependent("sq-root", types.StatusClosed, types.DepParentChild),
			squashDependent("sq-arm", types.StatusOpen, types.DepConditionalBlocks),
		},
		migrate.ID: {squashDependent("sq-restore", types.StatusDeferred, types.DepConditionalBlocks)},
		verify.ID: {
			squashDependent("sq-unused-fallback", types.StatusClosed, types.DepConditionalBlocks),
			squashDependent("sq-announce", types.StatusOpen, types.DepBlocks),
		},
		canary.ID:  {squashDependent("sq-canary-fallback", types.StatusOpen, types.DepConditionalBlocks)},
		build.ID:   {squashDependent(fallbackStep.ID, types.StatusOpen, types.DepConditionalBlocks)},
		pending.ID: {squashDependent("sq-pending-fallback", types.StatusOpen, types.DepConditionalBlocks)},
	}}
	children := []*types.Issue{deploy, migrate, verify, canary, build, fallbackStep, pending}

	kept, err := squashKeptSteps(context.Background(), r, children, false)
	if err != nil {
		t.Fatalf("squashKeptSteps: %v", err)
	}
	if want := map[string]bool{deploy.ID: true, migrate.ID: true}; !reflect.DeepEqual(kept, want) {
		t.Errorf("kept = %v, want %v: a closed step a live conditional-blocks fallback waits on must survive the squash, and nothing else", kept, want)
	}
	if want := []string{deploy.ID, migrate.ID, verify.ID, build.ID}; !reflect.DeepEqual(r.asked, want) {
		t.Errorf("read the dependents of %v, want %v: only a closed step whose reason is not a failure can hold a fallback back", r.asked, want)
	}

	r.asked = nil
	if kept, err := squashKeptSteps(context.Background(), r, children, true); err != nil || kept != nil || len(r.asked) != 0 {
		t.Errorf("--keep-children: kept=%v err=%v asked=%v, want nothing read: the squash deletes nothing", kept, err, r.asked)
	}

	r.err = errors.New("connection reset")
	if _, err := squashKeptSteps(context.Background(), r, children, false); err == nil || !strings.Contains(err.Error(), deploy.ID) {
		t.Errorf("err = %v, want the read failure naming %s: a squash that cannot tell what waits on a step must not delete it", err, deploy.ID)
	}
}

// squashRecordingWriter is a molWriter that records the squash's writes. It
// answers no read: the embedded nil interface panics on one.
type squashRecordingWriter struct {
	molWriter
	deleted, closed []string
}

func (w *squashRecordingWriter) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	issue.ID = "sq-digest"
	return nil
}

func (w *squashRecordingWriter) AddDependency(context.Context, *types.Dependency, string) error {
	return nil
}

func (w *squashRecordingWriter) DeleteIssue(_ context.Context, id, _ string) error {
	w.deleted = append(w.deleted, id)
	return nil
}

func (w *squashRecordingWriter) CloseIssue(_ context.Context, id, _, _ string) error {
	w.closed = append(w.closed, id)
	return nil
}

func (w *squashRecordingWriter) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	return nil
}

// TestSquashMoleculeIntoKeepsTheStepsAFallbackWaitsOn pins the write side: a
// kept step is summarized in the digest and reported in kept_ids, but never
// deleted, while every other child is, and the wisp root still auto-closes.
func TestSquashMoleculeIntoKeepsTheStepsAFallbackWaitsOn(t *testing.T) {
	root := &types.Issue{ID: "sq-root", Title: "ship", Ephemeral: true}
	children := []*types.Issue{
		{ID: "sq-deploy", Title: "Deploy", Status: types.StatusClosed, CloseReason: "deployed ok", Ephemeral: true},
		{ID: "sq-verify", Title: "Verify", Status: types.StatusClosed, CloseReason: "verified", Ephemeral: true},
	}
	kept := map[string]bool{"sq-deploy": true}

	w := &squashRecordingWriter{}
	result, err := squashMoleculeInto(context.Background(), w, root, children, kept, false, "", "test")
	if err != nil {
		t.Fatalf("squashMoleculeInto: %v", err)
	}
	if want := []string{"sq-verify"}; !reflect.DeepEqual(w.deleted, want) {
		t.Errorf("deleted %v, want %v: the step a fallback waits on must not be deleted", w.deleted, want)
	}
	if want := []string{"sq-deploy"}; !reflect.DeepEqual(result.KeptIDs, want) {
		t.Errorf("KeptIDs = %v, want %v", result.KeptIDs, want)
	}
	if result.DeletedCount != 1 || result.SquashedCount != 2 {
		t.Errorf("deleted/squashed = %d/%d, want 1/2: the digest still summarizes the kept step", result.DeletedCount, result.SquashedCount)
	}
	if !result.WispSquash || !reflect.DeepEqual(w.closed, []string{root.ID}) {
		t.Errorf("WispSquash=%v closed=%v, want the wisp root auto-closed", result.WispSquash, w.closed)
	}

	w = &squashRecordingWriter{}
	result, err = squashMoleculeInto(context.Background(), w, root, children, kept, true, "", "test")
	if err != nil {
		t.Fatalf("squashMoleculeInto --keep-children: %v", err)
	}
	if len(w.deleted) != 0 || len(result.KeptIDs) != 0 || !result.KeptChildren {
		t.Errorf("--keep-children: deleted=%v KeptIDs=%v KeptChildren=%v, want nothing deleted and no kept_ids", w.deleted, result.KeptIDs, result.KeptChildren)
	}
}
