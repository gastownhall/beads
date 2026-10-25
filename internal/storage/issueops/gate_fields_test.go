package issueops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestValidateGateFieldsBoundsEachMemberAtItsColumn(t *testing.T) {
	atBound := &types.Issue{
		AwaitType: strings.Repeat("é", MaxAwaitTypeLen),
		AwaitID:   strings.Repeat("é", types.MaxFieldLen),
		Timeout:   0,
		Waiters:   []string{strings.Repeat("w", types.MaxTextBytes-4)},
	}
	if err := ValidateGateFields(atBound); err != nil {
		t.Fatalf("every member at its bound: %v, want nil", err)
	}
	// Free-form type, blank and duplicate waiters, any issue type: no rule.
	if err := ValidateGateFields(&types.Issue{IssueType: types.TypeTask, AwaitType: "mail", Waiters: []string{"", "a", "a"}}); err != nil {
		t.Fatalf("free-form gate members: %v, want nil", err)
	}
	if err := ValidateGateFields(nil); err != nil {
		t.Fatalf("nil issue: %v, want nil", err)
	}

	for _, test := range []struct {
		name  string
		field string
		issue types.Issue
	}{
		{"await_type", publicops.GateFieldAwaitType, types.Issue{AwaitType: strings.Repeat("é", MaxAwaitTypeLen+1)}},
		{"await_id", publicops.GateFieldAwaitID, types.Issue{AwaitID: strings.Repeat("é", types.MaxFieldLen+1)}},
		{"timeout", publicops.GateFieldTimeout, types.Issue{Timeout: -time.Nanosecond}},
		{"waiters", publicops.GateFieldWaiters, types.Issue{Waiters: []string{strings.Repeat("w", types.MaxTextBytes-3)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			issue := test.issue
			assertGateFieldErr(t, ValidateGateFields(&issue), test.field)
		})
	}
}

func TestValidateGatePatchChecksOnlySetMembers(t *testing.T) {
	if err := ValidateGatePatch(publicops.IssuePatch{}); err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	if err := ValidateGatePatch(publicops.IssuePatch{AwaitID: publicops.Field[string]{Set: true}, Waiters: publicops.Field[[]string]{Set: true}}); err != nil {
		t.Fatalf("clearing patch: %v", err)
	}
	assertGateFieldErr(t, ValidateGatePatch(publicops.IssuePatch{
		AwaitID: publicops.Field[string]{Set: true, Value: strings.Repeat("a", types.MaxFieldLen+1)},
	}), publicops.GateFieldAwaitID)
	assertGateFieldErr(t, ValidateUpdateRequest(publicops.UpdateRequest{Patch: publicops.IssuePatch{
		Waiters: publicops.Field[[]string]{Set: true, Value: []string{strings.Repeat("w", types.MaxTextBytes)}},
	}}), publicops.GateFieldWaiters)
}

func TestValidatePublicCreateRequestRefusesAGateMemberByName(t *testing.T) {
	err := ValidatePublicCreateRequest(publicops.CreateRequest{Actor: "a", Issue: &types.Issue{Title: "t", Timeout: -time.Second}})
	assertGateFieldErr(t, err, publicops.GateFieldTimeout)
}

func TestUpdateFieldsMapsWaitersAsAWholeValue(t *testing.T) {
	source := []string{"a", "a"}
	updates := UpdateFields(publicops.IssuePatch{Waiters: publicops.Field[[]string]{Set: true, Value: source}})
	got, ok := updates["waiters"].([]string)
	if !ok || len(got) != 2 || got[0] != "a" || got[1] != "a" {
		t.Fatalf("waiters update = %#v, want the list as given", updates["waiters"])
	}
	source[0] = "mutated"
	if got[0] != "a" {
		t.Fatal("the waiters update aliases the caller's slice")
	}
	cleared := UpdateFields(publicops.IssuePatch{Waiters: publicops.Field[[]string]{Set: true}})
	if got, ok := cleared["waiters"].([]string); !ok || got == nil || len(got) != 0 {
		t.Fatalf("cleared waiters update = %#v, want a non-nil empty list (stored as [] rather than null)", cleared["waiters"])
	}
	if _, present := UpdateFields(publicops.IssuePatch{})["waiters"]; present {
		t.Fatal("an unset Waiters wrote the waiters key")
	}
}

func assertGateFieldErr(t *testing.T, err error, field string) {
	t.Helper()
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	var fieldErr *publicops.GateFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != field {
		t.Fatalf("error = %#v, want a *GateFieldError naming %s", err, field)
	}
}
