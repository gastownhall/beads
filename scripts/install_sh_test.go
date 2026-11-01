package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	installShOldBD = "#!/bin/sh\necho old\n"
	installShNewBD = "#!/bin/sh\necho new\n"
)

// installShLocationBlock matches the install-location choice in
// scripts/install.sh. `[[ -w /usr/local/bin ]]` is a bash builtin, so it
// cannot be stubbed on PATH: the test copy swaps the block for a test-owned
// directory so a root executor never installs into the real /usr/local/bin.
var installShLocationBlock = regexp.MustCompile(`(?m)^([ \t]*)if \[\[ -w /usr/local/bin \]\]; then\n` +
	`[ \t]*install_dir="/usr/local/bin"\n` +
	`[ \t]*else\n` +
	`[ \t]*install_dir="\$HOME/\.local/bin"\n` +
	`[ \t]*mkdir -p "\$install_dir"\n` +
	`[ \t]*fi\n`)

type installShOptions struct {
	// readOnlyDir makes the install dir unwritable so install_from_release
	// takes its sudo branch.
	readOnlyDir bool
	// fail is "cp" or "mv": that wrapper fails the staged copy or the rename.
	fail string
	// umask, when set, is the umask install_from_release runs under.
	umask string
}

type installShRun struct {
	status     int
	output     string
	calls      [][]string
	pid        string
	extracted  string
	installDir string
}

// runInstallShRelease drives install_from_release from a temp copy of
// scripts/install.sh with stubbed curl/tar/uname/sudo and logging
// cp/chmod/mv wrappers. It sources the copy and calls the function directly:
// running main would fall back to `go install` from the network when the
// release install fails.
func runInstallShRelease(t *testing.T, opts installShOptions) installShRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/install.sh is the Unix installer")
	}
	if opts.readOnlyDir && os.Geteuid() == 0 {
		t.Skip("bash [[ -w ]] is true for root, so the sudo branch is never taken")
	}
	bash := requireHostTool(t, "bash")
	realCP := requireHostTool(t, "cp")
	realChmod := requireHostTool(t, "chmod")
	realMV := requireHostTool(t, "mv")

	bin := t.TempDir()
	fixtures := t.TempDir()
	state := t.TempDir()
	tmpRoot := t.TempDir()
	work := t.TempDir()
	installDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldBD := filepath.Join(installDir, "bd")
	if err := os.WriteFile(oldBD, []byte(installShOldBD), 0o755); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies this process's umask; requireBD wants exactly 0755.
	if err := os.Chmod(oldBD, 0o755); err != nil {
		t.Fatal(err)
	}
	if opts.readOnlyDir {
		if err := os.Chmod(installDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(installDir, 0o755) })
	}

	lib := filepath.Join(state, "install-lib.sh")
	if err := os.WriteFile(lib, []byte(installShTestCopy(t)), 0o600); err != nil {
		t.Fatal(err)
	}

	const version = "9.9.9"
	archiveName := "beads_" + version + "_linux_amd64.tar.gz"
	archive := []byte("fake release archive\n")
	sum := sha256.Sum256(archive)
	writeFixture := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fixtures, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("archive.tar.gz", string(archive))
	writeFixture("checksums.txt", hex.EncodeToString(sum[:])+"  "+archiveName+"\n")
	writeFixture("bd", installShNewBD)
	writeFixture("release.json", `{
  "tag_name": "v`+version+`",
  "assets": [
    {
      "name": "`+archiveName+`"
    },
    {
      "name": "checksums.txt"
    }
  ]
}
`)

	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
out=""
prev=""
url=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
  fi
  prev="$arg"
  url="$arg"
done
case "$url" in
  */releases/latest) cat "$INSTALL_TEST_FIXTURES/release.json" ;;
  */checksums.txt) cat "$INSTALL_TEST_FIXTURES/checksums.txt" >"$out" ;;
  *.tar.gz) cat "$INSTALL_TEST_FIXTURES/archive.tar.gz" >"$out" ;;
  *) echo "unexpected curl call: $*" >&2; exit 22 ;;
esac
`)
	// Extract a known executable bd into the working directory, the way the
	// real archive lays it out, and record where that was.
	writeExecutable(t, filepath.Join(bin, "tar"), `#!/bin/sh
set -eu
printf '%s\n' "$PWD" >"$INSTALL_TEST_STATE/tar-cwd"
cat "$INSTALL_TEST_FIXTURES/bd" >bd
"$INSTALL_TEST_REAL_CHMOD" 755 bd
`)
	writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
`)
	// The sudo stub grants the test-owned dir write access instead of
	// privilege for one command, then takes it back, so the real tools still
	// move bytes and a command run without sudo still meets a read-only dir.
	writeExecutable(t, filepath.Join(bin, "sudo"), `#!/bin/sh
{ printf 'sudo'; for a in "$@"; do printf '\t%s' "$a"; done; printf '\n'; } >>"$INSTALL_TEST_CALL_LOG"
"$INSTALL_TEST_REAL_CHMOD" u+w "$INSTALL_TEST_DIR"
"$@"
rc=$?
"$INSTALL_TEST_REAL_CHMOD" u-w "$INSTALL_TEST_DIR"
exit "$rc"
`)
	writeExecutable(t, filepath.Join(bin, "chmod"), `#!/bin/sh
{ printf 'chmod'; for a in "$@"; do printf '\t%s' "$a"; done; printf '\n'; } >>"$INSTALL_TEST_CALL_LOG"
exec "$INSTALL_TEST_REAL_CHMOD" "$@"
`)
	writeExecutable(t, filepath.Join(bin, "cp"), `#!/bin/sh
{ printf 'cp'; for a in "$@"; do printf '\t%s' "$a"; done; printf '\n'; } >>"$INSTALL_TEST_CALL_LOG"
if [ "${INSTALL_TEST_FAIL:-}" = cp ]; then
  for last in "$@"; do :; done
  case "$last" in
    */.bd.install.tmp.*) printf 'ne' >"$last"; exit 1 ;;
  esac
fi
exec "$INSTALL_TEST_REAL_CP" "$@"
`)
	writeExecutable(t, filepath.Join(bin, "mv"), `#!/bin/sh
{ printf 'mv'; for a in "$@"; do printf '\t%s' "$a"; done; printf '\n'; } >>"$INSTALL_TEST_CALL_LOG"
if [ "${INSTALL_TEST_FAIL:-}" = mv ]; then
  for last in "$@"; do :; done
  if [ "$last" = "$INSTALL_TEST_DIR/bd" ]; then
    exit 1
  fi
fi
exec "$INSTALL_TEST_REAL_MV" "$@"
`)

	callLog := filepath.Join(state, "calls")
	pidFile := filepath.Join(state, "pid")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + bin + ":" + os.Getenv("PATH") + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"TMPDIR=" + tmpRoot,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"INSTALL_TEST_DIR=" + installDir,
		"INSTALL_TEST_FIXTURES=" + fixtures,
		"INSTALL_TEST_STATE=" + state,
		"INSTALL_TEST_CALL_LOG=" + callLog,
		"INSTALL_TEST_REAL_CP=" + realCP,
		"INSTALL_TEST_REAL_CHMOD=" + realChmod,
		"INSTALL_TEST_REAL_MV=" + realMV,
		"INSTALL_TEST_FAIL=" + opts.fail,
		"INSTALL_TEST_UMASK=" + opts.umask,
	}
	for _, name := range []string{"curl", "tar", "uname", "sudo", "cp", "chmod", "mv"} {
		requireShellCommandPath(t, bash, work, env, name, bin+"/"+name)
	}

	// install.sh's own `set -e` runs while sourcing; production calls
	// `if install_from_release`, where errexit is off, so turn it off too.
	driver := `PATH="$BEADS_TEST_COMMAND_PATH"; export PATH
source "$1"
set +e
if [ -n "$INSTALL_TEST_UMASK" ]; then umask "$INSTALL_TEST_UMASK"; fi
printf '%s\n' "$$" >"$2"
install_from_release linux_amd64
`
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", driver, "install-sh-test", lib, pidFile)
	cmd.Dir = work
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	run := installShRun{output: string(out), installDir: installDir}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		run.status = exitErr.ExitCode()
	default:
		t.Fatalf("run install_from_release: %v\n%s", err, out)
	}

	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("driver did not record its pid: %v\n%s", err, out)
	}
	run.pid = strings.TrimSpace(string(pid))
	tarCwd, err := os.ReadFile(filepath.Join(state, "tar-cwd"))
	if err != nil {
		t.Fatalf("tar stub never ran: %v\n%s", err, out)
	}
	extractDir := strings.TrimSpace(string(tarCwd))
	run.extracted = extractDir + "/bd"
	// mktemp -d uses TMPDIR on Linux but the per-user temp dir on macOS, so
	// check only what holds on both: the archive was extracted outside the
	// install dir, into a dir install_from_release removed before returning.
	if extractDir == installDir || strings.HasPrefix(extractDir, installDir+"/") {
		t.Fatalf("archive extracted under %s, want a mktemp dir outside the install dir", extractDir)
	}
	if _, err := os.Lstat(extractDir); err == nil {
		t.Fatalf("install_from_release left its mktemp dir %s behind\n%s", extractDir, out)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(callLog); err == nil {
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line != "" {
				run.calls = append(run.calls, strings.Split(line, "\t"))
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return run
}

// installShTestCopy returns scripts/install.sh with its trailing `main "$@"`
// removed and the install-location choice pinned to $INSTALL_TEST_DIR.
func installShTestCopy(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(src), "\n")
	kept := lines[:0]
	mains := 0
	for _, line := range lines {
		if strings.TrimRight(line, "\n") == `main "$@"` {
			mains++
			continue
		}
		kept = append(kept, line)
	}
	if mains != 1 {
		t.Fatalf(`scripts/install.sh has %d lines equal to main "$@", want exactly 1`, mains)
	}
	body := strings.Join(kept, "")
	if n := len(installShLocationBlock.FindAllStringIndex(body, -1)); n != 2 {
		t.Fatalf("scripts/install.sh has %d install-location blocks, want 2 (install_from_release, build_from_source)", n)
	}
	return installShLocationBlock.ReplaceAllString(body, `${1}install_dir="$$INSTALL_TEST_DIR"; mkdir -p "$$install_dir"`+"\n")
}

// callsNaming returns the logged calls whose last argument is path.
func (r installShRun) callsNaming(path string) [][]string {
	var got [][]string
	for _, call := range r.calls {
		if len(call) > 1 && call[len(call)-1] == path {
			got = append(got, call)
		}
	}
	return got
}

func (r installShRun) staged() string {
	return filepath.Join(r.installDir, ".bd.install.tmp."+r.pid)
}

func (r installShRun) requireBD(t *testing.T, want string) {
	t.Helper()
	target := filepath.Join(r.installDir, "bd")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v\n%s", target, err, r.output)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q\n%s", target, got, want, r.output)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// 0755 is the archive's mode: every user must be able to run bd.
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("%s mode %#o, want 0755\n%s", target, got, r.output)
	}
}

func (r installShRun) requireNoStagedLeftovers(t *testing.T) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(r.installDir, ".bd.install.tmp.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("staged temp left in install dir: %q\n%s", left, r.output)
	}
}

func joinCalls(calls [][]string) string {
	var b strings.Builder
	for _, call := range calls {
		b.WriteString(strings.Join(call, " "))
		b.WriteString("\n")
	}
	return b.String()
}

// TestInstallShStagesAndRenamesIntoPlace is #5616: like `make install` since
// #5611, the curl installer must copy the release binary to a temp name inside
// the install dir and rename it over bd. Moving it straight out of the mktemp
// dir is a copy onto the live path whenever /tmp is another filesystem, so a
// concurrent exec can see a partial bd.
func TestInstallShStagesAndRenamesIntoPlace(t *testing.T) {
	run := runInstallShRelease(t, installShOptions{})
	if run.status != 0 {
		t.Fatalf("install_from_release returned %d\n%s", run.status, run.output)
	}

	target := filepath.Join(run.installDir, "bd")
	staged := run.staged()
	wantStage := []string{"cp", run.extracted, staged}
	wantMode := []string{"chmod", "0755", staged}
	wantRename := []string{"mv", "-f", staged, target}
	stage, mode, rename := -1, -1, -1
	for i, call := range run.calls {
		switch strings.Join(call, "\t") {
		case strings.Join(wantStage, "\t"):
			stage = i
		case strings.Join(wantMode, "\t"):
			mode = i
		case strings.Join(wantRename, "\t"):
			rename = i
		}
	}
	if stage < 0 || mode < stage || rename < mode {
		t.Fatalf("want %q, then %q, then %q; calls:\n%s%s", wantStage, wantMode, wantRename, joinCalls(run.calls), run.output)
	}
	if onto := run.callsNaming(target); len(onto) != 1 {
		t.Fatalf("calls writing %s = %q, want only the rename from %s", target, onto, staged)
	}
	for _, call := range run.calls {
		for _, arg := range call[1:] {
			if arg == run.extracted && strings.Join(call, "\t") != strings.Join(wantStage, "\t") {
				t.Fatalf("call %q uses the mktemp binary other than to stage it", call)
			}
		}
	}
	run.requireBD(t, installShNewBD)
	run.requireNoStagedLeftovers(t)
}

// TestInstallShSudoBranchStagesAndRenames covers an install dir the user
// cannot write: the stage, the chmod and the rename all run under sudo, with
// the same same-filesystem rename as the writable branch.
func TestInstallShSudoBranchStagesAndRenames(t *testing.T) {
	run := runInstallShRelease(t, installShOptions{readOnlyDir: true})
	if run.status != 0 {
		t.Fatalf("install_from_release returned %d\n%s", run.status, run.output)
	}

	target := filepath.Join(run.installDir, "bd")
	staged := run.staged()
	wantStage := strings.Join([]string{"sudo", "cp", run.extracted, staged}, "\t")
	wantMode := strings.Join([]string{"sudo", "chmod", "0755", staged}, "\t")
	wantRename := strings.Join([]string{"sudo", "mv", "-f", staged, target}, "\t")
	stage, mode, rename := -1, -1, -1
	for i, call := range run.calls {
		switch strings.Join(call, "\t") {
		case wantStage:
			stage = i
		case wantMode:
			mode = i
		case wantRename:
			rename = i
		}
	}
	if stage < 0 || mode < stage || rename < mode {
		t.Fatalf("want sudo cp to %s, sudo chmod 0755 of it, then sudo mv -f onto %s; calls:\n%s%s", staged, target, joinCalls(run.calls), run.output)
	}
	for _, call := range run.callsNaming(target) {
		if call[len(call)-2] != staged {
			t.Fatalf("call %q writes %s from somewhere other than %s", call, target, staged)
		}
	}
	run.requireBD(t, installShNewBD)
	run.requireNoStagedLeftovers(t)
}

// TestInstallShFailedStageLeavesOldBinaryIntact fails the staged copy (after
// it wrote a partial file) or the rename, in both branches: the install must
// fail, keep the previous bd, and leave no staged temp behind. In the sudo
// branch the dir is read-only again after each sudo command, so only a
// cleanup run under sudo removes the temp.
func TestInstallShFailedStageLeavesOldBinaryIntact(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts installShOptions
	}{
		{"cp", installShOptions{fail: "cp"}},
		{"mv", installShOptions{fail: "mv"}},
		{"sudo_cp", installShOptions{readOnlyDir: true, fail: "cp"}},
		{"sudo_mv", installShOptions{readOnlyDir: true, fail: "mv"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runInstallShRelease(t, tc.opts)
			if run.status == 0 {
				t.Fatalf("install_from_release returned 0 with a failing %s; calls:\n%s%s", tc.opts.fail, joinCalls(run.calls), run.output)
			}
			if want := "Failed to install bd to " + run.installDir; !strings.Contains(run.output, want) {
				t.Fatalf("output lacks %q\n%s", want, run.output)
			}
			run.requireBD(t, installShOldBD)
			run.requireNoStagedLeftovers(t)
		})
	}
}

// TestInstallShInstalledBinaryIsWorldExecutable runs both branches under
// umask 077. The old mv kept the mode tar gave the binary (the archive's 0755
// when root runs it), but cp creates the staged copy under the caller's
// umask, so the installer must set 0755 itself or a root or sudo install
// leaves a bd other users cannot run.
func TestInstallShInstalledBinaryIsWorldExecutable(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts installShOptions
	}{
		{"writable", installShOptions{umask: "077"}},
		{"sudo", installShOptions{readOnlyDir: true, umask: "077"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runInstallShRelease(t, tc.opts)
			if run.status != 0 {
				t.Fatalf("install_from_release returned %d\n%s", run.status, run.output)
			}
			run.requireBD(t, installShNewBD)
			run.requireNoStagedLeftovers(t)
		})
	}
}
