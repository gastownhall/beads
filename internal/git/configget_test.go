package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyConfigGet drives real `git config --get` results through the
// shared rule: a value (even an empty one) is present, a silent exit 1 is
// absence, and a diagnostic-bearing failure is a read error that keeps both
// the exit status and git's message.
func TestClassifyConfigGet(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(args ...string) ([]byte, error) {
		return exec.Command("git", append([]string{"config"}, args...)...).Output()
	}

	value, present, err := ClassifyConfigGet(run("--file", write("set", "[beads]\n\trole = contributor\n"), "--get", "beads.role"))
	if err != nil || !present || value != "contributor" {
		t.Errorf("set key = (%q, %v, %v), want (contributor, true, nil)", value, present, err)
	}

	value, present, err = ClassifyConfigGet(run("--file", write("empty-value", "[beads]\n\trole =\n"), "--get", "beads.role"))
	if err != nil || !present || value != "" {
		t.Errorf("empty value = (%q, %v, %v), want (\"\", true, nil)", value, present, err)
	}

	value, present, err = ClassifyConfigGet(run("--file", write("absent", ""), "--get", "beads.role"))
	if err != nil || present || value != "" {
		t.Errorf("absent key = (%q, %v, %v), want (\"\", false, nil)", value, present, err)
	}

	_, present, err = ClassifyConfigGet(run("--file", write("malformed", "[beads\n"), "--get", "beads.role"))
	var exitErr *exec.ExitError
	if err == nil || present || !errors.As(err, &exitErr) || !strings.Contains(err.Error(), "bad config") {
		t.Errorf("malformed config = (present %v, %v), want a wrapped exit error with the diagnostic", present, err)
	}

	// Git also exits 1 for an invalid key; the diagnostic is what makes it a failure.
	_, present, err = ClassifyConfigGet(run("--file", write("invalid-key", ""), "--get", "nosection"))
	if err == nil || present || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("invalid key = (present %v, %v), want an exit-1 read error", present, err)
	}

	notFound := &exec.Error{Name: "git", Err: exec.ErrNotFound}
	if _, present, err = ClassifyConfigGet(nil, notFound); !errors.Is(err, exec.ErrNotFound) || present {
		t.Errorf("missing executable = (present %v, %v), want the exec error", present, err)
	}
}
