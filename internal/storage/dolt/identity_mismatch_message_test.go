package dolt

import (
	"strings"
	"testing"
)

// TestProjectIdentityMismatchError_InitAdvice pins GH#5558: when the identity
// check fires inside bd init (a CreateIfMissing open against an existing
// database), the refusal must not tell the user not to run the command they are
// running, and must name remedies that work from there. The ordinary open keeps
// its original advice.
func TestProjectIdentityMismatchError_InitAdvice(t *testing.T) {
	const localID, dbID, database = "project-AAAA", "project-BBBB", "shared_db"

	t.Run("create-if-missing", func(t *testing.T) {
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
			"bd init --database <name>",
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
