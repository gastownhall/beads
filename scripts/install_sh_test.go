package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// install_from_release must put bd on the live path the way `make install`
// has since #5611: copy the extracted binary to a temp name INSIDE the
// destination directory, then mv -f it over bd, so the final move is a
// same-filesystem rename(2). What #5616 filed instead moves the mktemp -d
// work straight onto $install_dir/bd, which degrades to a copy onto the
// live path whenever /tmp is on another filesystem — a concurrent bd exec
// in that window sees a partial binary (the 2026-08-08 rc=137 class).

const (
	// The stubbed release archive extracts installShNewPayload; the live
	// path is seeded with installShOldPayload so a test can tell a real
	// install from a no-op.
	installShNewPayload = "install-from-release-new\n"
	installShOldPayload = "pre-existing-old\n"
	// The stubbed latest release. install_from_release derives the archive
	// name from the version and platform, so these must line up with the
	// asset grep in release_has_asset and the checksums lookup.
	installShVersion     = "v9.9.9"
	installShArchiveName = "beads_9.9.9_linux_amd64.tar.gz"
)

type installShRun struct {
	status     int
	output     string
	callLog    []string
	installDir string
}

// installShSourcedLib returns a drivable copy of scripts/install.sh: the
// trailing main invocation is stripped (the tests call install_from_release
// as a function and must never run main, whose fallback ladder ends in a
// real `go install`), and both install-location blocks are pointed at
// $INSTALL_TEST_DIR, a test-owned directory. [[ -w /usr/local/bin ]] is a
// bash builtin, so no PATH stub can steer it; without the rewrite a root
// executor would install into the real /usr/local/bin.
func installShSourcedLib(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var body []string
	mainCalls := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == `main "$@"` {
			mainCalls++
			continue
		}
		body = append(body, line)
	}
	if mainCalls != 1 {
		t.Fatalf("scripts/install.sh has %d `main \"$@\"` lines, want exactly 1", mainCalls)
	}

	var out []string
	blocks := 0
	for i := 0; i < len(body); i++ {
		if strings.TrimSpace(body[i]) != "if [[ -w /usr/local/bin ]]; then" {
			out = append(out, body[i])
			continue
		}
		if i+6 > len(body) {
			t.Fatalf("scripts/install.sh install-location block at line %d runs into EOF", i+1)
		}
		want := []string{
			`install_dir="/usr/local/bin"`,
			"else",
			`install_dir="$HOME/.local/bin"`,
			`mkdir -p "$install_dir"`,
			"fi",
		}
		for j, wantLine := range want {
			if strings.TrimSpace(body[i+1+j]) != wantLine {
				t.Fatalf("scripts/install.sh has an unexpected install-location block at line %d:\n%s",
					i+1, strings.Join(body[i:i+6], "\n"))
			}
		}
		indent := body[i][:len(body[i])-len(strings.TrimLeft(body[i], " \t"))]
		out = append(out,
			indent+`install_dir="$INSTALL_TEST_DIR"`,
			indent+`mkdir -p "$install_dir"`)
		i += 5
		blocks++
	}
	if blocks != 2 {
		t.Fatalf("rewrote %d install-location blocks in scripts/install.sh, want 2", blocks)
	}

	lib := filepath.Join(t.TempDir(), "install-lib.sh")
	if err := os.WriteFile(lib, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return lib
}

// runInstallFromRelease drives the sourced install.sh copy through
// install_from_release linux_amd64 with the network, archive and platform
// stubbed, and returns its exit status, combined output, and the shared
// call log the cp/mv/sudo stubs append every command to.
func runInstallFromRelease(t *testing.T) installShRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Unix installer; Windows installs via install.ps1")
	}
	bash := requireHostTool(t, "bash")
	realCP := requireHostTool(t, "cp")
	realMV := requireHostTool(t, "mv")

	lib := installShSourcedLib(t)

	home := t.TempDir()
	tmpEnv := t.TempDir()
	stateDir := t.TempDir()
	binDir := t.TempDir()
	installDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "bd"), []byte(installShOldPayload), 0o755); err != nil {
		t.Fatal(err)
	}

	// The fake archive never needs to be a real tarball: tar is stubbed to
	// extract a known payload, and the checksum install.sh verifies is of
	// the archive bytes themselves.
	archive := []byte("install.sh test archive for #5616\n")
	archivePath := filepath.Join(stateDir, "archive")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + installShArchiveName + "\n"
	payloadPath := filepath.Join(stateDir, "payload")
	if err := os.WriteFile(payloadPath, []byte(installShNewPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseJSON := `{"tag_name": "` + installShVersion + `", "assets": [` +
		`{"name": "` + installShArchiveName + `"}, {"name": "checksums.txt"}]}` + "\n"

	callLog := filepath.Join(stateDir, "calls")
	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(stateDir, "status")

	// curl answers the latest-release query with the JSON above and serves
	// the archive and checksums for -o downloads; tar extracts a known
	// executable payload in place of the (fake) archive.
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
set -eu
out=
url=
prev=
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out=$arg; fi
  case $arg in https://*) url=$arg ;; esac
  prev=$arg
done
if [ -z "$out" ]; then
  printf '%s\n' "$INSTALL_SH_RELEASE_JSON"
  exit 0
fi
case $url in
  *checksums.txt) printf '%s' "$INSTALL_SH_CHECKSUMS" >"$out" ;;
  *) cat "$INSTALL_SH_ARCHIVE" >"$out" ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "tar"), `#!/bin/sh
set -eu
cat "$INSTALL_SH_PAYLOAD" > bd
chmod 0755 bd
`)
	writeExecutable(t, filepath.Join(binDir, "uname"), `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
`)
	// cp and mv wrap (do not replace) the real tools: every call lands in
	// one shared log so the tests can assert on the exact install recipe.
	writeExecutable(t, filepath.Join(binDir, "cp"), `#!/bin/sh
set -eu
printf '%s\n' "cp $*" >>"$INSTALL_SH_CALL_LOG"
exec "$REAL_CP" "$@"
`)
	writeExecutable(t, filepath.Join(binDir, "mv"), `#!/bin/sh
set -eu
printf '%s\n' "mv $*" >>"$INSTALL_SH_CALL_LOG"
exec "$REAL_MV" "$@"
`)

	// The driver sources the copy (so the file's own set -e applies), then
	// calls install_from_release the way main does — inside a condition,
	// with errexit off — and records the return status. main is never run,
	// so no go stub is needed.
	driver := filepath.Join(stateDir, "driver.sh")
	if err := os.WriteFile(driver, []byte(`set -e
source "$INSTALL_SH_LIB"
set +e
install_from_release linux_amd64
printf '%s\n' "$?" >"$INSTALL_SH_STATUS"
exit 0
`), 0o600); err != nil {
		t.Fatal(err)
	}

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, binDir, pathEnv)
	commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + commandPath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"HOME=" + home,
		"TMPDIR=" + tmpEnv,
		"INSTALL_SH_LIB=" + lib,
		"INSTALL_SH_STATUS=" + statusFile,
		"INSTALL_SH_CALL_LOG=" + callLog,
		"INSTALL_SH_RELEASE_JSON=" + releaseJSON,
		"INSTALL_SH_CHECKSUMS=" + checksums,
		"INSTALL_SH_ARCHIVE=" + archivePath,
		"INSTALL_SH_PAYLOAD=" + payloadPath,
		"INSTALL_TEST_DIR=" + installDir,
		"REAL_CP=" + realCP,
		"REAL_MV=" + realMV,
	}
	for _, name := range []string{"curl", "tar", "uname", "cp", "mv"} {
		requireShellCommandPath(t, bash, stateDir, env, name, binPath+"/"+name)
	}

	cmd := bashScriptCommand(bash, driver)
	cmd.Dir = stateDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("driver failed: %v\n%s", err, output)
	}
	statusData, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("driver recorded no status: %v\n%s", err, output)
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(statusData)))
	if err != nil {
		t.Fatalf("driver status %q: %v", statusData, err)
	}
	return installShRun{
		status:     status,
		output:     string(output),
		callLog:    readCallLines(t, callLog),
		installDir: installDir,
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stagedInstallPair finds the #5611 install recipe in the call log —
// exactly one copy of the extracted binary onto
// <installDir>/.bd.install.tmp.<pid> followed by one mv -f of that staged
// temp over bd — and returns the rename call. prefix distinguishes the
// plain branch ("" — logged by the cp/mv wrappers) from the sudo branch
// ("sudo " — logged by the sudo stub).
func stagedInstallPair(t *testing.T, log []string, prefix, installDir string) string {
	t.Helper()
	stagedPrefix := filepath.Join(installDir, ".bd.install.tmp.")
	target := filepath.Join(installDir, "bd")
	copyIdx, renameIdx := -1, -1
	staged := ""
	for i, call := range log {
		if !strings.HasPrefix(call, prefix+"cp ") && !strings.HasPrefix(call, prefix+"mv ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(call, prefix))
		switch {
		case fields[0] == "cp" && len(fields) == 3:
			if !strings.HasPrefix(fields[2], stagedPrefix) || !isAllDigits(fields[2][len(stagedPrefix):]) {
				continue
			}
			if copyIdx >= 0 {
				t.Fatalf("two staged copies in the call log:\n%s", strings.Join(log, "\n"))
			}
			if strings.HasPrefix(fields[1], installDir+string(os.PathSeparator)) {
				t.Fatalf("staged copy source %s is inside the install dir; want the extracted binary from the mktemp work dir", fields[1])
			}
			copyIdx, staged = i, fields[2]
		case fields[0] == "mv" && len(fields) == 4 && fields[1] == "-f" && fields[2] == staged && fields[3] == target:
			if renameIdx >= 0 {
				t.Fatalf("two renames onto bd in the call log:\n%s", strings.Join(log, "\n"))
			}
			renameIdx = i
		}
	}
	if copyIdx < 0 {
		t.Fatalf("no %sstaged copy onto %s<pid> in the call log:\n%s", prefix, stagedPrefix, strings.Join(log, "\n"))
	}
	if renameIdx < 0 {
		t.Fatalf("no %smv -f of %s onto bd in the call log:\n%s", prefix, staged, strings.Join(log, "\n"))
	}
	if renameIdx < copyIdx {
		t.Fatalf("rename logged before the staged copy:\n%s", strings.Join(log, "\n"))
	}
	return log[renameIdx]
}

// TestInstallShStagesAndRenamesIntoPlace is AC-1 of #5616: with a writable
// install dir, install_from_release must stage the extracted release binary
// inside the destination directory and rename it over bd, and nothing may
// copy or move the mktemp-dir binary onto the live path, where a
// cross-filesystem mv leaves bd partial for the whole copy window.
func TestInstallShStagesAndRenamesIntoPlace(t *testing.T) {
	run := runInstallFromRelease(t)

	if run.status != 0 {
		t.Fatalf("install_from_release exited %d, want 0\n%s", run.status, run.output)
	}
	target := filepath.Join(run.installDir, "bd")

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("installed bd missing: %v\n%s", err, run.output)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed bd mode %v carries no execute bit", info.Mode().Perm())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read installed bd: %v", err)
	}
	if string(got) != installShNewPayload {
		t.Errorf("installed bd holds %q, want the extracted payload %q", got, installShNewPayload)
	}

	rename := stagedInstallPair(t, run.callLog, "", run.installDir)
	for _, call := range run.callLog {
		fields := strings.Fields(call)
		if len(fields) < 2 || fields[len(fields)-1] != target || call == rename {
			continue
		}
		t.Errorf("command %q writes the live bd; only the staged rename\n  %s\nmay touch it", call, rename)
	}

	if leftovers, _ := filepath.Glob(filepath.Join(run.installDir, ".bd.install.tmp.*")); len(leftovers) > 0 {
		t.Errorf("successful install left staged temps behind: %v", leftovers)
	}
}
