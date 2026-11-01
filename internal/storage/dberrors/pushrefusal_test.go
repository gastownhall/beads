package dberrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

const remoteDanglingRefusal = "unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"

// TestIsRemoteDanglingReference_EveryRendering pins the match against each
// form in which bd can surface Dolt's remote-side refusal (GH#3358): the SQL
// route's Error 1105 wrapped by the push, the CLI route's CombinedOutput, the
// embedded engine's bare error, and the http tier's flattened text.
func TestIsRemoteDanglingReference_EveryRendering(t *testing.T) {
	sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: remoteDanglingRefusal}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"sql route", fmt.Errorf("failed to push to origin/main: %w", sqlErr)},
		{"cli route", fmt.Errorf("dolt push failed: %s: %w", "Uploading...\n"+"error: failed to push some refs to 'https://doltremoteapi.dolthub.com/o/r'\n"+remoteDanglingRefusal, errors.New("exit status 1"))},
		{"embedded", errors.New(remoteDanglingRefusal)},
		{"http flattened", errors.New(fmt.Errorf("failed to push to origin/main: %w", sqlErr).Error())},
		{"other case", errors.New("UNKNOWN PUSH ERROR; FOUND DANGLING REFERENCES TO HASHSET {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}")},
	} {
		if !IsRemoteDanglingReference(tc.err) {
			t.Errorf("%s: IsRemoteDanglingReference(%q) = false, want true", tc.name, tc.err)
		}
	}
}

// TestIsRemoteDanglingReference_NotOtherFailures keeps the remote guidance
// off failures it would mislead: bd's own pre-push fsck (the push never
// reached the remote, even when fsck's quoted output repeats Dolt's phrase),
// Dolt's local NBS "dangling ref" form, and the push refusals that already
// have guidance of their own.
func TestIsRemoteDanglingReference_NotOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"pre-push fsck", fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s", errors.New("dangling chunk reference"), "chunk abc missing")},
		{"pre-push fsck quoting dolt", fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s", errors.New("dangling chunk reference"), "Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}")},
		{"local nbs", errors.New("dangling ref: found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}")},
		{"diverged history", errors.New("failed to push to origin/main: no common ancestor")},
		{"pk mismatch", errors.New("error: cannot merge because table dependencies has different primary keys in its common ancestor")},
		{"remote not found", errors.New("remote 'backup' not found")},
	} {
		if IsRemoteDanglingReference(tc.err) {
			t.Errorf("%s: IsRemoteDanglingReference(%v) = true, want false", tc.name, tc.err)
		}
	}
}
