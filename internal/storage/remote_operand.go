package storage

import (
	"fmt"
	"strings"
)

// ValidateRemoteOperand refuses a remote or peer name that Dolt would parse
// as an option. Remote names reach DOLT_PUSH, DOLT_PULL, DOLT_FETCH,
// DOLT_REMOTE and the dolt CLI as positional operands, and those parse a
// leading '-' as a flag even when the name is bound as a SQL parameter, so
// `--remote=--force` would turn into a forced push (GH#7876).
//
// Only the leading dash is checked: empty-name and default-remote handling
// stay with each caller, and names such as "1st" remain valid.
func ValidateRemoteOperand(name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid remote name %q: must not start with '-'", name)
	}
	return nil
}
