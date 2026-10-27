package scripts_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The pinned NDK scripts/ci/install-android-ndk.sh installs. Tests only need
// it to recognize the pin; the download itself is a fake.
const pinnedAndroidNDKRevision = "30.0.16248370"

const (
	pinnedAndroidNDKURL = "https://dl.google.com/android/repository/android-ndk-r30-linux.zip"
	pinnedAndroidNDKDir = "android-ndk-r30"
)

// TestInstallAndroidNDK pins the installer half of the #3538 contract: only
// the pinned URL is ever downloaded, retries are bounded and back off,
// success prints exactly one BEADS_ANDROID_NDK_HOME=<root> line (stdout and
// $GITHUB_ENV), and every failure path -- exhausted downloads, a SHA-256
// that does not match the pin, an extracted tree of another revision, a
// non-Linux-x86_64 host -- exits non-zero having written nothing to
// $GITHUB_ENV. An image NDK of exactly the pinned revision is used as-is.
func TestInstallAndroidNDK(t *testing.T) {
	tests := []struct {
		name string
		// ANDROID_HOME layout: "" unsets it, otherwise a map of ndk subdir
		// (revision) to whether that tree is valid.
		androidHome  map[string]bool
		curlFailures int
		shaFail      bool
		extractedRev string
		unameOS      string
		unameArch    string
		wantExit     int
		wantCurls    int
		wantUnzips   int
		// empty: expect no BEADS_ANDROID_NDK_HOME at all.
		wantRootFrom string // "dest" (download) or "image" (fast path)
	}{
		{
			name:         "downloads the pinned NDK",
			curlFailures: 0,
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     0,
			wantCurls:    1,
			wantUnzips:   1,
			wantRootFrom: "dest",
		},
		{
			name:         "exhausted downloads fail closed",
			curlFailures: 3,
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     42,
			wantCurls:    3,
			wantUnzips:   0,
		},
		{
			name:         "sha mismatch fails without extracting",
			shaFail:      true,
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     1,
			wantCurls:    1,
			wantUnzips:   0,
		},
		{
			name:         "extracted revision drift fails",
			extractedRev: "27.3.13750724",
			wantExit:     1,
			wantCurls:    1,
			wantUnzips:   1,
		},
		{
			name:         "image NDK of the pinned revision is reused",
			androidHome:  map[string]bool{pinnedAndroidNDKRevision: true},
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     0,
			wantCurls:    0,
			wantUnzips:   0,
			wantRootFrom: "image",
		},
		{
			// The runner image's default NDK is a different revision: it
			// must be ignored, not silently used.
			name:         "image NDK of another revision is not reused",
			androidHome:  map[string]bool{"27.3.13750724": true},
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     0,
			wantCurls:    1,
			wantUnzips:   1,
			wantRootFrom: "dest",
		},
		{
			name:         "non-Linux-x86_64 host is refused",
			unameOS:      "Darwin",
			unameArch:    "arm64",
			extractedRev: pinnedAndroidNDKRevision,
			wantExit:     1,
			wantCurls:    0,
			wantUnzips:   0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bash := requireHostTool(t, "bash")
			root := sourceRepoRoot(t)

			bin := t.TempDir()
			stateDir := t.TempDir()
			destDir := t.TempDir()
			curlLog := filepath.Join(stateDir, "curl-calls")
			sleepLog := filepath.Join(stateDir, "sleep-calls")
			unzipLog := filepath.Join(stateDir, "unzip-calls")
			githubEnv := filepath.Join(stateDir, "github-env")
			for _, path := range []string{curlLog, sleepLog, unzipLog, githubEnv} {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// An image NDK tree (fast path), with source.properties in the
			// real NDK's "Pkg.Revision = <rev>" shape.
			writeImageNDK := func(parent, revision string) string {
				dir := filepath.Join(parent, "ndk", revision)
				if err := os.MkdirAll(filepath.Join(dir, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin"), 0o755); err != nil {
					t.Fatal(err)
				}
				props := fmt.Sprintf("Pkg.Desc = Android NDK\nPkg.Revision = %s\n", revision)
				if err := os.WriteFile(filepath.Join(dir, "source.properties"), []byte(props), 0o644); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, filepath.Join(dir, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin", "aarch64-linux-android21-clang"), "#!/bin/sh\nexit 0\n")
				return dir
			}
			androidHome := ""
			if test.androidHome != nil {
				androidHome = t.TempDir()
				for revision, valid := range test.androidHome {
					if valid {
						writeImageNDK(androidHome, revision)
					}
				}
			}
			var imageRoot string
			if valid, ok := test.androidHome[pinnedAndroidNDKRevision]; ok && valid {
				imageRoot = filepath.Join(androidHome, "ndk", pinnedAndroidNDKRevision)
			}

			// Fail the first BEADS_TEST_NDK_CURL_FAILURES attempts with a
			// distinctive status, then materialize the -o target.
			writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
count=0
if [ -s "$BEADS_TEST_NDK_CURL_COUNT" ]; then
  IFS= read -r count <"$BEADS_TEST_NDK_CURL_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$BEADS_TEST_NDK_CURL_COUNT"
printf '%s\n' "$*" >>"$BEADS_TEST_NDK_CURL_LOG"
if [ "$count" -le "$BEADS_TEST_NDK_CURL_FAILURES" ]; then
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
printf '%s\n' "$*" >>"$BEADS_TEST_NDK_SLEEP_LOG"
`)
			writeExecutable(t, filepath.Join(bin, "sha256sum"), `#!/bin/sh
set -eu
cat >/dev/null
if [ "${BEADS_TEST_NDK_SHA_FAIL:-}" = 1 ]; then
  printf '%s: FAILED\n' "$*" >&2
  exit 1
fi
`)
			writeExecutable(t, filepath.Join(bin, "unzip"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$BEADS_TEST_NDK_UNZIP_LOG"
dest=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-d" ]; then
    dest="$arg"
  fi
  prev="$arg"
done
root="$dest/`+pinnedAndroidNDKDir+`"
mkdir -p "$root/toolchains/llvm/prebuilt/linux-x86_64/bin"
printf 'Pkg.Revision = %s\n' "$BEADS_TEST_NDK_REVISION" >"$root/source.properties"
printf '#!/bin/sh\nexit 0\n' >"$root/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang"
chmod +x "$root/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang"
`)
			writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf '%s\n' "${BEADS_TEST_NDK_UNAME_ARCH:-x86_64}" ;;
  *) printf '%s\n' "${BEADS_TEST_NDK_UNAME_OS:-Linux}" ;;
esac
`)

			pathEnv := shellPathEnv()
			binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
			statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
			commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
			if runtime.GOOS == "windows" {
				commandPath = binPath + ":/usr/bin:/bin"
			}
			env := []string{
				"PATH=" + os.Getenv("PATH"),
				"BEADS_TEST_COMMAND_PATH=" + commandPath,
				"LC_ALL=C",
				"LANG=C",
				"BASH_ENV=",
				"ENV=",
				"BEADS_TEST_NDK_CURL_COUNT=" + statePath + "/curl-call-count",
				"BEADS_TEST_NDK_CURL_LOG=" + statePath + "/curl-calls",
				"BEADS_TEST_NDK_SLEEP_LOG=" + statePath + "/sleep-calls",
				"BEADS_TEST_NDK_UNZIP_LOG=" + statePath + "/unzip-calls",
				"BEADS_TEST_NDK_CURL_FAILURES=" + fmt.Sprint(test.curlFailures),
				"BEADS_TEST_NDK_REVISION=" + test.extractedRev,
				"BEADS_TEST_NDK_UNAME_OS=" + test.unameOS,
				"BEADS_TEST_NDK_UNAME_ARCH=" + test.unameArch,
				"GITHUB_ENV=" + shellPathUnderEnv(t, bash, githubEnv, pathEnv),
			}
			if test.shaFail {
				env = append(env, "BEADS_TEST_NDK_SHA_FAIL=1")
			}
			if androidHome != "" {
				env = append(env, "ANDROID_HOME="+shellPathUnderEnv(t, bash, androidHome, pathEnv))
			}
			for _, name := range []string{"curl", "sleep", "sha256sum", "unzip", "uname"} {
				requireShellCommandPath(t, bash, root, env, name, binPath+"/"+name)
			}

			cmd := exec.Command(bash, "--noprofile", "--norc", "-c",
				`PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc "$@"`,
				"script-test", "scripts/ci/install-android-ndk.sh",
				shellPathUnderEnv(t, bash, destDir, pathEnv))
			cmd.Dir = root
			cmd.Env = env
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()

			if got := exitCode(runErr); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; err=%v\nstdout:\n%s\nstderr:\n%s", got, test.wantExit, runErr, stdout.String(), stderr.String())
			}

			curlCalls := readCallLines(t, curlLog)
			sleepCalls := readCallLines(t, sleepLog)
			unzipCalls := readCallLines(t, unzipLog)
			if len(curlCalls) != test.wantCurls {
				t.Fatalf("curl count = %d, want %d; calls=%q\nstderr:\n%s", len(curlCalls), test.wantCurls, curlCalls, stderr.String())
			}
			for _, call := range curlCalls {
				if !strings.HasSuffix(call, " "+pinnedAndroidNDKURL) {
					t.Fatalf("curl call = %q, want it to download only %s", call, pinnedAndroidNDKURL)
				}
				if !strings.Contains(call, "--connect-timeout 30") || !strings.Contains(call, "--max-time 240") {
					t.Fatalf("curl call = %q, want bounded connect (--connect-timeout 30) and transfer (--max-time 240)", call)
				}
			}
			// Bounded retries: one 5-second backoff between attempts, none
			// after the last.
			if wantSleeps := test.wantCurls - 1; wantSleeps > 0 {
				if len(sleepCalls) != wantSleeps {
					t.Fatalf("sleep count = %d, want %d; calls=%q", len(sleepCalls), wantSleeps, sleepCalls)
				}
				for _, call := range sleepCalls {
					if call != "5" {
						t.Fatalf("sleep call = %q, want 5", call)
					}
				}
			} else if len(sleepCalls) != 0 {
				t.Fatalf("sleep calls = %q, want none", sleepCalls)
			}
			if len(unzipCalls) != test.wantUnzips {
				t.Fatalf("unzip count = %d, want %d; calls=%q\nstderr:\n%s", len(unzipCalls), test.wantUnzips, unzipCalls, stderr.String())
			}

			githubEnvBytes, err := os.ReadFile(githubEnv)
			if err != nil {
				t.Fatal(err)
			}
			var wantLine string
			switch test.wantRootFrom {
			case "dest":
				wantLine = "BEADS_ANDROID_NDK_HOME=" + shellPathUnderEnv(t, bash, destDir, pathEnv) + "/" + pinnedAndroidNDKDir
			case "image":
				wantLine = "BEADS_ANDROID_NDK_HOME=" + shellPathUnderEnv(t, bash, imageRoot, pathEnv)
			}
			if wantLine == "" {
				if len(githubEnvBytes) != 0 {
					t.Fatalf("failure path wrote to $GITHUB_ENV: %q\nstderr:\n%s", githubEnvBytes, stderr.String())
				}
				if got := strings.TrimSpace(stdout.String()); got != "" {
					t.Fatalf("failure path printed %q to stdout, want nothing", got)
				}
				return
			}
			if got := stdout.String(); got != wantLine+"\n" {
				t.Fatalf("stdout = %q, want exactly %q\nstderr:\n%s", got, wantLine+"\n", stderr.String())
			}
			if got := string(githubEnvBytes); got != wantLine+"\n" {
				t.Fatalf("$GITHUB_ENV = %q, want %q", got, wantLine+"\n")
			}
		})
	}
}

func TestInstallAndroidNDKHelperIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable bits")
	}

	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "install-android-ndk.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable", path)
	}
}
