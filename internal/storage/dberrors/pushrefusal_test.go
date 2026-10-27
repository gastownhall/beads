package dberrors

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsRemoteDanglingReference_NonMatchForms pins what must NOT be
// classified as a remote dangling-reference push refusal: nil; bd's own local
// pre-push fsck abort (including one whose quoted fsck output contains the
// remote-side phrase — the local corridor check never reached the remote);
// Dolt's local NBS store form; a diverged-history error; a
// different-primary-keys merge refusal; and a remote-not-found error.
func TestIsRemoteDanglingReference_NonMatchForms(t *testing.T) {
	localFSCK := errors.New(
		"dangling chunk reference: aborting push to prevent propagating corrupt chunks: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {r564v1teutmhp289qspj9ba1lhrvit00}",
	)
	nbsLocalForm := errors.New(
		"dangling ref: found dangling references to HashSet {r564v1teutmhp289qspj9ba1lhrvit00}",
	)
	diverged := errors.New("cannot pull with merge: no common ancestor between refs/heads/main and origin/main")
	pkMismatch := errors.New("cannot merge because table dependencies has different primary keys")
	remoteNotFound := errors.New("remote 'backup' not found")
	for _, err := range []error{
		nil,
		localFSCK,
		nbsLocalForm,
		diverged,
		pkMismatch,
		remoteNotFound,
	} {
		if IsRemoteDanglingReference(err) {
			t.Errorf("IsRemoteDanglingReference(%v) = true, want false", err)
		}
	}
}

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
