package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeMetadataCAS struct {
	result issueops.CompareAndSetKeyResult
	err    error
}

func (f *fakeMetadataCAS) CompareAndSetKey(context.Context, issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	return f.result, f.err
}

func rawValue(s string) *json.RawMessage {
	value := json.RawMessage(s)
	return &value
}

// TestHookMetadataCASFiresOnlyForASwapThatMovedTheValue is the decorator's
// whole subject. Swapped answers the PRECONDITION, so it is not the question a
// hook script cares about: the two cases below where it is true and nothing
// fired are the two the role documents as writing nothing.
func TestHookMetadataCASFiresOnlyForASwapThatMovedTheValue(t *testing.T) {
	for _, test := range []struct {
		name     string
		request  issueops.CompareAndSetKeyRequest
		inner    *fakeMetadataCAS
		wantFire []string
	}{
		{
			name:     "a swap that moved the value fires the update hook",
			request:  issueops.CompareAndSetKeyRequest{IssueID: "bd-1", Value: rawValue(`"held"`)},
			inner:    &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}},
			wantFire: []string{"metadata:bd-1"},
		},
		{
			name: "a delete fires the update hook",
			request: issueops.CompareAndSetKeyRequest{
				IssueID: "bd-1", Expected: rawValue(`"held"`),
			},
			inner:    &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}},
			wantFire: []string{"metadata:bd-1"},
		},
		{
			name: "a value-to-itself swap fires nothing",
			request: issueops.CompareAndSetKeyRequest{
				IssueID: "bd-1", Expected: rawValue(`{"a":1,"b":2}`), Value: rawValue("{ \"b\":2, \"a\":1 }"),
			},
			inner: &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}},
		},
		{
			name: "an absent-to-absent swap fires nothing",
			request: issueops.CompareAndSetKeyRequest{
				IssueID: "bd-1",
			},
			inner: &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}},
		},
		{
			name:    "a lost race fires nothing",
			request: issueops.CompareAndSetKeyRequest{IssueID: "bd-1", Value: rawValue(`"held"`)},
			inner:   &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: false, Current: rawValue(`"other"`)}},
		},
		{
			name:    "an error fires nothing",
			request: issueops.CompareAndSetKeyRequest{IssueID: "bd-1", Value: rawValue(`"held"`)},
			inner:   &fakeMetadataCAS{err: errors.New("boom")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			cas := &hookMetadataCAS{inner: test.inner, hooks: recorder}

			if _, err := cas.CompareAndSetKey(context.Background(), test.request); err != nil && test.inner.err == nil {
				t.Fatalf("CompareAndSetKey error = %v", err)
			}
			if !reflect.DeepEqual(recorder.completions, test.wantFire) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, test.wantFire)
			}
		})
	}
}

// TestHookMetadataCASPassesTheResultThrough pins that the decorator reports
// what the role reported: a hook layer that rewrote a verdict would be a hook
// layer deciding a race.
func TestHookMetadataCASPassesTheResultThrough(t *testing.T) {
	current := json.RawMessage(`"other"`)
	inner := &fakeMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: false, Current: &current}}
	cas := &hookMetadataCAS{inner: inner, hooks: &recordingIssueOperationHooks{}}

	result, err := cas.CompareAndSetKey(context.Background(), issueops.CompareAndSetKeyRequest{IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("CompareAndSetKey error = %v", err)
	}
	if result.Swapped || result.Current != &current {
		t.Fatalf("result = %+v, want the inner surface's answer unchanged", result)
	}
}
