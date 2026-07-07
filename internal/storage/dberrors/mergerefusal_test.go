package dberrors

import (
	"errors"
	"testing"
)

// The real-world message shape is pinned against a live Dolt merge by
// TestCrossUpgradeBoundaryMerge (internal/storage/dolt); this covers the
// matcher/extractor consistency contract: any casing IsAncestorPKMismatch
// accepts must also yield the table name (bd-578h9.17).
func TestAncestorPKMismatch_CasingConsistency(t *testing.T) {
	for _, msg := range []string{
		"cannot merge because table dependencies has different primary keys",
		"Cannot merge because table dependencies has different primary keys in its common ancestor",
		"CANNOT MERGE BECAUSE TABLE dependencies HAS DIFFERENT PRIMARY KEYS",
	} {
		err := errors.New(msg)
		if !IsAncestorPKMismatch(err) {
			t.Errorf("IsAncestorPKMismatch(%q) = false, want true", msg)
		}
		if got := AncestorPKMismatchTable(err); got != "dependencies" {
			t.Errorf("AncestorPKMismatchTable(%q) = %q, want %q", msg, got, "dependencies")
		}
	}
}

func TestAncestorPKMismatch_NonMatches(t *testing.T) {
	if IsAncestorPKMismatch(nil) {
		t.Error("IsAncestorPKMismatch(nil) = true, want false")
	}
	err := errors.New("merge conflict in table dependencies")
	if IsAncestorPKMismatch(err) {
		t.Errorf("IsAncestorPKMismatch(%q) = true, want false", err)
	}
	if got := AncestorPKMismatchTable(err); got != "" {
		t.Errorf("AncestorPKMismatchTable(%q) = %q, want empty", err, got)
	}
}
