//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGeneratedHookSectionWithoutBdKeepsChainedGate covers GH#6751: when bd is
// not on PATH the generated section must not skip a chained <hook>.old gate,
// so a refusing pre-commit.old still blocks the commit.
func TestGeneratedHookSectionWithoutBdKeepsChainedGate(t *testing.T) {
	hooksDir := t.TempDir()
	emptyPath := t.TempDir() // no bd here

	hook := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"+generateHookSection("pre-commit")), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(hooksDir, "pre-commit.old")
	if err := os.WriteFile(gate, []byte("#!/bin/sh\necho gate refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", hook)
	cmd.Dir = hooksDir
	cmd.Env = []string{"PATH=" + emptyPath, "HOME=" + hooksDir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatalf("pre-commit without bd on PATH exited 0; chained pre-commit.old gate was skipped (fail-open). output:\n%s", out)
	}
	if !errors.As(err, &exitErr) {
		t.Fatalf("running hook: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "gate refused") {
		t.Errorf("hook output missing the chained gate's own diagnostic, so it may not have run:\n%s", out)
	}
}

// hookChainCase is one classification-edge scenario for the shell-side
// <hook>.old classifier the generated section runs when bd is unavailable.
type hookChainCase struct {
	name string
	// oldBody and oldMode describe the sidecar; a directory is created
	// instead when oldBody is exactly "".
	oldBody string
	oldMode os.FileMode // 0 means the default 0o755
	// hookDirName names the hooks directory inside the test's temp dir, so a
	// case can exercise paths with spaces.
	hookDirName string
	// hookArg0 invokes the hook through this $0 spelling instead of its
	// absolute path (no-slash and symlink cases).
	hookArg0 func(hooksDir string) (arg0 string, dir string)
	env      []string // extra environment entries
	prefix   string   // lines prepended to the hook body (e.g. set -eu)
	// wantRun: the sidecar must execute (its marker shows up). When false
	// the hook must succeed without running it, unless wantExit is set.
	wantRun bool
	// wantExit, when nonzero, is the exact expected hook exit code (the
	// derived default is 7 when wantRun and 0 when not).
	wantExit int
	skipFn   func(t *testing.T)
}

// TestGeneratedHookChainClassifierEdges pins the section's shell-side
// classification of <hook>.old against the same rules getHookVersion applies
// on the Go side (GH#6751): bd-owned markers and "bd hooks run" delegations
// are skipped, everything else — including files that merely mention the
// marker words — runs and its verdict is honored.
func TestGeneratedHookChainClassifierEdges(t *testing.T) {
	const ranMarker = "SIDECHAIN_RAN"
	cases := []hookChainCase{
		{
			name:    "bd section copy is not re-chained",
			oldBody: "#!/bin/sh\n" + generateHookSection("pre-commit") + "\nexit 9\n",
		},
		{
			name:    "bd shim marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd-shim v1.2.3\nexit 9\n",
		},
		{
			name:    "legacy version marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd-hooks-version: 1.2.3\nexit 9\n",
		},
		{
			name:    "legacy inline marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd (beads)\nexit 9\n",
		},
		{
			name:    "bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n# bd hooks run pre-commit \"$@\"\nexit 9\n",
		},
		{
			name:    "tab-separated bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n# run via: bd\thooks\trun pre-commit\nexit 9\n",
		},
		{
			name:    "indented bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n  bd hooks run pre-commit \"$@\"\nexit 9\n",
		},
		{
			name:    "re-entered invocation does not run the sidecar again",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 9\n",
			env:     []string{"BD_HOOK_CHAIN_ACTIVE=pre-commit"},
		},
		{
			name:    "foreign hook merely mentioning BEADS INTEGRATION runs",
			oldBody: "#!/bin/sh\n# see BEADS INTEGRATION docs for context\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "foreign hook with dash-banner comment runs",
			oldBody: "#!/bin/sh\n# --- NOTE: not a BEADS INTEGRATION marker\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "foreign hook begin-marker-shaped line with extra words runs",
			oldBody: "#!/bin/sh\n# --- BEGIN foo BEADS INTEGRATION\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "word-boundary: xbd hooks run is foreign and runs",
			oldBody: "#!/bin/sh\n# xbd hooks run\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "word-boundary: bd hooks running is foreign and runs",
			oldBody: "#!/bin/sh\n# bd hooks running elsewhere\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "no final newline is still scanned",
			oldBody: "#!/bin/sh\n# bd hooks run pre-commit \"$@\"\necho " + ranMarker + " >&2\nexit 7",
		},
		{
			name:    "long trailing line without newline runs",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n" + strings.Repeat("x", 5000),
			wantRun: true,
		},
		{
			name:    "non-executable sidecar is skipped",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			oldMode: 0o644,
		},
		{
			name:    "sidecar directory is skipped",
			oldBody: "",
		},
		{
			name:    "set -eu caller still honors the verdict",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			prefix:  "set -eu\n",
			wantRun: true,
		},
		{
			name:        "hooks directory with a space in its path",
			oldBody:     "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			hookDirName: "hooks with space",
			wantRun:     true,
		},
		{
			// Unreadable-but-executable: the scan cannot disqualify what it
			// cannot read, so the sidecar still runs — and the interpreter
			// cannot read it either, refusing the hook instead of skipping
			// the gate.
			name:     "unreadable sidecar is still attempted",
			oldBody:  "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			oldMode:  0o100,
			wantExit: 2,
			skipFn: func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Skip("root reads files regardless of mode bits")
				}
			},
		},
		{
			// $0 is the symlink's path, so the sidecar is looked up next to
			// the invocation point, not next to the link target.
			name:    "symlinked invocation resolves the sidecar beside the link",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			// A no-slash $0 (hook found through PATH or the working
			// directory) falls back to the working directory for the sidecar
			// lookup, matching the section's documented invocation-directory
			// rule.
			name:    "no-slash $0 resolves the sidecar via the working directory",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipFn != nil {
				tc.skipFn(t)
			}
			baseDir := t.TempDir()
			emptyPath := t.TempDir() // no bd here

			hooksDir := filepath.Join(baseDir, "hooks")
			if tc.hookDirName != "" {
				hooksDir = filepath.Join(baseDir, tc.hookDirName)
			}
			if err := os.MkdirAll(hooksDir, 0o755); err != nil {
				t.Fatal(err)
			}

			sidecarDir := hooksDir
			switch tc.name {
			case "symlinked invocation resolves the sidecar beside the link":
				// The real hook lives in hooksDir; the invocation goes through
				// a symlink in a sibling directory that also holds the
				// sidecar.
				linkDir := filepath.Join(baseDir, "invocation")
				if err := os.MkdirAll(linkDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(hooksDir, "pre-commit"), filepath.Join(linkDir, "pre-commit")); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				tc.hookArg0 = func(string) (string, string) {
					return filepath.Join(linkDir, "pre-commit"), linkDir
				}
				sidecarDir = linkDir
			case "no-slash $0 resolves the sidecar via the working directory":
				tc.hookArg0 = func(dir string) (string, string) {
					return "pre-commit", dir
				}
			}

			if tc.oldBody == "" {
				if err := os.Mkdir(filepath.Join(sidecarDir, "pre-commit.old"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				mode := tc.oldMode
				if mode == 0 {
					mode = 0o755
				}
				sidecar := filepath.Join(sidecarDir, "pre-commit.old")
				if err := os.WriteFile(sidecar, []byte(tc.oldBody), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(sidecar, mode); err != nil {
					t.Fatal(err)
				}
			}

			hookPath := filepath.Join(hooksDir, "pre-commit")
			if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"+tc.prefix+generateHookSection("pre-commit")), 0o755); err != nil {
				t.Fatal(err)
			}

			env := append([]string{"PATH=" + emptyPath, "HOME=" + baseDir}, tc.env...)
			var out []byte
			var err error
			if tc.hookArg0 != nil {
				arg0, dir := tc.hookArg0(hooksDir)
				cmd := exec.Command("/bin/sh", arg0)
				cmd.Dir = dir
				cmd.Env = env
				out, err = cmd.CombinedOutput()
			} else {
				cmd := exec.Command("/bin/sh", hookPath)
				cmd.Dir = hooksDir
				cmd.Env = env
				out, err = cmd.CombinedOutput()
			}

			ran := strings.Contains(string(out), ranMarker)
			if ran != tc.wantRun {
				t.Fatalf("sidecar ran = %v, want %v\noutput:\n%s\nerr: %v", ran, tc.wantRun, out, err)
			}
			wantExit := tc.wantExit
			if wantExit == 0 && tc.wantRun {
				wantExit = 7
			}
			if wantExit != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != wantExit {
					t.Fatalf("hook exit = %v, want %d\noutput:\n%s", err, wantExit, out)
				}
			} else if err != nil {
				t.Fatalf("hook should succeed when the sidecar is skipped, got: %v\n%s", err, out)
			}
		})
	}
}

// hookChainMinimalPathEnv returns a git-friendly environment whose PATH
// deliberately excludes bd, plus one extra entry appended verbatim.
func hookChainMinimalPathEnv(t *testing.T, home string, extra ...string) []string {
	t.Helper()
	for _, dir := range []string{"/usr/bin", "/bin"} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Skipf("%s is required for the minimal-PATH repro but is unavailable", dir)
		}
	}
	env := hookProcessGitEnv(home)
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=/usr/bin:/bin"
		}
	}
	return append(env, extra...)
}

// TestGeneratedHookPrePushForwardsArgsAndStdinWithoutBD covers GH#6751's
// pre-push case: without bd on PATH the section runs the foreign sidecar
// itself, so the sidecar must receive the push arguments git passed to the
// hook and the ref lines git fed on stdin — and its verdict must refuse the
// push.
func TestGeneratedHookPrePushForwardsArgsAndStdinWithoutBD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH=/usr/bin:/bin is POSIX-specific")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}

	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	remoteDir := filepath.Join(base, "remote.git")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(base, "record")
	env := hookChainMinimalPathEnv(t, t.TempDir(), "HOOK_RECORD="+recordPath, "HOOK_RUNS="+filepath.Join(base, "runs"))

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = repoDir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return out, err
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := run(args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	must("init", "--initial-branch=main", ".")
	if out, err := run("init", "--bare", remoteDir); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	must("config", "core.hooksPath", ".git/hooks")
	must("config", "user.name", "Hook Test")
	must("config", "user.email", "hook-test@example.invalid")
	must("remote", "add", "origin", remoteDir)

	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	prePush := "#!/bin/sh\n" + generateHookSection("pre-push")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(prePush), 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := "#!/bin/sh\n" +
		"printf 'run\\n' >> \"$HOOK_RUNS\"\n" +
		"printf 'sidechain refused push\\n' >&2\n" +
		"{\n" +
		"  printf 'argc=%s\\n' \"$#\"\n" +
		"  _bd_test_index=0\n" +
		"  for _bd_test_arg in \"$@\"; do\n" +
		"    printf 'arg-%s=<%s>\\n' \"$_bd_test_index\" \"$_bd_test_arg\"\n" +
		"    _bd_test_index=$((_bd_test_index + 1))\n" +
		"  done\n" +
		"  while IFS= read -r _bd_test_line || [ -n \"$_bd_test_line\" ]; do\n" +
		"    printf 'stdin=<%s>\\n' \"$_bd_test_line\"\n" +
		"  done\n" +
		"} > \"$HOOK_RECORD\"\n" +
		"exit 7\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push.old"), []byte(sidecar), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "f"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must("add", "f")
	must("commit", "-m", "with a chained pre-push gate")
	commitSHA := strings.TrimSpace(func() string {
		out, err := run("rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("git rev-parse HEAD: %v\n%s", err, out)
		}
		return string(out)
	}())

	pushOut, pushErr := run("push", "origin", "HEAD:refs/heads/main")
	if pushErr == nil {
		t.Fatalf("push succeeded; the chained pre-push gate was skipped or ignored:\n%s", pushOut)
	}
	if !strings.Contains(string(pushOut), "sidechain refused push") {
		t.Errorf("push output missing the sidecar's own diagnostic, so it may not have run:\n%s", pushOut)
	}

	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("sidecar never wrote its record; it did not run:\n%s", pushOut)
	}
	want := []string{
		"argc=2\n",
		"arg-0=<origin>\n",
		"arg-1=<" + remoteDir + ">\n",
		"stdin=<HEAD " + commitSHA + " refs/heads/main 0000000000000000000000000000000000000000>\n",
	}
	for _, fragment := range want {
		if !strings.Contains(string(record), fragment) {
			t.Errorf("sidecar record missing %q\nrecord:\n%s", fragment, record)
		}
	}

	runs, err := os.ReadFile(filepath.Join(base, "runs"))
	if err != nil {
		t.Fatalf("sidecar run counter missing: %v", err)
	}
	if got := strings.Count(string(runs), "run\n"); got != 1 {
		t.Errorf("sidecar ran %d times, want exactly 1\nruns:\n%s", got, runs)
	}

	// The gate's verdict must actually stop the push, not just be printed.
	check := exec.Command(gitPath, "--git-dir", remoteDir, "rev-parse", "--verify", "refs/heads/main")
	check.Env = env
	if out, err := check.CombinedOutput(); err == nil {
		t.Errorf("push reached the remote despite the chained gate refusing:\n%s", out)
	}
}

// TestGeneratedHookChainRunsOnceAndFallsThrough covers GH#6751's success
// path: without bd on PATH a successful foreign sidecar runs exactly once,
// the section then falls through to user content placed after the END
// marker, and with bd present the shell does not run the sidecar a second
// time next to bd's own chain.
func TestGeneratedHookChainRunsOnceAndFallsThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH-only fixtures are POSIX-specific")
	}

	build := func(t *testing.T) (hooksDir string, env []string) {
		t.Helper()
		hooksDir = t.TempDir()
		sidecar := "#!/bin/sh\n" +
			"printf 'GATE_OK\\n'\n" +
			"printf 'run\\n' >> \"$HOOK_RUNS\"\n" +
			"exit 0\n"
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit.old"), []byte(sidecar), 0o755); err != nil {
			t.Fatal(err)
		}
		hook := "#!/bin/sh\n" + generateHookSection("pre-commit") + "echo TRAILING_USER_CONTENT\n"
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		return hooksDir, []string{"HOOK_RUNS=" + filepath.Join(t.TempDir(), "runs")}
	}
	runHook := func(env []string, hooksDir string) (string, int) {
		t.Helper()
		cmd := exec.Command("/bin/sh", filepath.Join(hooksDir, "pre-commit"))
		cmd.Dir = hooksDir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run hook: %v\n%s", err, out)
			}
			code = exitErr.ExitCode()
		}
		return string(out), code
	}
	runsCount := func(t *testing.T, env []string) (string, int) {
		t.Helper()
		path := ""
		for _, entry := range env {
			if strings.HasPrefix(entry, "HOOK_RUNS=") {
				path = strings.TrimPrefix(entry, "HOOK_RUNS=")
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", 0
			}
			t.Fatal(err)
		}
		return string(data), strings.Count(string(data), "run\n")
	}

	t.Run("bd absent: sidecar once, trailing content still runs", func(t *testing.T) {
		hooksDir, env := build(t)
		emptyPath := t.TempDir() // no bd here
		env = append([]string{"PATH=" + emptyPath, "HOME=" + hooksDir}, env...)

		out, code := runHook(env, hooksDir)
		if code != 0 {
			t.Fatalf("hook exit = %d, want 0 (successful gate, trailing content)\n%s", code, out)
		}
		if !strings.Contains(out, "GATE_OK") {
			t.Errorf("output missing the sidecar's marker, so it did not run:\n%s", out)
		}
		if !strings.Contains(out, "TRAILING_USER_CONTENT") {
			t.Errorf("output missing content after the managed section — a successful chain must fall through, not exit:\n%s", out)
		}
		if _, count := runsCount(t, env); count != 1 {
			t.Errorf("sidecar ran %d times, want exactly 1", count)
		}
	})

	t.Run("bd present: the section leaves chaining to bd", func(t *testing.T) {
		hooksDir, env := build(t)
		binDir := t.TempDir()
		writeHookProcessFixture(t, binDir, "bd", hookProcessBDStub)
		env = append([]string{"PATH=" + binDir, "HOME=" + hooksDir}, env...)

		out, code := runHook(env, hooksDir)
		if code != 0 {
			t.Fatalf("hook exit = %d, want 0\n%s", code, out)
		}
		// bd was invoked (the stub reports its argv) and did the chaining.
		assertHookProcessBDInvocation(t, out, "pre-commit")
		if _, count := runsCount(t, env); count != 0 {
			t.Errorf("sidecar ran %d times with bd on PATH; the shell must not execute it next to bd's own chain", count)
		}
	})
}
