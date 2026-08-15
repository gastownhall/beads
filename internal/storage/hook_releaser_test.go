package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeReleaser struct {
	result issueops.ReleaseResult
	err    error
}

func (f *fakeReleaser) Release(context.Context, issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	return f.result, f.err
}

// TestHookReleaserFiresOnlyForAReleaseThatWrote is the decorator's whole
// subject: a hook script exists to observe changes, so the condition is the
// role's own Changed rather than the absence of an error.
//
// THE THIRD ROW IS THE ONE THAT EARNS THE FILE. Today the role refuses every
// shape that would not write, so nothing produces a Changed-false success and
// `err == nil` would pass every case here. That is exactly the coincidence this
// row removes: it drives the decorator with the answer the role reserves the
// right to give, and a decorator inferring the fire from err == nil goes red on
// it.
func TestHookReleaserFiresOnlyForAReleaseThatWrote(t *testing.T) {
	for _, test := range []struct {
		name     string
		inner    *fakeReleaser
		wantFire []string
	}{
		{
			name:     "a release that wrote fires the update hook",
			inner:    &fakeReleaser{result: issueops.ReleaseResult{Changed: true}},
			wantFire: []string{"release:bd-1"},
		},
		{
			name:  "a release that wrote nothing fires nothing",
			inner: &fakeReleaser{result: issueops.ReleaseResult{Changed: false}},
		},
		{
			name:  "a refusal fires nothing",
			inner: &fakeReleaser{err: errors.New("boom")},
		},
		{
			name:  "a refusal that somehow reports Changed still fires nothing",
			inner: &fakeReleaser{result: issueops.ReleaseResult{Changed: true}, err: errors.New("boom")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			releaser := &hookReleaser{inner: test.inner, hooks: recorder}

			_, err := releaser.Release(context.Background(), issueops.ReleaseRequest{
				Actor: "agent", IssueID: "bd-1",
			})
			if err != nil && test.inner.err == nil {
				t.Fatalf("Release error = %v", err)
			}
			if !reflect.DeepEqual(recorder.completions, test.wantFire) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, test.wantFire)
			}
		})
	}
}

// TestHookReleaserPassesTheResultThrough pins that the decorator reports what
// the role reported: a hook layer that rewrote a snapshot would be a hook layer
// deciding what a caller sees the release produced.
func TestHookReleaserPassesTheResultThrough(t *testing.T) {
	issue := &issueops.Issue{ID: "bd-1", RowVersion: 42}
	inner := &fakeReleaser{result: issueops.ReleaseResult{Issue: issue, Changed: true}}
	releaser := &hookReleaser{inner: inner, hooks: &recordingIssueOperationHooks{}}

	result, err := releaser.Release(context.Background(), issueops.ReleaseRequest{Actor: "agent", IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("Release error = %v", err)
	}
	if result.Issue != issue || !result.Changed {
		t.Fatalf("result = %+v, want the inner surface's answer unchanged", result)
	}
}
