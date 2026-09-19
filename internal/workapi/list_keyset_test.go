package workapi

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// TestBuildListFilterCarriesTheWholeKeysetPosition pins that the position
// reaches the filter INTACT. The builder is the single seam every
// implementation of issueops.Reader passes through, so a half-carried position
// — the instant and the id but not the priority — is not a partial feature: it
// is a priority-ordered ORDER BY walked with a created-order predicate, which
// silently drops every row of a higher-numbered priority created after the
// cursor.
func TestBuildListFilterCarriesTheWholeKeysetPosition(t *testing.T) {
	var cfg ListConfig

	at := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	priority := 2

	filter, err := BuildListFilter(issueops.ListRequest{
		SortBy:         "priority",
		AfterCreatedAt: &at,
		AfterID:        "bd-9",
		AfterPriority:  &priority,
	}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	if filter.AfterCreatedAt == nil || !filter.AfterCreatedAt.Equal(at) {
		t.Errorf("filter.AfterCreatedAt = %v, want %v", filter.AfterCreatedAt, at)
	}
	if filter.AfterID != "bd-9" {
		t.Errorf("filter.AfterID = %q, want %q", filter.AfterID, "bd-9")
	}
	if filter.AfterPriority == nil {
		t.Fatal("filter.AfterPriority is nil: the priority half of the position was dropped, so a priority-ordered page would be walked with a created-order predicate")
	}
	if *filter.AfterPriority != priority {
		t.Errorf("filter.AfterPriority = %d, want %d", *filter.AfterPriority, priority)
	}
	if filter.SortBy != "priority" {
		t.Errorf("filter.SortBy = %q, want %q — the predicate and the ORDER BY must name the same order", filter.SortBy, "priority")
	}

	// A request with no position carries none, so the absent case cannot be
	// satisfied by a builder that stamps a zero priority onto every filter.
	unpositioned, err := BuildListFilter(issueops.ListRequest{SortBy: "priority"}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter (unpositioned): %v", err)
	}
	if unpositioned.AfterPriority != nil {
		t.Errorf("filter.AfterPriority = %d on a request that sent no cursor, want nil", *unpositioned.AfterPriority)
	}
}

// TestReadyFlagRefusesThePriorityHalfOfThePosition pins that the priority half
// is refused by the --ready arm for the same reason the instant half is: the
// blocker-aware query cannot carry a keyset position at all, and a refusal that
// named only two thirds of one would let a caller "fix" their request by
// dropping the fields it named and get the wider set anyway.
func TestReadyFlagRefusesThePriorityHalfOfThePosition(t *testing.T) {
	var cfg ListConfig

	at := time.Now().UTC().Add(-time.Hour)
	priority := 1
	_, err := BuildListFilter(issueops.ListRequest{
		ReadyFlag:      true,
		AfterCreatedAt: &at,
		AfterID:        "bd-9",
		AfterPriority:  &priority,
	}, cfg)
	if err == nil {
		t.Fatal("BuildListFilter accepted a keyset position under --ready; want ErrValidation")
	}
	for _, want := range []string{"AfterCreatedAt", "AfterPriority"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s, so a caller cannot tell which part of their position was the problem: %v", want, err)
		}
	}
}
