package scripts_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	hostileSuccessSentinel = "CI_GATE_HOSTILE_SUCCESS_SENTINEL"
	failureFixtureStarted  = "CI_GATE_FAILURE_FIXTURE_STARTED"
	cleanSuccessSentinel   = "CI_GATE_CLEAN_SUCCESS_SENTINEL"
	cleanFixtureStarted    = "CI_GATE_CLEAN_FIXTURE_STARTED"
)

func TestPRCIGatePrivilegedExecBehavior(t *testing.T) {
	tools := findCIGateBashTools(t)
	testDir := t.TempDir()

	failingFixture := writeBashTestScript(t, testDir, "failing-gate.sh", fmt.Sprintf(`printf '%%s\n' %s
exit 23
printf '%%s\n' %s
`, bashQuote(failureFixtureStarted), bashQuote(hostileSuccessSentinel)))
	failingOuter := writeBashTestScript(t, testDir, "failing-outer.sh", fmt.Sprintf(
		"exec /usr/bin/bash --noprofile --norc -p -euo pipefail -- %s\n",
		bashQuote(toBashPath(failingFixture)),
	))
	poison := writeBashTestScript(t, testDir, "poison.sh", fmt.Sprintf(
		"printf '%%s\\n' %s\n",
		bashQuote(hostileSuccessSentinel),
	))
	hostileHarness := writeBashTestScript(t, testDir, "hostile-harness.sh", fmt.Sprintf(`source() {
  printf '%%s\n' %s
}
exit() {
  printf '%%s\n' %s
  return 0
}
export -f source exit
export BASH_ENV=%s
export ENV=%s
shopt -s extdebug
set -o noclobber
export BASHOPTS SHELLOPTS
exec /usr/bin/env -u BASH_ENV -u ENV -u BASHOPTS -u SHELLOPTS /usr/bin/bash --noprofile --norc -p -euo pipefail %s
`, bashQuote(hostileSuccessSentinel), bashQuote(hostileSuccessSentinel), bashQuote(toBashPath(poison)), bashQuote(toBashPath(poison)), bashQuote(toBashPath(failingOuter))))

	t.Run("hostile exported functions cannot swallow failure", func(t *testing.T) {
		output, err := runBashCommand(
			t,
			tools.bash,
			[]string{"--noprofile", "--norc", "-euo", "pipefail", toBashPath(hostileHarness)},
			testDir,
			cleanShellEnvironment(nil),
		)
		requireExitCode(t, err, 23, output)
		if !strings.Contains(output, failureFixtureStarted) {
			t.Fatalf("failing fixture was not reached; output:\n%s", output)
		}
		if strings.Contains(output, hostileSuccessSentinel) {
			t.Fatalf("hostile startup state swallowed the failing gate; output:\n%s", output)
		}
	})

	cleanFixture := writeBashTestScript(t, testDir, "clean-gate.sh", fmt.Sprintf(
		"printf '%%s\\n' %s\nprintf '%%s\\n' %s\n",
		bashQuote(cleanFixtureStarted), bashQuote(cleanSuccessSentinel),
	))
	cleanOuter := writeBashTestScript(t, testDir, "clean-outer.sh", fmt.Sprintf(
		"exec /usr/bin/bash --noprofile --norc -p -euo pipefail -- %s\n",
		bashQuote(toBashPath(cleanFixture)),
	))

	t.Run("clean fixture executes through the same boundary", func(t *testing.T) {
		output, err := runCIOuterShell(t, tools, cleanOuter, testDir, nil)
		if err != nil {
			t.Fatalf("clean fixture failed: %v\n%s", err, output)
		}
		for _, marker := range []string{cleanFixtureStarted, cleanSuccessSentinel} {
			if !strings.Contains(output, marker) {
				t.Fatalf("clean fixture output lacks %q:\n%s", marker, output)
			}
		}
	})
}

func TestPRCIGateHostAndWorkspaceGuardsBehavior(t *testing.T) {
	tools := findCIGateBashTools(t)
	step := readCIWorkflow(t, "pr.yml").job(t, "ci-gate").step(t, "Evaluate CI gate")
	const unameAssignment = `host_kernel="$(/usr/bin/uname -s)"`
	if strings.Count(step.Run, unameAssignment) != 1 {
		t.Fatalf("ci-gate must have one exact host-kernel assignment before behavioral falsification:\n%s", step.Run)
	}

	repoRoot := sourceRepoRoot(t)
	workspace := toBashPath(repoRoot)
	baseEnv := map[string]string{
		"CHECK_TEST":        "success",
		"CI_GATE_NAME":      "CI_GATE_BOUND_HOST_SENTINEL",
		"CI_GATE_REQUIRED":  "CHECK_TEST",
		"GITHUB_EVENT_NAME": "pull_request",
		"GITHUB_WORKSPACE":  workspace,
	}

	t.Run("wrong host falsifier", func(t *testing.T) {
		wrongHostRun := strings.Replace(step.Run, unameAssignment, `host_kernel="Darwin"`, 1)
		wrongHostScript := writeBashTestScript(t, t.TempDir(), "wrong-host.sh", wrongHostRun)
		output, err := runCIOuterShell(t, tools, wrongHostScript, repoRoot, baseEnv)
		requireExitCode(t, err, 1, output)
		if !strings.Contains(output, "ci-gate requires Linux from /usr/bin/uname, got 'Darwin'") {
			t.Fatalf("wrong-host guard did not report its rejection:\n%s", output)
		}
		if strings.Contains(output, "CI_GATE_BOUND_HOST_SENTINEL passed") {
			t.Fatalf("wrong-host execution reached the success sentinel:\n%s", output)
		}
	})

	t.Run("workspace must be nonempty and exact", func(t *testing.T) {
		linuxRun := strings.Replace(step.Run, unameAssignment, `host_kernel="Linux"`, 1)
		linuxScript := writeBashTestScript(t, t.TempDir(), "forced-linux.sh", linuxRun)

		for name, badWorkspace := range map[string]string{
			"empty":    "",
			"mismatch": workspace + "/not-the-checkout",
			"relative": "relative/workspace",
		} {
			t.Run(name, func(t *testing.T) {
				environment := cloneStringMap(baseEnv)
				environment["GITHUB_WORKSPACE"] = badWorkspace
				output, err := runCIOuterShell(t, tools, linuxScript, repoRoot, environment)
				requireExitCode(t, err, 1, output)
				if strings.Contains(output, "CI_GATE_BOUND_HOST_SENTINEL passed") {
					t.Fatalf("invalid workspace reached the success sentinel:\n%s", output)
				}
			})
		}
	})

	t.Run("unmodified workflow agrees with current host", func(t *testing.T) {
		workflowScript := writeBashTestScript(t, t.TempDir(), "workflow.sh", step.Run)
		output, err := runCIOuterShell(t, tools, workflowScript, repoRoot, baseEnv)
		if runtime.GOOS == "linux" {
			if err != nil {
				t.Fatalf("Linux-bound workflow failed on Linux: %v\n%s", err, output)
			}
			if !strings.Contains(output, "CI_GATE_BOUND_HOST_SENTINEL passed") {
				t.Fatalf("Linux-bound workflow did not reach the gate success sentinel:\n%s", output)
			}
			return
		}

		requireExitCode(t, err, 1, output)
		if !strings.Contains(output, "ci-gate requires Linux from /usr/bin/uname") {
			t.Fatalf("non-Linux host did not fail at the host guard:\n%s", output)
		}
	})
}

type ciGateBashTools struct {
	bash string
	env  string
}

func findCIGateBashTools(t *testing.T) ciGateBashTools {
	t.Helper()

	if runtime.GOOS != "windows" {
		for _, path := range []string{"/usr/bin/bash", "/usr/bin/env"} {
			if info, err := os.Stat(path); err != nil || info.IsDir() {
				t.Skipf("exact CI Bash boundary is unavailable locally: %s", path)
			}
		}
		return ciGateBashTools{bash: "/usr/bin/bash", env: "/usr/bin/env"}
	}

	for _, pathEntry := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(pathEntry, "bash.exe")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		root := filepath.Dir(pathEntry)
		if strings.EqualFold(filepath.Base(pathEntry), "bin") && strings.EqualFold(filepath.Base(root), "usr") {
			root = filepath.Dir(root)
		}
		bashPath := filepath.Join(root, "usr", "bin", "bash.exe")
		envPath := filepath.Join(root, "usr", "bin", "env.exe")
		if regularFile(bashPath) && regularFile(envPath) {
			return ciGateBashTools{bash: bashPath, env: envPath}
		}
	}

	t.Skip("Git for Windows /usr/bin/bash and /usr/bin/env are unavailable locally")
	return ciGateBashTools{}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func runCIOuterShell(t *testing.T, tools ciGateBashTools, script, workingDir string, extraEnv map[string]string) (string, error) {
	t.Helper()
	return runBashCommand(
		t,
		tools.env,
		[]string{
			"-u", "BASH_ENV",
			"-u", "ENV",
			"-u", "BASHOPTS",
			"-u", "SHELLOPTS",
			"/usr/bin/bash", "--noprofile", "--norc", "-p", "-euo", "pipefail", toBashPath(script),
		},
		workingDir,
		cleanShellEnvironment(extraEnv),
	)
}

func runBashCommand(t *testing.T, program string, args []string, workingDir string, environment []string) (string, error) {
	t.Helper()
	command := exec.Command(program, args...)
	command.Dir = workingDir
	command.Env = environment
	output, err := command.CombinedOutput()
	return string(output), err
}

func cleanShellEnvironment(extra map[string]string) []string {
	blocked := map[string]bool{
		"BASHOPTS":  true,
		"BASH_ENV":  true,
		"ENV":       true,
		"OLDPWD":    true,
		"PWD":       true,
		"SHELLOPTS": true,
	}
	for key := range extra {
		blocked[strings.ToUpper(key)] = true
	}

	environment := make([]string, 0, len(os.Environ())+len(extra))
	for _, assignment := range os.Environ() {
		key, _, found := strings.Cut(assignment, "=")
		upperKey := strings.ToUpper(key)
		if found && (blocked[upperKey] || strings.HasPrefix(upperKey, "BASH_FUNC_")) {
			continue
		}
		environment = append(environment, assignment)
	}

	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+extra[key])
	}
	return environment
}

func writeBashTestScript(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireExitCode(t *testing.T, err error, want int, output string) {
	t.Helper()
	if err == nil {
		t.Fatalf("command succeeded, want exit %d; output:\n%s", want, output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("command error = %v, want exit %d; output:\n%s", err, want, output)
	}
	if got := exitError.ExitCode(); got != want {
		t.Fatalf("command exit = %d, want %d; output:\n%s", got, want, output)
	}
}

func toBashPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS != "windows" {
		return filepath.ToSlash(path)
	}

	volume := filepath.VolumeName(path)
	if len(volume) == 2 && volume[1] == ':' {
		remainder := strings.TrimPrefix(path, volume)
		return "/" + strings.ToLower(volume[:1]) + filepath.ToSlash(remainder)
	}
	return filepath.ToSlash(path)
}

func bashQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
