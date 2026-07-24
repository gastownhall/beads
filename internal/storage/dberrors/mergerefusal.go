package dberrors

import (
	"regexp"
	"strings"
)

// Case-insensitive to stay consistent with IsAncestorPKMismatch's lowercased
// substring match: a casing change in Dolt's message must not produce
// "detected, but table unknown".
var ancestorPKTableRe = regexp.MustCompile(`(?i)cannot merge because table (\S+) has different primary keys`)

// IsAncestorPKMismatch checks whether the error is Dolt's hard refusal to
// merge a table whose primary key set differs between the merging heads or in
// their common ancestor (merge.ErrMergeWithDifferentPks /
// ErrMergeWithDifferentPksFromAncestor: "cannot merge because table X has
// different primary keys[ in its common ancestor]"). This is the signature of
// a schema fork where clones reshaped a table's primary key independently —
// e.g. two clones straddling the 0041/0043/0050 dependencies PK reshape
// (#4259). Dolt refuses the merge before any row conflicts materialize, so
// the pull auto-resolver never gets a chance to run, and retrying can never
// converge the clones: recovery requires re-cloning from one canonical side.
//
// TestCrossUpgradeBoundaryMerge (internal/storage/dolt) pins this match
// against the error a real Dolt merge produces across that boundary.
func IsAncestorPKMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot merge because table") &&
		strings.Contains(msg, "different primary keys")
}

// AncestorPKMismatchTable extracts the table name from a Dolt
// different-primary-keys merge refusal, or "" if it cannot be determined.
func AncestorPKMismatchTable(err error) string {
	if err == nil {
		return ""
	}
	m := ancestorPKTableRe.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
