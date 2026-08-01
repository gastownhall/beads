package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const doltSQLServerImage = "dolthub/dolt-sql-server:2.2.0"

func TestPullDoltImageRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name       string
		failures   int
		wantExit   int
		wantPulls  int
		wantSleeps int
	}{
		{name: "immediate success", failures: 0, wantExit: 0, wantPulls: 1, wantSleeps: 0},
		{name: "eventual success", failures: 2, wantExit: 0, wantPulls: 3, wantSleeps: 2},
		{name: "exhausted retries", failures: 3, wantExit: 42, wantPulls: 3, wantSleeps: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runPullDoltImage(t, test.failures)
			if got := pullDoltExitCode(run.err); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", got, test.wantExit, run.err, run.output)
			}
			if len(run.dockerCalls) != test.wantPulls {
				t.Fatalf("docker pull count = %d, want %d; calls=%q\n%s", len(run.dockerCalls), test.wantPulls, run.dockerCalls, run.output)
			}
			for _, call := range run.dockerCalls {
				if call != "pull "+doltSQLServerImage {
					t.Fatalf("docker call = %q, want %q", call, "pull "+doltSQLServerImage)
				}
			}
			if len(run.sleepCalls) != test.wantSleeps {
				t.Fatalf("sleep count = %d, want %d; calls=%q\n%s", len(run.sleepCalls), test.wantSleeps, run.sleepCalls, run.output)
			}
			for _, call := range run.sleepCalls {
				if call != "5" {
					t.Fatalf("sleep call = %q, want 5", call)
				}
			}
		})
	}
}

func TestDoltImagePullWorkflowsUseRetryHelper(t *testing.T) {
	wantCalls := map[string]int{
		"main.yml":       2,
		"pr.yml":         2,
		"pr-risk.yml":    2,
		"regression.yml": 1,
	}

	workflowsDir := filepath.Join(sourceRepoRoot(t), ".github", "workflows")
	for name, want := range wantCalls {
		data, err := os.ReadFile(filepath.Join(workflowsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if got := strings.Count(text, "run: ./scripts/ci/pull-dolt-image.sh"); got != want {
			t.Errorf("%s retry-helper calls = %d, want %d", name, got, want)
		}
	}

	workflowPaths, err := filepath.Glob(filepath.Join(workflowsDir, "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range workflowPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "docker pull "+doltSQLServerImage) {
			t.Errorf("%s still pulls the Dolt image without retries", filepath.Base(path))
		}
	}
}

func TestPullDoltImageHelperIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable bits")
	}

	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "pull-dolt-image.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable", path)
	}
}

type pullDoltRun struct {
	output      string
	dockerCalls []string
	sleepCalls  []string
	err         error
}

func runPullDoltImage(t *testing.T, failures int) pullDoltRun {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is required to test pull-dolt-image.sh: %v", err)
	}

	bin := t.TempDir()
	stateDir := t.TempDir()
	dockerLog := filepath.Join(stateDir, "docker-calls")
	sleepLog := filepath.Join(stateDir, "sleep-calls")
	for _, path := range []string{dockerLog, sleepLog} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeExecutable(t, filepath.Join(bin, "docker"), `#!/bin/sh
set -eu
count=0
if [ -s "$DOLT_PULL_CALL_COUNT" ]; then
  IFS= read -r count <"$DOLT_PULL_CALL_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$DOLT_PULL_CALL_COUNT"
printf '%s\n' "$*" >>"$DOLT_PULL_DOCKER_LOG"
if [ "$count" -le "$DOLT_PULL_FAILURES" ]; then
  exit 42
fi
`)
	writeExecutable(t, filepath.Join(bin, "sleep"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DOLT_PULL_SLEEP_LOG"
`)

	binPath := shellPath(t, bin)
	statePath := shellPath(t, stateDir)
	path := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = binPath + ":/usr/bin:/bin"
	}
	cmd := exec.Command(bash, "scripts/ci/pull-dolt-image.sh")
	cmd.Dir = sourceRepoRoot(t)
	cmd.Env = []string{
		"PATH=" + path,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"DOLT_PULL_CALL_COUNT=" + statePath + "/docker-call-count",
		"DOLT_PULL_DOCKER_LOG=" + statePath + "/docker-calls",
		"DOLT_PULL_SLEEP_LOG=" + statePath + "/sleep-calls",
		"DOLT_PULL_FAILURES=" + strconv.Itoa(failures),
	}
	output, runErr := cmd.CombinedOutput()

	dockerCalls := readCallLines(t, dockerLog)
	sleepCalls := readCallLines(t, sleepLog)
	return pullDoltRun{output: string(output), dockerCalls: dockerCalls, sleepCalls: sleepCalls, err: runErr}
}

func readCallLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func pullDoltExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
