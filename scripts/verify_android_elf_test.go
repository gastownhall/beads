package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// androidReadelfHeaders is `readelf -lW` of an NDK r30, API-21 bd-android-arm64
// build, trimmed to the headers verify-android-elf.sh reads.
const androidReadelfHeaders = `
Elf file type is DYN (Position-Independent Executable file)
Entry point 0x1c39000
There are 10 program headers, starting at offset 64

Program Headers:
  Type           Offset   VirtAddr           PhysAddr           FileSiz  MemSiz   Flg Align
  PHDR           0x000040 0x0000000000000040 0x0000000000000040 0x000230 0x000230 R   0x8
  INTERP         0x000270 0x0000000000000270 0x0000000000000270 0x000015 0x000015 R   0x1
      [Requesting program interpreter: /system/bin/linker64]
  LOAD           0x000000 0x0000000000000000 0x0000000000000000 0x1c38a48 0x1c38a48 R   0x4000
  LOAD           0x1c38a48 0x0000000001c3ca48 0x0000000001c3ca48 0x2a1f350 0x2a1f350 R E 0x4000
  LOAD           0x4657da0 0x000000000465fda0 0x000000000465fda0 0x0c3b260 0x0d4ff38 RW  0x4000
  DYNAMIC        0x52923a0 0x000000000529a3a0 0x000000000529a3a0 0x0001d0 0x0001d0 RW  0x8
  GNU_STACK      0x000000 0x0000000000000000 0x0000000000000000 0x000000 0x000000 RW  0x0
  NOTE           0x0002a8 0x00000000000002a8 0x00000000000002a8 0x000098 0x000098 R   0x4
`

// TestVerifyAndroidELF pins the release post hook that proves
// bd-android-arm64 is a Bionic ELF (#3538). verify-cgo.sh alone would accept
// a binary linked by the host glibc toolchain, and a missing readelf must fail
// rather than skip: a skip reports an unchecked binary as verified.
func TestVerifyAndroidELF(t *testing.T) {
	glibc := strings.Replace(androidReadelfHeaders, "/system/bin/linker64", "/lib/ld-linux-aarch64.so.1", 1)
	noInterp := strings.Replace(androidReadelfHeaders, "      [Requesting program interpreter: /system/bin/linker64]\n", "", 1)
	// 4 KB pages: what NDK r27 (the runner image's ANDROID_NDK_HOME) links.
	fourKB := strings.Replace(androidReadelfHeaders, "R E 0x4000", "R E 0x1000", 1)
	noLoad := strings.ReplaceAll(androidReadelfHeaders, "  LOAD ", "  XLOAD ")

	tests := []struct {
		name        string
		args        []string
		headers     string
		noReadelf   bool
		missingFile bool
		wantExit    int
		wantOutput  string
	}{
		{name: "android ELF", headers: androidReadelfHeaders, wantExit: 0, wantOutput: "OK: "},
		{name: "glibc interpreter", headers: glibc, wantExit: 1, wantOutput: "program interpreter '/lib/ld-linux-aarch64.so.1', want /system/bin/linker64"},
		{name: "no interpreter", headers: noInterp, wantExit: 1, wantOutput: "program interpreter 'none', want /system/bin/linker64"},
		{name: "4 KB PT_LOAD alignment", headers: fourKB, wantExit: 1, wantOutput: "PT_LOAD segment aligned to 0x1000, want at least 0x4000"},
		{name: "no PT_LOAD", headers: noLoad, wantExit: 1, wantOutput: "no PT_LOAD segment"},
		{name: "readelf not on PATH", headers: androidReadelfHeaders, noReadelf: true, wantExit: 1, wantOutput: "'readelf' is required"},
		{name: "binary missing", headers: androidReadelfHeaders, missingFile: true, wantExit: 1, wantOutput: "binary not found"},
		{name: "no argument", args: []string{}, wantExit: 2, wantOutput: "Usage:"},
		{name: "two arguments", args: []string{"a", "b"}, wantExit: 2, wantOutput: "Usage:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runVerifyAndroidELF(t, test.args, test.headers, test.noReadelf, test.missingFile)
			if got := exitCode(run.err); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", got, test.wantExit, run.err, run.output)
			}
			if !strings.Contains(run.output, test.wantOutput) {
				t.Fatalf("output does not contain %q:\n%s", test.wantOutput, run.output)
			}
			if test.wantExit == 0 {
				want := []string{"-lW " + run.binary}
				if len(run.readelfCalls) != 1 || run.readelfCalls[0] != want[0] {
					t.Fatalf("readelf calls = %q, want %q", run.readelfCalls, want)
				}
			}
		})
	}
}

type verifyAndroidELFRun struct {
	output       string
	binary       string
	readelfCalls []string
	err          error
}

// runVerifyAndroidELF runs the script with PATH holding only a fake readelf
// (or nothing), so the host's binutils can neither satisfy nor mask a check.
func runVerifyAndroidELF(t *testing.T, args []string, headers string, noReadelf, missingFile bool) verifyAndroidELFRun {
	t.Helper()

	bash := requireHostTool(t, "bash")
	bin := t.TempDir()
	stateDir := t.TempDir()
	readelfLog := filepath.Join(stateDir, "readelf-calls")
	if err := os.WriteFile(readelfLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(stateDir, "bd")
	if !missingFile {
		if err := os.WriteFile(binary, []byte("not really an ELF"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !noReadelf {
		writeExecutable(t, filepath.Join(bin, "readelf"), `#!/bin/sh
printf '%s\n' "$*" >>"$VERIFY_ANDROID_ELF_READELF_LOG"
printf '%s' "$VERIFY_ANDROID_ELF_HEADERS"
`)
	}

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
	statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
	shellBinary := statePath + "/bd"
	if args == nil {
		args = []string{shellBinary}
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + binPath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"VERIFY_ANDROID_ELF_READELF_LOG=" + statePath + "/readelf-calls",
		"VERIFY_ANDROID_ELF_HEADERS=" + headers,
	}

	cmdArgs := append([]string{"--noprofile", "--norc", "-c",
		`PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc "$@"`,
		"script-test", "scripts/verify-android-elf.sh"}, args...)
	cmd := exec.Command(bash, cmdArgs...)
	cmd.Dir = sourceRepoRoot(t)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return verifyAndroidELFRun{
		output:       string(output),
		binary:       shellBinary,
		readelfCalls: readCallLines(t, readelfLog),
		err:          err,
	}
}
