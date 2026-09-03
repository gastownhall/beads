package workapi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestBuildReadyCountFilterIsTheUnboundedReadyFilter is the filter-level half
// of the identity issueops.ReadyCounter promises: the predicate a count runs is
// the predicate the listing runs, with the page taken off. It is asserted
// against BuildReadyFilter rather than field by field so a new ReadyRequest
// field the count builder forgot to carry shows up as a difference.
//
// Brief is in the fixture because it is the field most likely to be dropped
// here by an author reasoning that a count returns no rows: the store-backed
// counter's indexed COUNT(*) ignores it, but the unit-of-work counter sizes the
// set by running the unbounded page (uow/ready_counter.go), where dropping it
// hydrates every heavy column of the entire ready set.
func TestBuildReadyCountFilterIsTheUnboundedReadyFilter(t *testing.T) {
	priority := 1
	request := issueops.ReadyRequest{
		Brief:            true,
		IssueType:        "mr",
		Assignee:         "alice",
		Labels:           []string{" alpha ", "alpha"},
		LabelsAny:        []string{"gamma"},
		ExcludeLabels:    []string{" delta "},
		LabelPattern:     "tech-*",
		LabelRegex:       "^tech-",
		Priority:         &priority,
		ParentID:         "bd-parent",
		IncludeDeferred:  true,
		IncludeEphemeral: true,
		ExcludeTypes:     []string{"epic, mol"},
		MetadataFields:   map[string]string{"team": "core"},
		HasMetadataKey:   "team",
		Sort:             "priority",
	}

	counted, err := BuildReadyCountFilter(request)
	if err != nil {
		t.Fatalf("BuildReadyCountFilter() error = %v", err)
	}

	unlimited := 0
	listing := request
	listing.Limit = &unlimited
	want, err := BuildReadyFilter(listing)
	if err != nil {
		t.Fatalf("BuildReadyFilter() error = %v", err)
	}
	if !reflect.DeepEqual(counted, want) {
		t.Errorf("BuildReadyCountFilter() = %+v\nBuildReadyFilter(Limit=0) = %+v\nthe count and the listing must run one predicate", counted, want)
	}
	if counted.Limit != 0 {
		t.Errorf("counted filter Limit = %d, want 0: a nil Limit takes the shared page default, so an unbounded count has to say so", counted.Limit)
	}
	if counted.Offset != 0 {
		t.Errorf("counted filter Offset = %d, want 0", counted.Offset)
	}
	// Asserted absolutely and not only through the comparison above, which
	// holds just as well if BuildReadyFilter stops resolving Brief at all: both
	// sides would read false together and the projection would be gone from the
	// count and the listing at once.
	if !counted.Lite {
		t.Error("counted filter Lite = false, want true: a request that asked for the projection must not be counted with the full column list")
	}
}

// TestBuildReadyCountFilterRefusesAPage pins the two deterministic refusals the
// role states (issueops/readycounter.go:74-86). The explicitly-unlimited limit
// is refused with the rest: accepting the pointer here would make "unset" and
// "explicitly unlimited" mean different things to a count than to a page.
func TestBuildReadyCountFilterRefusesAPage(t *testing.T) {
	limit := 10
	unlimited := 0
	for _, test := range []struct {
		name    string
		request issueops.ReadyRequest
	}{
		{"limit", issueops.ReadyRequest{Sort: "priority", Limit: &limit}},
		{"explicitly unlimited limit", issueops.ReadyRequest{Sort: "priority", Limit: &unlimited}},
		{"offset", issueops.ReadyRequest{Sort: "priority", Offset: 5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildReadyCountFilter(test.request); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("BuildReadyCountFilter() error = %v, want ErrValidation", err)
			}
		})
	}
}

// TestBuildReadyCountFilterLeavesTheRequestAlone is the builder-level half of
// the snapshot promise. The zeroed limit goes on a local copy, so a caller that
// counts and then lists with the same request value still has the request it
// wrote.
func TestBuildReadyCountFilterLeavesTheRequestAlone(t *testing.T) {
	request := issueops.ReadyRequest{Sort: "priority", Labels: []string{"alpha"}}
	want := issueops.ReadyRequest{Sort: "priority", Labels: []string{"alpha"}}

	if _, err := BuildReadyCountFilter(request); err != nil {
		t.Fatalf("BuildReadyCountFilter() error = %v", err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("BuildReadyCountFilter mutated the caller's request: got %+v, want %+v", request, want)
	}
}

// TestBuildReadyCountFilterRefusesAnInvalidSort keeps the count's refusals a
// SUPERSET of the listing's: a sort policy the listing rejects is not quietly
// accepted here because a count has no order.
func TestBuildReadyCountFilterRefusesAnInvalidSort(t *testing.T) {
	if _, err := BuildReadyCountFilter(issueops.ReadyRequest{Sort: "bogus"}); !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("BuildReadyCountFilter(sort=bogus) error = %v, want ErrValidation", err)
	}
}
