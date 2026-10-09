package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/pre-push-suite.sh is the push-time suite .githooks/pre-push runs
// when a pushed branch changes Go or Bazel inputs: bazel.yml's test lane
// (`bazel test //... --config=ci`), remotely when some rc names an executor,
// else through the read-only fork cache. Plain go test runs only when bazel
// cannot (or BD_PREPUSH_SUITE=go asks for it), and then under a banner, so
// a green push is never read as CI parity.

type prePushSuiteRun struct {
	out     string
	bazel   string
	make    string
	exitErr error
}

// runPrePushSuite runs the suite with fake bazel and make that log their
// arguments. announce is what the fake `bazel info --announce_rc` prints to
// stderr; bazel "missing" points BAZEL at a path that does not exist.
func runPrePushSuite(t *testing.T, bazel, announce string, env ...string) prePushSuiteRun {
	t.Helper()
	bash := requireHostTool(t, "bash")
	dir := t.TempDir()
	bazelLog := filepath.Join(dir, "bazel.log")
	makeLog := filepath.Join(dir, "make.log")
	writeExecutable := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fakeBazel := writeExecutable("bazel", `if [ "$1" = info ]; then printf '%s\n' "$FAKE_ANNOUNCE" >&2; echo release 9; exit 0; fi
printf '%s\n' "$*" >>"`+bazelLog+`"
`)
	fakeMake := writeExecutable("make", `printf '%s\n' "$*" >>"`+makeLog+`"
`)
	if bazel == "missing" {
		fakeBazel = filepath.Join(dir, "no-such-bazel")
	}

	cmd := exec.Command(bash, filepath.Join(sourceRepoRoot(t), "scripts", "pre-push-suite.sh"))
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); name == "BD_PREPUSH_SUITE" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "BAZEL="+fakeBazel, "MAKE="+fakeMake, "FAKE_ANNOUNCE="+announce)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	read := func(path string) string {
		data, _ := os.ReadFile(path)
		return strings.TrimSpace(string(data))
	}
	return prePushSuiteRun{out: string(out), bazel: read(bazelLog), make: read(makeLog), exitErr: err}
}

const announceWithExecutor = `INFO: Reading rc options for 'info' from /home/u/.bazelrc:
  Inherited 'build' options: --remote_executor=grpcs://rbe.example:443 --jobs=64`

const announceWithoutExecutor = `INFO: Reading rc options for 'info' from /w/.bazelrc:
  Inherited 'common' options: --enable_bzlmod`

func TestPrePushSuiteRunsBazelTestLane(t *testing.T) {
	for _, tc := range []struct {
		name, announce string
		env            []string
		want           string
	}{
		{"auto with an executor", announceWithExecutor, nil, "test //... --config=ci --config=remote-exec"},
		{"auto without an executor", announceWithoutExecutor, nil, "test //... --config=ci --config=fork-cache"},
		{"explicit cache", announceWithExecutor, []string{"BD_PREPUSH_SUITE=cache"}, "test //... --config=ci --config=fork-cache"},
		{"explicit rbe", announceWithExecutor, []string{"BD_PREPUSH_SUITE=rbe"}, "test //... --config=ci --config=remote-exec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runPrePushSuite(t, "present", tc.announce, tc.env...)
			if run.exitErr != nil {
				t.Fatalf("suite failed: %v\n%s", run.exitErr, run.out)
			}
			if run.bazel != tc.want {
				t.Errorf("bazel ran %q, want %q\n%s", run.bazel, tc.want, run.out)
			}
			if run.make != "" {
				t.Errorf("make ran %q; the bazel suite needs no go fallback\n%s", run.make, run.out)
			}
		})
	}
}

func TestPrePushSuiteRbeWithoutExecutorFails(t *testing.T) {
	run := runPrePushSuite(t, "present", announceWithoutExecutor, "BD_PREPUSH_SUITE=rbe")
	if run.exitErr == nil || run.bazel != "" {
		t.Fatalf("BD_PREPUSH_SUITE=rbe without an executor: err %v, bazel ran %q\n%s", run.exitErr, run.bazel, run.out)
	}
}

func TestPrePushSuiteGoFallbackIsAnnounced(t *testing.T) {
	for _, tc := range []struct {
		name, bazel, why string
		env              []string
	}{
		{"bazel missing", "missing", "bazel is not installed", nil},
		{"explicit go", "present", "BD_PREPUSH_SUITE=go", []string{"BD_PREPUSH_SUITE=go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runPrePushSuite(t, tc.bazel, announceWithExecutor, tc.env...)
			if run.exitErr != nil {
				t.Fatalf("suite failed: %v\n%s", run.exitErr, run.out)
			}
			if run.make != "test-go" || run.bazel != "" {
				t.Errorf("ran make %q, bazel %q; want make test-go only\n%s", run.make, run.bazel, run.out)
			}
			for _, want := range []string{"NOT the bazel suite CI gates on", "why: " + tc.why, "bazel test //... --config=ci"} {
				if !strings.Contains(run.out, want) {
					t.Errorf("go fallback banner lacks %q:\n%s", want, run.out)
				}
			}
		})
	}
}

func TestPrePushSuiteRejectsUnknownMode(t *testing.T) {
	run := runPrePushSuite(t, "present", announceWithExecutor, "BD_PREPUSH_SUITE=sometimes")
	if run.exitErr == nil || run.bazel != "" || run.make != "" {
		t.Fatalf("unknown mode: err %v, bazel %q, make %q\n%s", run.exitErr, run.bazel, run.make, run.out)
	}
}

// The hook runs the suite only for branch pushes whose commits change Go or
// Bazel inputs; a tag push, a deletion, or a docs-only push skips it.
func TestPrePushHookRunsSuiteOnlyForGoChanges(t *testing.T) {
	body := readPolicyFile(t, sourceRepoRoot(t), ".githooks/pre-push")
	for _, want := range []string{
		`"$repo_root/scripts/pre-push-suite.sh" </dev/null`,
		"refs/tags/*) continue ;;",
		"'*.go'",
		"'*.bazel'",
	} {
		if !strings.Contains(body, want) {
			t.Errorf(".githooks/pre-push lacks %q", want)
		}
	}
	// Index the call line: the header comment names pre-push-suite.sh too, and
	// sits before the managed section wherever the call block is.
	managed := strings.Index(body, "# --- BEGIN BEADS INTEGRATION")
	suite := strings.Index(body, `"$repo_root/scripts/pre-push-suite.sh" </dev/null`)
	if managed < 0 || suite < 0 || suite > managed {
		t.Errorf(".githooks/pre-push must run the suite before the managed beads section")
	}
}
