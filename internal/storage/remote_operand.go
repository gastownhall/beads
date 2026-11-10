package storage

import (
	"fmt"
	"strings"
)

// ValidateRemoteOperand rejects a remote or peer name that could be parsed as
// an option by the Dolt command surfaces a name eventually reaches: the
// CALL DOLT_PUSH/DOLT_PULL/DOLT_FETCH procedures (where a leading-dash bound
// parameter becomes a flag for the procedure) and the dolt CLI subprocess
// argv (GH#7876). It is driver-neutral: command parsing, the server store,
// the embedded store, the shared remote SQL API and issueops all call it
// before using a name, so every route into those surfaces is covered at its
// earliest owning boundary.
//
// The empty name is valid here: default-remote resolution and the existing
// empty/duplicate-name diagnostics stay with their callers. Valid names such
// as "1st" stay legal.
func ValidateRemoteOperand(name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid remote name %q: must not start with '-'", name)
	}
	return nil
}
