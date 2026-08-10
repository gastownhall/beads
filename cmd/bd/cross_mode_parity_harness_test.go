//go:build cgo

package main

// Cross-mode parity harness.
//
// A parity case runs the SAME command sequence against two equivalent, freshly
// initialized workspaces — one classic (embedded Dolt) and one proxied-server —
// records a plain, comparable outcome value per mode, and asserts the two are
// identical. It is the oracle a proxied port owes: not "the proxied path works"
// (a single-mode integration test says that) but "the proxied path is
// indistinguishable from the classic one", which is the property a
// shared-server constellation actually depends on.
//
// Identity is asserted on OUTCOMES, never on ids: the two workspaces mint their
// own ids, so a case names rows by role and compares exit codes, row state, and
// the machine-greppable substrings of the output.
//
// The cases are named TestProxiedServer* so the proxied CI lane's discovery
// (.github/scripts/proxied-test-shard.sh) picks them up, and gate only on
// BEADS_TEST_PROXIED_SERVER: the classic half needs no gate of its own, since
// the same binary runs it with the proxied env removed.

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// crossModeEnv is one initialized workspace plus the environment that selects
// its backend. Everything a parity case does goes through run(), so the only
// difference between the two modes is the env and the directory.
type crossModeEnv struct {
	mode string
	bd   string
	dir  string
	env  []string
}

// newCrossModeEnvs initializes one classic and one proxied workspace with
// equivalent configuration. The prefixes differ only so the two workspaces are
// telling apart in failure output; nothing compares ids across modes.
func newCrossModeEnvs(t *testing.T, bd, classicPrefix, proxiedPrefix string) []crossModeEnv {
	t.Helper()
	classicDir, _, _ := bdInit(t, bd, "--prefix", classicPrefix)
	proxied := newSharedProxiedProject(t, bd, proxiedPrefix)
	return []crossModeEnv{
		{mode: "classic", bd: bd, dir: classicDir, env: bdEnv(classicDir)},
		{mode: "proxied", bd: bd, dir: proxied.dir, env: bdProxiedEnv(proxied.dir)},
	}
}

// run executes bd in this workspace and returns stdout, stderr and the exit
// code. A command that could not be started at all is fatal — that is a broken
// harness, not a parity verdict.
func (e crossModeEnv) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return e.runWith(t, nil, args...)
}

// runStdin is run() with stdin content, for the commands that read a script.
// It always attaches a reader, including for the empty script — "no stdin at
// all" and "an empty stdin" are different inputs to `bd batch`, and only the
// second is the no-op case.
func (e crossModeEnv) runStdin(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	return e.runWith(t, strings.NewReader(stdin), args...)
}

func (e crossModeEnv) runWith(t *testing.T, stdin io.Reader, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(e.bd, args...)
	cmd.Dir = e.dir
	cmd.Env = e.env
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("[%s] bd %s could not run: %v\nstdout:\n%s\nstderr:\n%s",
				e.mode, strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// mustRun is run() for a step whose failure would make the rest of the case
// meaningless (seeding, mostly).
func (e crossModeEnv) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, code := e.run(t, args...)
	if code != 0 {
		t.Fatalf("[%s] bd %s failed with exit %d\nstdout:\n%s\nstderr:\n%s",
			e.mode, strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// create seeds one issue and returns its id.
func (e crossModeEnv) create(t *testing.T, title string, extra ...string) string {
	t.Helper()
	args := append([]string{"create", title, "--type", "task", "--json"}, extra...)
	out := e.mustRun(t, args...)
	return parseIssueJSON(t, []byte(out)).ID
}

// show reads one issue back.
func (e crossModeEnv) show(t *testing.T, id string) *types.Issue {
	t.Helper()
	out := e.mustRun(t, "show", id, "--json")
	return parseIssueJSON(t, []byte(out))
}
