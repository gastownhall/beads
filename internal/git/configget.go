package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ClassifyConfigGet interprets the result of a `git config --get <key>` run
// through exec.Cmd.Output, so every caller tells absence from failure by the
// same rule:
//
//   - success: the trimmed value with present=true, including an explicitly
//     empty value, which callers keep or collapse by their own policy;
//   - exit 1 with empty stderr: the key is absent (present=false, nil error);
//   - anything else: a read failure. Git also exits 1 for an invalid key, but
//     with a diagnostic, and a malformed or unreadable config file exits 128.
//     The returned error wraps err and carries the stderr diagnostic.
//
// It classifies process results only: it does not run git, select a
// repository, or decide whether a failure is fatal to the caller.
func ClassifyConfigGet(out []byte, err error) (value string, present bool, readErr error) {
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		diagnostic := bytes.TrimSpace(exitErr.Stderr)
		if exitErr.ExitCode() == 1 && len(diagnostic) == 0 {
			return "", false, nil
		}
		if len(diagnostic) > 0 {
			return "", false, fmt.Errorf("%w: %s", err, diagnostic)
		}
	}
	return "", false, err
}
