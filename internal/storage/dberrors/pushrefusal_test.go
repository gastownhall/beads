package dberrors

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsRemoteDanglingReference_MatchForms pins every form bd can surface
// Dolt's remote-side dangling-reference push refusal in (GH#3358): the SQL
// route's Error 1105 envelope wrapped by the store's "failed to push" prefix,
// the CLI route's CombinedOutput rendering with exit status, the embedded
// engine's bare form, the http tier's flattened errors.New text, and a
// different letter case in Dolt's message.
func TestIsRemoteDanglingReference_MatchForms(t *testing.T) {
	sqlRoute := fmt.Errorf(
		"failed to push to origin/main: %w",
		errors.New("Error 1105 (HY000): unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"),
	)
	cliRoute := errors.New(
		"dolt push failed: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}: exit status 1",
	)
	embedded := errors.New(
		"unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}",
	)
	flattened := errors.New(
		"Error 1105 (HY000): unknown push error; Found dangling references to HashSet {nrcp8b3mbvr4neiklr4tuitl7m0677bq}",
	)
	otherCase := errors.New(
		"Error 1105 (HY000): unknown push error; found DANGLING references TO HashSet {8cqtu3q5dlfbrkve90vbb1fub03k6ife}",
	)
	for _, err := range []error{sqlRoute, cliRoute, embedded, flattened, otherCase} {
		if !IsRemoteDanglingReference(err) {
			t.Errorf("IsRemoteDanglingReference(%q) = false, want true", err)
		}
	}
}
