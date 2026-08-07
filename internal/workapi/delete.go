package workapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/issueops"
)

// The shared, DATABASE-FREE half of issueops.Deleter: what a delete request
// means before anything is read, and how the two answers that name rows —
// the orphan list and the dependents refusal — are ordered.
//
// Every implementation runs these, so `bd delete` has one definition of a
// malformed request rather than one per backend.
//
// What is NOT here is the deletion. The existence probe, the guard and the
// erasure need one transaction (issueops.Deleter.Delete); the bodies live in
// internal/storage/issueops/delete.go and in the unit-of-work provider.

// ValidateDeleteRequest applies the request rules every Deleter implementation
// shares, before anything is read.
//
// There is deliberately no require-a-filter analog of the sweep gate here: a
// delete request carries no predicate at all, so a caller cannot spell
// "everything" without typing every id. The guard that does matter — dependents
// outside the request — needs the graph and therefore lives in the bodies.
func ValidateDeleteRequest(in issueops.DeleteRequest) error {
	if len(in.IDs) == 0 {
		return fmt.Errorf("%w: delete requires at least one issue id", issueops.ErrValidation)
	}
	for i, id := range in.IDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: delete id at position %d is blank", issueops.ErrValidation, i)
		}
	}
	return nil
}

// NormalizeDeleteIDs collapses duplicates, keeping the caller's FIRST mention
// of each id, and trims surrounding whitespace.
//
// First-mention order rather than sorted, because it is the order the front
// doors echo back in their "issues not found" line and in the confirmation
// hint they print.
//
// It assumes a request already accepted by ValidateDeleteRequest, so no entry
// trims to empty.
func NormalizeDeleteIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// SortedDeleteIDs copies and sorts a set of ids ascending. It is what puts
// DeleteResult.Orphaned and DependentsOutsideRequestError.Dependents in the
// order issueops.DeleteResult promises, from bodies that collect them out of
// maps and would otherwise publish Go's map order.
func SortedDeleteIDs(ids map[string]bool) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
