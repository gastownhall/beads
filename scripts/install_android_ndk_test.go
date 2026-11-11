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

const (
	pinnedAndroidNDKRevision = "30.0.16248370"
	pinnedAndroidNDKURL      = "https://dl.google.com/android/repository/android-ndk-r30-linux.zip"
	pinnedAndroidNDKSHA256   = "753611f410d002cfcd3f3dc2ef49aad532089d3180b436c060a90bf0fcb64df2"
	androidNDKClangRel       = "toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang"
)

// TestInstallAndroidNDK pins scripts/ci/install-android-ndk.sh, which supplies
// the NDK the bd-android-arm64 release links against (#3538). It must download
// only the pinned zip, retry like install-dolt.sh, and never hand out an NDK it
// did not verify: a failed download, a wrong digest or a wrong extracted
// revision exits non-zero and writes nothing to $GITHUB_ENV.
func TestInstallAndroidNDK(t *testing.T) {
	tests := []struct {
		name string
		installAndroidNDKOptions
		wantExit    int
		wantCurls   int
		wantSleeps  int
		wantUnzips  int
		wantOutput  string
		wantFast    bool // the root is $ANDROID_HOME/ndk/<revision>
		wantNoUsage bool
	}{
		{name: "immediate success", wantExit: 0, wantCurls: 1, wantUnzips: 1},
		{name: "eventual success", installAndroidNDKOptions: installAndroidNDKOptions{curlFailures: 2}, wantExit: 0, wantCurls: 3, wantSleeps: 2, wantUnzips: 1},
		{name: "exhausted retries", installAndroidNDKOptions: installAndroidNDKOptions{curlFailures: 3}, wantExit: 42, wantCurls: 3, wantSleeps: 2, wantOutput: "after 3 attempts"},
		{name: "SHA-256 differs from the pin", installAndroidNDKOptions: installAndroidNDKOptions{digest: strings.Repeat("0", 64)}, wantExit: 1, wantCurls: 1, wantOutput: "want " + pinnedAndroidNDKSHA256},
		{name: "extracted revision differs", installAndroidNDKOptions: installAndroidNDKOptions{extractedRevision: "30.0.16248371"}, wantExit: 1, wantCurls: 1, wantUnzips: 1, wantOutput: "revision " + pinnedAndroidNDKRevision},
		{name: "extracted revision merely contains the pin", installAndroidNDKOptions: installAndroidNDKOptions{extractedRevision: "130.0.16248370"}, wantExit: 1, wantCurls: 1, wantUnzips: 1},
		{name: "pinned NDK under ANDROID_HOME", installAndroidNDKOptions: installAndroidNDKOptions{androidHomeRevision: pinnedAndroidNDKRevision}, wantExit: 0, wantFast: true},
		// The image default (r27, at ANDROID_NDK_HOME) or any other revision
		// under $ANDROID_HOME/ndk/<pin> falls through to the download.
		{name: "other NDK under ANDROID_HOME", installAndroidNDKOptions: installAndroidNDKOptions{androidHomeRevision: "27.3.13750724"}, wantExit: 0, wantCurls: 1, wantUnzips: 1},
		{name: "ANDROID_NDK_HOME is never read", installAndroidNDKOptions: installAndroidNDKOptions{ndkHomeIsPinned: true}, wantExit: 0, wantCurls: 1, wantUnzips: 1},
		{name: "already installed in dest", installAndroidNDKOptions: installAndroidNDKOptions{preinstalled: true}, wantExit: 0},
		{name: "dest defaults to RUNNER_TEMP", installAndroidNDKOptions: installAndroidNDKOptions{destFromRunnerTemp: true}, wantExit: 0, wantCurls: 1, wantUnzips: 1},
		{name: "no dest and no RUNNER_TEMP", installAndroidNDKOptions: installAndroidNDKOptions{noDest: true}, wantExit: 2, wantOutput: "Usage:"},
		{name: "macOS host", installAndroidNDKOptions: installAndroidNDKOptions{unameS: "Darwin", unameM: "arm64"}, wantExit: 1, wantOutput: "bd-android-arm64) is Linux x86_64 only"},
		{name: "linux arm64 host", installAndroidNDKOptions: installAndroidNDKOptions{unameM: "aarch64"}, wantExit: 1, wantOutput: "Linux x86_64 only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runInstallAndroidNDK(t, test.installAndroidNDKOptions)
			if got := exitCode(run.err); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", got, test.wantExit, run.err, run.stderr)
			}
			if len(run.curlCalls) != test.wantCurls {
				t.Fatalf("curl count = %d, want %d; calls=%q\n%s", len(run.curlCalls), test.wantCurls, run.curlCalls, run.stderr)
			}
			if len(run.sleepCalls) != test.wantSleeps {
				t.Fatalf("sleep count = %d, want %d; calls=%q", len(run.sleepCalls), test.wantSleeps, run.sleepCalls)
			}
			if len(run.unzipCalls) != test.wantUnzips {
				t.Fatalf("unzip count = %d, want %d; calls=%q\n%s", len(run.unzipCalls), test.wantUnzips, run.unzipCalls, run.stderr)
			}
			for _, call := range run.sleepCalls {
				if call != "5" {
					t.Fatalf("sleep call = %q, want 5", call)
				}
			}
			for _, call := range run.curlCalls {
				if !strings.HasPrefix(call, "-fsSL --connect-timeout 30 --max-time 240 -o ") || !strings.HasSuffix(call, " "+pinnedAndroidNDKURL) {
					t.Fatalf("curl call = %q, want -fsSL --connect-timeout 30 --max-time 240 -o <zip> %s", call, pinnedAndroidNDKURL)
				}
			}
			if !strings.Contains(run.stderr, test.wantOutput) {
				t.Fatalf("stderr does not contain %q:\n%s", test.wantOutput, run.stderr)
			}

			if test.wantExit != 0 {
				if run.stdout != "" || run.githubEnv != "" {
					t.Fatalf("failed install printed %q and wrote %q to GITHUB_ENV, want neither", run.stdout, run.githubEnv)
				}
				if _, err := os.Stat(filepath.Join(run.dest, "android-ndk-r30")); err == nil {
					t.Fatalf("failed install left %s/android-ndk-r30 in place", run.dest)
				}
				return
			}
			wantRoot := run.destShell + "/android-ndk-r30"
			if test.wantFast {
				wantRoot = run.androidHomeShell + "/ndk/" + pinnedAndroidNDKRevision
			}
			wantLine := "BEADS_ANDROID_NDK_HOME=" + wantRoot + "\n"
			if run.stdout != wantLine {
				t.Fatalf("stdout = %q, want %q", run.stdout, wantLine)
			}
			if run.githubEnv != wantLine {
				t.Fatalf("GITHUB_ENV = %q, want %q", run.githubEnv, wantLine)
			}
			if !test.wantFast {
				clang := filepath.Join(run.dest, "android-ndk-r30", filepath.FromSlash(androidNDKClangRel))
				if _, err := os.Stat(clang); err != nil {
					t.Fatalf("installed NDK has no API-21 clang: %v", err)
				}
			}
		})
	}
}

type installAndroidNDKOptions struct {
	curlFailures        int
	digest              string // what the fake sha256sum reports; default the pin
	extractedRevision   string // Pkg.Revision in the zip; default the pin
	androidHomeRevision string // an NDK at $ANDROID_HOME/ndk/<pin> with this revision
	ndkHomeIsPinned     bool   // ANDROID_NDK_HOME holds a valid pinned NDK
	preinstalled        bool   // dest already holds the pinned NDK
	destFromRunnerTemp  bool
	noDest              bool
	unameS, unameM      string
}

type installAndroidNDKRun struct {
	stdout, stderr   string
	githubEnv        string
	dest, destShell  string
	androidHomeShell string
	curlCalls        []string
	sleepCalls       []string
	unzipCalls       []string
	err              error
}

// writeFakeAndroidNDK lays out the files install-android-ndk.sh checks.
func writeFakeAndroidNDK(t *testing.T, root, revision string) {
	t.Helper()
	bin := filepath.Dir(filepath.Join(root, filepath.FromSlash(androidNDKClangRel)))
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aarch64-linux-android21-clang", "aarch64-linux-android21-clang++"} {
		writeExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nexit 0\n")
	}
	props := "Pkg.Desc = Android NDK\nPkg.Revision = " + revision + "\nPkg.BaseRevision = 30.0.16248370\n"
	if err := os.WriteFile(filepath.Join(root, "source.properties"), []byte(props), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runInstallAndroidNDK(t *testing.T, opts installAndroidNDKOptions) installAndroidNDKRun {
	t.Helper()
	// Not only Windows: the download path renames with GNU mv -T, which
	// macOS's mv lacks, and the faked uname cannot change that.
	if runtime.GOOS != "linux" {
		t.Skip("the android NDK install is Linux x86_64 only")
	}
	if opts.digest == "" {
		opts.digest = pinnedAndroidNDKSHA256
	}
	if opts.extractedRevision == "" {
		opts.extractedRevision = pinnedAndroidNDKRevision
	}
	if opts.unameS == "" {
		opts.unameS = "Linux"
	}
	if opts.unameM == "" {
		opts.unameM = "x86_64"
	}

	bash := requireHostTool(t, "bash")
	bin := t.TempDir()
	stateDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "ndk-dest")
	androidHome := t.TempDir()
	ndkHome := t.TempDir()
	logs := map[string]string{}
	for _, name := range []string{"curl", "sleep", "unzip", "github-env"} {
		logs[name] = filepath.Join(stateDir, name)
		if err := os.WriteFile(logs[name], nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if opts.androidHomeRevision != "" {
		writeFakeAndroidNDK(t, filepath.Join(androidHome, "ndk", pinnedAndroidNDKRevision), opts.androidHomeRevision)
	}
	if opts.ndkHomeIsPinned {
		writeFakeAndroidNDK(t, ndkHome, pinnedAndroidNDKRevision)
	}
	if opts.preinstalled {
		writeFakeAndroidNDK(t, filepath.Join(dest, "android-ndk-r30"), pinnedAndroidNDKRevision)
	}

	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
count=0
if [ -s "$NDK_INSTALL_CURL_COUNT" ]; then
  IFS= read -r count <"$NDK_INSTALL_CURL_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$NDK_INSTALL_CURL_COUNT"
printf '%s\n' "$*" >>"$NDK_INSTALL_CURL_LOG"
if [ "$count" -le "$NDK_INSTALL_CURL_FAILURES" ]; then
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
: >"$out"
`)
	writeExecutable(t, filepath.Join(bin, "sleep"), `#!/bin/sh
printf '%s\n' "$*" >>"$NDK_INSTALL_SLEEP_LOG"
`)
	writeExecutable(t, filepath.Join(bin, "sha256sum"), `#!/bin/sh
printf '%s  %s\n' "$NDK_INSTALL_DIGEST" "$1"
`)
	// unzip -q <zip> -d <dir>: extract a fake NDK tree reporting the
	// configured revision.
	writeExecutable(t, filepath.Join(bin, "unzip"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$NDK_INSTALL_UNZIP_LOG"
[ "$1" = "-q" ] && [ "$3" = "-d" ] || exit 9
root="$4/android-ndk-r30"
bindir="$root/toolchains/llvm/prebuilt/linux-x86_64/bin"
mkdir -p "$bindir"
for name in aarch64-linux-android21-clang aarch64-linux-android21-clang++; do
  printf '#!/bin/sh\nexit 0\n' >"$bindir/$name"
  chmod +x "$bindir/$name"
done
printf 'Pkg.Desc = Android NDK\nPkg.Revision = %s\n' "$NDK_INSTALL_REVISION" >"$root/source.properties"
`)
	writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
case "${1:-}" in
  -m) printf '%s\n' "$NDK_INSTALL_UNAME_M" ;;
  *) printf '%s\n' "$NDK_INSTALL_UNAME_S" ;;
esac
`)

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
	statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
	destParent := shellPathUnderEnv(t, bash, filepath.Dir(dest), pathEnv)
	destShell := destParent + "/" + filepath.Base(dest)
	androidHomeShell := shellPathUnderEnv(t, bash, androidHome, pathEnv)
	root := sourceRepoRoot(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"GITHUB_ENV=" + statePath + "/github-env",
		"ANDROID_HOME=" + androidHomeShell,
		"ANDROID_NDK_HOME=" + shellPathUnderEnv(t, bash, ndkHome, pathEnv),
		"NDK_INSTALL_CURL_COUNT=" + statePath + "/curl-count",
		"NDK_INSTALL_CURL_LOG=" + statePath + "/curl",
		"NDK_INSTALL_SLEEP_LOG=" + statePath + "/sleep",
		"NDK_INSTALL_UNZIP_LOG=" + statePath + "/unzip",
		"NDK_INSTALL_CURL_FAILURES=" + strconv.Itoa(opts.curlFailures),
		"NDK_INSTALL_DIGEST=" + opts.digest,
		"NDK_INSTALL_REVISION=" + opts.extractedRevision,
		"NDK_INSTALL_UNAME_S=" + opts.unameS,
		"NDK_INSTALL_UNAME_M=" + opts.unameM,
	}
	if opts.destFromRunnerTemp {
		env = append(env, "RUNNER_TEMP="+destShell)
	}
	for _, name := range []string{"curl", "sleep", "sha256sum", "unzip", "uname"} {
		requireShellCommandPath(t, bash, root, env, name, binPath+"/"+name)
	}

	script := `PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc scripts/ci/install-android-ndk.sh "$@"`
	args := []string{"--noprofile", "--norc", "-c", script, "script-test"}
	if !opts.destFromRunnerTemp && !opts.noDest {
		args = append(args, destShell)
	}
	cmd := exec.Command(bash, args...)
	cmd.Dir = root
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	githubEnv, err := os.ReadFile(logs["github-env"])
	if err != nil {
		t.Fatal(err)
	}
	return installAndroidNDKRun{
		stdout:           stdout.String(),
		stderr:           stderr.String(),
		githubEnv:        string(githubEnv),
		dest:             dest,
		destShell:        destShell,
		androidHomeShell: androidHomeShell,
		curlCalls:        readCallLines(t, logs["curl"]),
		sleepCalls:       readCallLines(t, logs["sleep"]),
		unzipCalls:       readCallLines(t, logs["unzip"]),
		err:              runErr,
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
