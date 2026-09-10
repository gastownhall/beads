package workapi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The database-free half of issueops.Deleter, pinned in milliseconds. Every
// backend runs these functions, so a disagreement about what a malformed
// request means is caught here rather than three times over in conformance.

func TestValidateDeleteRequest(t *testing.T) {
	for _, test := range []struct {
		name    string
		request issueops.DeleteRequest
		wantErr bool
	}{
		{"one id", issueops.DeleteRequest{IDs: []string{"bd-1"}}, false},
		{"several ids", issueops.DeleteRequest{IDs: []string{"bd-1", "bd-2"}}, false},
		{"untrimmed id is accepted and normalized later", issueops.DeleteRequest{IDs: []string{" bd-1 "}}, false},
		{"nil ids", issueops.DeleteRequest{}, true},
		{"empty ids", issueops.DeleteRequest{IDs: []string{}}, true},
		{"blank id", issueops.DeleteRequest{IDs: []string{""}}, true},
		{"whitespace id", issueops.DeleteRequest{IDs: []string{"\t "}}, true},
		{"blank beside a real one", issueops.DeleteRequest{IDs: []string{"bd-1", "  "}}, true},
		{"expected version on one id", issueops.DeleteRequest{IDs: []string{"bd-1"}, ExpectedVersion: deleteVersion(7)}, false},
		// DeleteRequest.ExpectedVersion counts DISTINCT ids, so the two rows
		// below are the whole rule: a repeated mention still names one row.
		{"expected version on a repeated id", issueops.DeleteRequest{IDs: []string{"bd-1", " bd-1 "}, ExpectedVersion: deleteVersion(7)}, false},
		{"expected version on several ids", issueops.DeleteRequest{IDs: []string{"bd-1", "bd-2"}, ExpectedVersion: deleteVersion(7)}, true},
		// Version 0 is a real expectation — a row nothing has written yet — so
		// the pointer and not the value is what disables the check.
		{"expected version zero on one id", issueops.DeleteRequest{IDs: []string{"bd-1"}, ExpectedVersion: deleteVersion(0)}, false},
		{"expected version zero on several ids", issueops.DeleteRequest{IDs: []string{"bd-1", "bd-2"}, ExpectedVersion: deleteVersion(0)}, true},
		// Without a version the multi-id request is the ordinary one, which is
		// what keeps the new rule from reading as a general arity limit.
		{"several ids without a version", issueops.DeleteRequest{IDs: []string{"bd-1", "bd-2"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeleteRequest(test.request)
			if test.wantErr {
				if !errors.Is(err, issueops.ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

// TestNormalizeDeleteIDs pins FIRST-MENTION order rather than sorted order. It
// is the order both front doors echo back — in "issues not found" and in the
// `--force` hint they print — so a caller re-reading its own `--from-file` list
// against that output should not have to re-sort it.
func TestNormalizeDeleteIDs(t *testing.T) {
	for _, test := range []struct {
		name string
		in   []string
		want []string
	}{
		{"unchanged", []string{"bd-1", "bd-2"}, []string{"bd-1", "bd-2"}},
		{"duplicates collapse to the first mention", []string{"bd-2", "bd-1", "bd-2"}, []string{"bd-2", "bd-1"}},
		{"whitespace is trimmed before comparing", []string{" bd-1", "bd-1 "}, []string{"bd-1"}},
		{"order is not sorted", []string{"bd-z", "bd-a"}, []string{"bd-z", "bd-a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeDeleteIDs(test.in); !reflect.DeepEqual(got, test.want) {
				t.Errorf("NormalizeDeleteIDs(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

// TestNormalizeDeleteIDsDoesNotMutateTheCaller is the pin behind
// issueops.DeleteRequest's "IDs is read, never written through": normalizing in
// place is the most natural way to write this function and would hand the
// caller back a shorter, reordered version of its own slice.
func TestNormalizeDeleteIDsDoesNotMutateTheCaller(t *testing.T) {
	in := []string{" bd-2 ", "bd-1", "bd-2"}
	snapshot := append([]string(nil), in...)

	NormalizeDeleteIDs(in)

	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("caller's slice changed: got %v, want %v", in, snapshot)
	}
}

// TestSortedDeleteIDs pins the ascending order DeleteResult.Orphaned and
// DependentsOutsideRequestError.Dependents promise. Both are collected out of
// maps, so without this they would publish Go's map order and a caller
// diffing two runs would see spurious changes.
func TestSortedDeleteIDs(t *testing.T) {
	if got := SortedDeleteIDs(nil); got != nil {
		t.Errorf("SortedDeleteIDs(nil) = %v, want nil", got)
	}
	if got := SortedDeleteIDs(map[string]bool{}); got != nil {
		t.Errorf("SortedDeleteIDs(empty) = %v, want nil", got)
	}
	got := SortedDeleteIDs(map[string]bool{"bd-c": true, "bd-a": true, "bd-b": true})
	if want := []string{"bd-a", "bd-b", "bd-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SortedDeleteIDs() = %v, want %v", got, want)
	}
}

// deleteVersion is the one-line address-of a table row cannot spell inline.
func deleteVersion(v int64) *int64 { return &v }
