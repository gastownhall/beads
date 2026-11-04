package issueops

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// These are the parts of issueops.BatchGetter's answer that are decided
// without a database — the request rules, the first-mention collapse and the
// ordered split into Issues and Missing — so they are pinned here in
// milliseconds and the conformance contract is left to assert what only a real
// backend can show.

func TestValidateGetManyRequestChecksTheCapBeforeTheBlankScan(t *testing.T) {
	// A request over the cap that ALSO carries a blank id has to report the
	// cap: a caller told only about the blank would fix it and get a second,
	// different refusal back for the same request. The blank sits at position
	// 0, so a scan that ran first could not miss it.
	ids := slices.Repeat([]string{"bd-1"}, publicops.MaxGetManyIDs+1)
	ids[0] = ""

	err := ValidateGetManyRequest(publicops.GetManyRequest{IDs: ids})
	var tooMany *publicops.TooManyIDsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("an over-cap request with a blank id = %v, want *TooManyIDsError", err)
	}
	if tooMany.Requested != len(ids) || tooMany.Cap != publicops.MaxGetManyIDs {
		t.Errorf("refusal = %+v, want Requested %d and Cap %d", *tooMany, len(ids), publicops.MaxGetManyIDs)
	}
	if !errors.Is(err, storage.ErrValidation) {
		t.Errorf("the cap refusal %v does not match ErrValidation", err)
	}
}

func TestValidateGetManyRequestRefusals(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
		want bool
	}{
		{"no ids at all", nil, false},
		{"exactly the cap's worth of mentions", slices.Repeat([]string{"bd-1"}, publicops.MaxGetManyIDs), false},
		{"one mention past the cap", slices.Repeat([]string{"bd-1"}, publicops.MaxGetManyIDs+1), true},
		{"an empty id beside a real one", []string{"bd-1", ""}, true},
	} {
		err := ValidateGetManyRequest(publicops.GetManyRequest{IDs: test.ids})
		if got := errors.Is(err, storage.ErrValidation); got != test.want {
			t.Errorf("%s: ErrValidation = %t (err %v), want %t", test.name, got, err, test.want)
		}
	}
}

func TestDedupeGetManyIDsKeepsTheFirstMentionAndLeavesTheRequestAlone(t *testing.T) {
	// The repeat of bd-b follows a different id, so a collapse that sorted, or
	// that kept each id's LAST mention, would answer bd-a before bd-b. The
	// padded id is a different exact id, not a duplicate to trim away.
	request := []string{"bd-b", "bd-a", "bd-b", " bd-a "}
	sent := slices.Clone(request)

	got := DedupeGetManyIDs(request)
	if want := []string{"bd-b", "bd-a", " bd-a "}; !slices.Equal(got, want) {
		t.Errorf("DedupeGetManyIDs(%q) = %q, want %q", sent, got, want)
	}
	// GetManyRequest promises IDs is never written through, and collapsing in
	// place is the idiomatic way to break that promise.
	if !slices.Equal(request, sent) {
		t.Errorf("DedupeGetManyIDs rewrote the caller's slice to %q, want %q", request, sent)
	}
}

func TestFinishGetManySplitsTheRequestInItsOwnOrder(t *testing.T) {
	// The id list is the order, not the found map — whose iteration order Go
	// randomizes on purpose — and neither half is sorted, so a sorting
	// assembly would answer differently on both.
	result := FinishGetMany(
		[]string{"c", "ghost-2", "a", "ghost-1"},
		map[string]*publicops.Issue{"a": {ID: "a"}, "c": {ID: "c"}},
	)

	var issueIDs []string
	for _, issue := range result.Issues {
		issueIDs = append(issueIDs, issue.ID)
	}
	if want := []string{"c", "a"}; !slices.Equal(issueIDs, want) {
		t.Errorf("Issues = %q, want %q (the request's order)", issueIDs, want)
	}
	if want := []string{"ghost-2", "ghost-1"}; !slices.Equal(result.Missing, want) {
		t.Errorf("Missing = %q, want %q (the request's order)", result.Missing, want)
	}
}

func TestFinishGetManyAnswersNeverNil(t *testing.T) {
	// An all-found request answers with an empty, non-nil Missing, and an empty
	// request with two: a front door that marshals them emits [] rather than
	// null.
	allFound := FinishGetMany([]string{"a"}, map[string]*publicops.Issue{"a": {ID: "a"}})
	if allFound.Missing == nil || len(allFound.Missing) != 0 {
		t.Errorf("Missing = %#v for an all-found request, want an empty, non-nil slice", allFound.Missing)
	}

	empty := FinishGetMany(nil, nil)
	if empty.Issues == nil || len(empty.Issues) != 0 {
		t.Errorf("Issues = %#v for an empty request, want an empty, non-nil slice", empty.Issues)
	}
	if empty.Missing == nil || len(empty.Missing) != 0 {
		t.Errorf("Missing = %#v for an empty request, want an empty, non-nil slice", empty.Missing)
	}
}

func TestExecuteGetManyRefusesOverTheCapBeforeTheQuery(t *testing.T) {
	// A mock with no expectations fails any statement it is handed, so the
	// refusal is proved to come from the body's own check rather than from
	// storage. The request repeats ONE id past the cap, so a body that
	// collapsed repeats before validating would have let it through to the
	// query instead.
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	_, err = ExecuteGetMany(context.Background(), db, publicops.GetManyRequest{
		IDs: slices.Repeat([]string{"bd-1"}, publicops.MaxGetManyIDs+1),
	})
	var tooMany *publicops.TooManyIDsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("one id repeated past the cap = %v, want *TooManyIDsError before any query", err)
	}
}
