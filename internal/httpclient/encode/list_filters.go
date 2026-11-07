package encode

import (
	"reflect"

	"github.com/steveyegge/beads/issueops"
)

// ListFiltersMembers are the issueops.ListRequest members listIssues carries
// behind the issues.list.filters behavior token: the rest of `bd list`'s filter
// vocabulary, published together in one server build.
//
// It is ONE list read three ways. listFilters (encode.go) emits each member;
// ListFiltersSet below asks whether a request carries any, which is the
// question the pre-dial gate (httpclient role_reader.go) puts to the handshake;
// and TestListFiltersMembersAreTheTokenedParameters holds the parameters these
// members map to in listTable equal to the listIssues parameters whose document
// description cites the token. A member added to one and not the others fails
// there rather than riding the wire ungated.
var ListFiltersMembers = []string{
	"TitleContains", "DescContains", "NotesContains", "ExternalContains", "ExternalRef",
	"SpecPrefix", "LabelPattern", "LabelRegex",
	"UpdatedAfter", "UpdatedBefore", "ClosedAfter", "ClosedBefore",
	"DeferAfter", "DeferBefore", "DueAfter", "DueBefore",
	"Priority", "PriorityMin", "PriorityMax",
	"EmptyDesc", "NoLabels", "NoParent", "ExcludeTypes", "ExcludeStatus",
	"PinnedFlag", "NoPinnedFlag", "DeferredFlag", "OverdueFlag",
	"MolType", "WispType",
}

// ListFiltersSet reports whether req carries any ListFiltersMembers member,
// by the encoder's own notion of populated: a non-nil pointer is set even to a
// zero value (priority 0 is a real priority), an empty slice is not.
func ListFiltersSet(req issueops.ListRequest) bool {
	rv := reflect.ValueOf(req)
	for _, name := range ListFiltersMembers {
		if populated(rv.FieldByName(name)) {
			return true
		}
	}
	return false
}
