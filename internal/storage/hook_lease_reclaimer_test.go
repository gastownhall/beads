package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeLeaseReclaimer struct {
	result issueops.ReclaimResult
	err    error
}

func (f *fakeLeaseReclaimer) Reclaim(context.Context, issueops.ReclaimRequest) (issueops.ReclaimResult, error) {
	return f.result, f.err
}

// TestHookLeaseReclaimerFiresOncePerReclaimedRow is the decorator's whole
// subject: one on_update completion for every row the sweep reverted, in the
// sweep's order, and none at all for a refusal — even a refusal that somehow
// carries rows, because the role leaves its result unspecified on error.
func TestHookLeaseReclaimerFiresOncePerReclaimedRow(t *testing.T) {
	for _, test := range []struct {
		name     string
		inner    *fakeLeaseReclaimer
		wantFire []string
	}{
		{
			name: "every reverted row fires once",
			inner: &fakeLeaseReclaimer{result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{
				{ID: "bd-1"}, {ID: "bd-2"},
			}}},
			wantFire: []string{"reclaim:bd-1", "reclaim:bd-2"},
		},
		{
			name:  "a sweep that reverted nothing fires nothing",
			inner: &fakeLeaseReclaimer{result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{}}},
		},
		{
			name:  "a refusal fires nothing",
			inner: &fakeLeaseReclaimer{err: errors.New("boom")},
		},
		{
			name: "a refusal that carries rows still fires nothing",
			inner: &fakeLeaseReclaimer{
				result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{{ID: "bd-1"}}},
				err:    errors.New("boom"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			reclaimer := &hookLeaseReclaimer{inner: test.inner, hooks: recorder}

			_, err := reclaimer.Reclaim(context.Background(), issueops.ReclaimRequest{Actor: "reaper"})
			if (err != nil) != (test.inner.err != nil) {
				t.Fatalf("Reclaim error = %v, want %v", err, test.inner.err)
			}
			if !reflect.DeepEqual(recorder.completions, test.wantFire) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, test.wantFire)
			}
		})
	}
}

// TestHookLeaseReclaimerPassesTheResultThrough pins that the decorator reports
// exactly what the role reported, revisions included: a hook layer that
// rewrote the result would be deciding which token a caller feeds forward.
func TestHookLeaseReclaimerPassesTheResultThrough(t *testing.T) {
	want := issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{{ID: "bd-1", PreviousOwner: "w", Revision: "42"}}}
	reclaimer := &hookLeaseReclaimer{inner: &fakeLeaseReclaimer{result: want}, hooks: &recordingIssueOperationHooks{}}

	got, err := reclaimer.Reclaim(context.Background(), issueops.ReclaimRequest{Actor: "reaper"})
	if err != nil {
		t.Fatalf("Reclaim error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reclaim result = %+v, want %+v", got, want)
	}
}
