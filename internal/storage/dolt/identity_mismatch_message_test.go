package dolt

import (
	"errors"
	"strings"
	"testing"
)

// TestProjectIdentityMismatchError_InitAdvice pins GH#5558: when the identity
// check fires inside bd init (Config.OpenedByInit), the refusal must not tell
// the user not to run the command they are running, and must name remedies that
// work from there. Every other open keeps its original advice — including a
// CreateIfMissing open, which is not by itself bd init.
func TestProjectIdentityMismatchError_InitAdvice(t *testing.T) {
	const localID, dbID, database = "project-AAAA", "project-BBBB", "shared_db"

	t.Run("opened-by-init", func(t *testing.T) {
		msg := projectIdentityMismatchError(localID, dbID, database, true).Error()
		if strings.Contains(msg, "Do NOT run 'bd init'") {
			t.Errorf("init-time refusal tells the user not to run bd init:\n%s", msg)
		}
		for _, want := range []string{
			"PROJECT IDENTITY MISMATCH",
			localID,
			dbID,
			`"` + database + `"`,
			"bd dolt status",
			"bd init --database <other-name>",
			"bd init --server-host <host> --server-port <port>",
			"bd doctor --fix",
			"bd bootstrap",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("init-time refusal missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("normal open", func(t *testing.T) {
		msg := projectIdentityMismatchError(localID, dbID, database, false).Error()
		for _, want := range []string{
			"PROJECT IDENTITY MISMATCH — refusing to connect",
			localID,
			dbID,
			"bd dolt status",
			"Do NOT run 'bd init' — your data likely exists, just on a different server.",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("normal-open refusal missing %q:\n%s", want, msg)
			}
		}
	})
}

// TestProjectIdentityUnverifiableError_Advice pins the refusal for an existing
// database whose project ID cannot be read (verifyProjectIdentity's read-error
// branch): it must keep the read error in the chain and name only remedies that
// get past the check. 'bd doctor' and 'bd bootstrap' reach the same check, and
// plain 'bd init --database' stops at init's "already initialized" guard, so
// none of them may be offered as a fix.
func TestProjectIdentityUnverifiableError_Advice(t *testing.T) {
	const localID, database = "project-AAAA", "clone_db"
	readErr := errors.New("get metadata _project_id: Error 1146 (HY000): table not found: metadata")

	err := projectIdentityUnverifiableError(localID, database, readErr)
	if !errors.Is(err, readErr) {
		t.Errorf("refusal does not wrap the read error: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"cannot verify project identity",
		`"` + database + `"`,
		localID,
		readErr.Error(),
		"bd dolt status",
		"bd init --reinit-local --database <other-name>",
		"DROP DATABASE `" + database + "`",
		"bd init --recreate-missing",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q:\n%s", want, msg)
		}
	}
	for _, unwanted := range []string{
		"run 'bd doctor --fix' or 'bd bootstrap'",
		"bd init --database <other-name>",
	} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("refusal offers %q, which this check also refuses:\n%s", unwanted, msg)
		}
	}
}
