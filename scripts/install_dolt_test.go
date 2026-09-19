package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// pinnedDoltCLIVersion is the version scripts/ci/install-dolt.sh pins.
// TestPinnedDoltCLIMatchesContainerImage guards the pin itself; this file only
// needs a value the fake dolt can report back so the drift guard is exercised.
const pinnedDoltCLIVersion = "2.2.0"

// TestInstallDoltPropagatesDownloadFailure is the regression net for a download
// that never succeeds exiting 0. The script's whole purpose is keeping CI off an
// unpinned dolt, so a silent success there hands the suite whatever binary the
// runner image happened to ship — the exact drift the pin exists to prevent.
func TestInstallDoltPropagatesDownloadFailure(t *testing.T) {
	tests := []struct {
		name            string
		curlFailures    int
		reportedVersion string
		wantExit        int
		wantCurls       int
		wantSleeps      int
		wantExtracts    int
		wantInstalls    int
	}{
		{
			name:            "immediate success",
			curlFailures:    0,
			reportedVersion: pinnedDoltCLIVersion,
			wantExit:        0,
			wantCurls:       1,
			wantSleeps:      0,
			wantExtracts:    1,
			wantInstalls:    1,
		},
		{
			name:            "eventual success",
			curlFailures:    2,
			reportedVersion: pinnedDoltCLIVersion,
			wantExit:        0,
			wantCurls:       3,
			wantSleeps:      2,
			wantExtracts:    1,
			wantInstalls:    1,
		},
		{
			name:            "exhausted retries",
			curlFailures:    3,
			reportedVersion: pinnedDoltCLIVersion,
			wantExit:        42,
			wantCurls:       3,
			wantSleeps:      2,
			wantExtracts:    0,
			wantInstalls:    0,
		},
		{
			name:            "installed version drifts",
			curlFailures:    0,
			reportedVersion: "2.3.1",
			wantExit:        1,
			wantCurls:       1,
			wantSleeps:      0,
			wantExtracts:    1,
			wantInstalls:    1,
		},
		{
			// A substring drift guard would wave this through: "12.2.0"
			// contains "2.2.0".
			name:            "installed version merely contains the pin",
			curlFailures:    0,
			reportedVersion: "12.2.0",
			wantExit:        1,
			wantCurls:       1,
			wantSleeps:      0,
			wantExtracts:    1,
			wantInstalls:    1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runInstallDolt(t, test.curlFailures, test.reportedVersion)
			if got := pullDoltExitCode(run.err); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", got, test.wantExit, run.err, run.output)
			}
			if len(run.curlCalls) != test.wantCurls {
				t.Fatalf("curl count = %d, want %d; calls=%q\n%s", len(run.curlCalls), test.wantCurls, run.curlCalls, run.output)
			}
			if len(run.sleepCalls) != test.wantSleeps {
				t.Fatalf("sleep count = %d, want %d; calls=%q\n%s", len(run.sleepCalls), test.wantSleeps, run.sleepCalls, run.output)
			}
			if len(run.tarCalls) != test.wantExtracts {
				t.Fatalf("tar count = %d, want %d; calls=%q\n%s", len(run.tarCalls), test.wantExtracts, run.tarCalls, run.output)
			}
			if len(run.sudoCalls) != test.wantInstalls {
				t.Fatalf("sudo install count = %d, want %d; calls=%q\n%s", len(run.sudoCalls), test.wantInstalls, run.sudoCalls, run.output)
			}
			for _, call := range run.sleepCalls {
				if call != "5" {
					t.Fatalf("sleep call = %q, want 5", call)
				}
			}
			// Counts alone would let the script drift back to a
			// releases/latest URL or install somewhere other than the PATH
			// entry CI uses, so pin what each call actually asked for. uname
			// is stubbed to Linux/x86_64, which makes the asset name fixed.
			wantURL := "https://github.com/dolthub/dolt/releases/download/v" +
				pinnedDoltCLIVersion + "/dolt-linux-amd64.tar.gz"
			for _, call := range run.curlCalls {
				if !strings.HasPrefix(call, "-fsSL -o ") || !strings.HasSuffix(call, " "+wantURL) {
					t.Fatalf("curl call = %q, want -fsSL -o <workdir>/... %s", call, wantURL)
				}
			}
			const wantInstallSuffix = "/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt"
			for _, call := range run.sudoCalls {
				if !strings.HasPrefix(call, "install -m 0755 ") || !strings.HasSuffix(call, wantInstallSuffix) {
					t.Fatalf("sudo call = %q, want install -m 0755 <workdir>%s", call, wantInstallSuffix)
				}
			}
		})
	}
}

func TestInstallDoltHelperIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable bits")
	}

	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "install-dolt.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable", path)
	}
}

type installDoltRun struct {
	output     string
	curlCalls  []string
	sleepCalls []string
	tarCalls   []string
	sudoCalls  []string
	err        error
}

func runInstallDolt(t *testing.T, curlFailures int, reportedVersion string) installDoltRun {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is required to test install-dolt.sh: %v", err)
	}

	bin := t.TempDir()
	stateDir := t.TempDir()
	curlLog := filepath.Join(stateDir, "curl-calls")
	sleepLog := filepath.Join(stateDir, "sleep-calls")
	tarLog := filepath.Join(stateDir, "tar-calls")
	sudoLog := filepath.Join(stateDir, "sudo-calls")
	for _, path := range []string{curlLog, sleepLog, tarLog, sudoLog} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Fail the first DOLT_INSTALL_CURL_FAILURES attempts with a distinctive
	// status, then materialize the -o target so a success is indistinguishable
	// from a real download.
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
count=0
if [ -s "$DOLT_INSTALL_CURL_COUNT" ]; then
  IFS= read -r count <"$DOLT_INSTALL_CURL_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$DOLT_INSTALL_CURL_COUNT"
printf '%s\n' "$*" >>"$DOLT_INSTALL_CURL_LOG"
if [ "$count" -le "$DOLT_INSTALL_CURL_FAILURES" ]; then
  exit 42
fi
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  : >"$out"
fi
`)
	writeExecutable(t, filepath.Join(bin, "sleep"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DOLT_INSTALL_SLEEP_LOG"
`)
	writeExecutable(t, filepath.Join(bin, "tar"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DOLT_INSTALL_TAR_LOG"
`)
	writeExecutable(t, filepath.Join(bin, "sudo"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DOLT_INSTALL_SUDO_LOG"
`)
	writeExecutable(t, filepath.Join(bin, "dolt"), `#!/bin/sh
set -eu
printf 'dolt version %s\n' "$DOLT_INSTALL_REPORTED_VERSION"
`)
	// Pin the platform so the asset name and the extracted layout do not depend
	// on whichever machine runs the test.
	writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
`)

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
	statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
	commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		commandPath = binPath + ":/usr/bin:/bin"
	}
	root := sourceRepoRoot(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + commandPath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"DOLT_INSTALL_CURL_COUNT=" + statePath + "/curl-call-count",
		"DOLT_INSTALL_CURL_LOG=" + statePath + "/curl-calls",
		"DOLT_INSTALL_SLEEP_LOG=" + statePath + "/sleep-calls",
		"DOLT_INSTALL_TAR_LOG=" + statePath + "/tar-calls",
		"DOLT_INSTALL_SUDO_LOG=" + statePath + "/sudo-calls",
		"DOLT_INSTALL_CURL_FAILURES=" + strconv.Itoa(curlFailures),
		"DOLT_INSTALL_REPORTED_VERSION=" + reportedVersion,
	}
	for _, name := range []string{"curl", "sleep", "tar", "sudo", "dolt", "uname"} {
		requireShellCommandPath(t, bash, root, env, name, binPath+"/"+name)
	}

	cmd := bashScriptCommand(bash, "scripts/ci/install-dolt.sh")
	cmd.Dir = root
	cmd.Env = env
	output, runErr := cmd.CombinedOutput()

	return installDoltRun{
		output:     string(output),
		curlCalls:  readCallLines(t, curlLog),
		sleepCalls: readCallLines(t, sleepLog),
		tarCalls:   readCallLines(t, tarLog),
		sudoCalls:  readCallLines(t, sudoLog),
		err:        runErr,
	}
}
