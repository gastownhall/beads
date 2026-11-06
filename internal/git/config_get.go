package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ClassifyConfigGet classifies the raw result of a `git config --get <key>`
// run — the (out, err) pair exec.Cmd.Output produces — into the value it
// returned, whether the key was present, and whether the read itself failed:
//
//	success                   → (trimmed value, true, nil), an explicitly empty value included
//	exit 1 with empty stderr  → ("", false, nil): the key is absent
//	any other failure         → ("", false, error) retaining the underlying error and its diagnostic
//
// Git also uses exit 1 for diagnosable failures such as an invalid key, with
// the reason on stderr; only the silent exit 1 means the key is absent.
//
// The helper classifies process results only. It does not run git, select a
// repository, choose an environment, or decide whether a caller must fail:
// the same (out, err) is a fatal error at one boundary and a warning at the
// next, and callers keep their own policy on top of the result — the domain
// GetConfig adapter collapses an empty successful value into absence, while
// doctor's role validation treats it as invalid.
func ClassifyConfigGet(out []byte, err error) (value string, present bool, readErr error) {
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 && len(bytes.TrimSpace(exitErr.Stderr)) == 0 {
				return "", false, nil
			}
			if diag := bytes.TrimSpace(exitErr.Stderr); len(diag) > 0 {
				return "", false, fmt.Errorf("%w: %s", err, diag)
			}
			return "", false, err
		}
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}
