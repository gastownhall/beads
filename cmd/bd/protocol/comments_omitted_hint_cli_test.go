package protocol

// CLI guard for the stderr hint on `bd show --json` when comment bodies are
// withheld. The unit tests in cmd/bd pin the helper; these pin the COMMAND:
// the hint lands on stderr only, stdout stays byte-identical (parseable JSON
// with nothing ahead of it), --quiet suppresses the hint while leaving
// stdout untouched, hydrated and zero-comment issues stay silent, a multi-id
// run emits one line per withheld issue, and text mode emits no hint. The
// package's run helpers use CombinedOutput, and the stream split is exactly
// what is under test here, so these cases capture stdout and stderr apart.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func runSplit(t *testing.T, w *workspace, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// assertStdoutIsBareJSON pins the promise the hint must never break: a
// consumer reading stdout alone gets the same JSON it got before the hint
// existed — valid, and starting AT the JSON, with no hint line ahead of it.
func assertStdoutIsBareJSON(t *testing.T, stdout, context string) {
	t.Helper()
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		t.Errorf("%s: stdout does not start at the JSON — hint leaked to stdout: %q", context, stdout)
	}
	if !json.Valid([]byte(trimmed)) {
		t.Errorf("%s: stdout is not valid JSON: %q", context, stdout)
	}
}

func TestCommentsOmittedHint_CLI(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	withComments := w.create("--title", "Hint target")
	for _, note := range []string{"First note", "Second note"} {
		w.run("comments", "add", withComments, note)
	}
	oneComment := w.create("--title", "Single comment")
	w.run("comments", "add", oneComment, "Alone")
	noComments := w.create("--title", "No comments")

	t.Run("withheld_writes_hint_to_stderr_only", func(t *testing.T) {
		stdout, stderr := runSplit(t, w, "show", withComments, "--json")
		want := "Hint: " + withComments + " has 2 comments not shown; add --include-comments"
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing hint line %q, got: %q", want, stderr)
		}
		assertStdoutIsBareJSON(t, stdout, "default --json")
		if strings.Contains(stdout, "not shown") {
			t.Errorf("hint leaked to stdout: %q", stdout)
		}
	})

	t.Run("quiet_suppresses_hint_and_not_stdout", func(t *testing.T) {
		plainOut, plainErr := runSplit(t, w, "show", withComments, "--json")
		if !strings.Contains(plainErr, "not shown") {
			t.Fatalf("baseline run must carry the hint for this test to mean anything, got: %q", plainErr)
		}
		quietOut, quietErr := runSplit(t, w, "show", withComments, "--json", "--quiet")
		if strings.Contains(quietErr, "not shown") {
			t.Errorf("--quiet must suppress the hint (documented: errors only), got stderr: %q", quietErr)
		}
		if quietOut != plainOut {
			t.Errorf("--quiet changed stdout:\n  plain: %q\n  quiet: %q", plainOut, quietOut)
		}
	})

	t.Run("hydrated_and_zero_comment_stay_silent", func(t *testing.T) {
		stdout, stderr := runSplit(t, w, "show", withComments, "--json", "--include-comments")
		if strings.Contains(stderr, "not shown") {
			t.Errorf("--include-comments leaves nothing withheld, stderr must stay silent: %q", stderr)
		}
		assertStdoutIsBareJSON(t, stdout, "--include-comments")

		stdout, stderr = runSplit(t, w, "show", noComments, "--json")
		if strings.Contains(stderr, "not shown") {
			t.Errorf("zero-comment issue must not be hinted, stderr: %q", stderr)
		}
		assertStdoutIsBareJSON(t, stdout, "zero-comment --json")
	})

	t.Run("multi_id_one_line_per_withheld_issue", func(t *testing.T) {
		stdout, stderr := runSplit(t, w, "show", withComments, oneComment, "--json")
		if !strings.Contains(stderr, withComments+" has 2 comments not shown") {
			t.Errorf("missing hint for %s, stderr: %q", withComments, stderr)
		}
		if !strings.Contains(stderr, oneComment+" has 1 comment not shown") {
			t.Errorf("missing singular hint for %s, stderr: %q", oneComment, stderr)
		}
		assertStdoutIsBareJSON(t, stdout, "multi-id --json")
	})

	t.Run("text_mode_has_no_hint", func(t *testing.T) {
		_, stderr := runSplit(t, w, "show", withComments)
		if strings.Contains(stderr, "not shown") {
			t.Errorf("text mode shows comments inline and must not hint, stderr: %q", stderr)
		}
	})
}
