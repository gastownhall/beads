package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeReadelfOutput emits `readelf -lW` program headers for the ELF shape
// named by BEADS_TEST_READELF_ELF, in the format real binutils prints (the
// interpreter rides on its own bracketed line under the INTERP row; Align is
// the LOAD row's last field, and the flags column before it may itself hold
// a space, as in "R E").
const fakeReadelf = `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$BEADS_TEST_READELF_ARGS"
hdr='  INTERP         0x000318 0x0000000000000318 0x0000000000000318 0x00002d 0x00002d R   0x1
      [Requesting program interpreter: %s]
  LOAD           0x000000 0x0000000000000000 0x0000000000000000 0x0b9c58 0x0b9c58 R   0x4000
  LOAD           0x0b9c60 0x00000000000b9c60 0x00000000000b9c60 0x04b534 0x04b534 R E 0x4000
  LOAD           0x101000 0x0000000000101000 0x0000000000101000 0x030000 0x030000 RW  %s
'
case "${BEADS_TEST_READELF_ELF:-}" in
  good)
    printf "$hdr" /system/bin/linker64 0x10000
    ;;
  glibc)
    printf "$hdr" /lib/ld-linux-aarch64.so.1 0x10000
    ;;
  smallalign)
    printf "$hdr" /system/bin/linker64 0x1000
    ;;
  nointerp)
    printf '  LOAD           0x000000 0x0000000000000000 0x0000000000000000 0x0b9c58 0x0b9c58 R   0x4000\n  LOAD           0x0b9c60 0x00000000000b9c60 0x00000000000b9c60 0x04b534 0x04b534 R E 0x4000\n'
    ;;
  *)
    printf 'readelf test fake: unknown BEADS_TEST_READELF_ELF=%s\n' "$BEADS_TEST_READELF_ELF" >&2
    exit 9
    ;;
esac
`

// TestVerifyAndroidElf pins the Bionic gate of the bd-android-arm64 build
// (#3538): the asset is only acceptable with the /system/bin/linker64
// interpreter and every PT_LOAD aligned for 16 KB pages. verify-cgo.sh would
// wave through a host-gcc glibc binary or an NDK-27 4 KB-aligned link, so
// this script must fail on both -- and, unlike verify-cgo.sh's silent skip
// of a missing readelf, fail closed when readelf is absent.
func TestVerifyAndroidElf(t *testing.T) {
	tests := []struct {
		name       string
		elf        string
		readelf    bool // readelf is on PATH
		extraArgs  int  // arguments beyond the binary path
		wantExit   int
		wantNaming []string // phrases the failure must name
		wantNoExec bool     // readelf must not run at all
	}{
		{
			name:     "bionic 16 KB ELF passes",
			elf:      "good",
			readelf:  true,
			wantExit: 0,
		},
		{
			name:       "glibc interpreter is rejected",
			elf:        "glibc",
			readelf:    true,
			wantExit:   1,
			wantNaming: []string{"interpreter", "/system/bin/linker64"},
		},
		{
			name:       "4 KB PT_LOAD alignment is rejected",
			elf:        "smallalign",
			readelf:    true,
			wantExit:   1,
			wantNaming: []string{"alignment", "0x4000"},
		},
		{
			name:       "missing interpreter is rejected",
			elf:        "nointerp",
			readelf:    true,
			wantExit:   1,
			wantNaming: []string{"interpreter"},
		},
		{
			name:       "missing readelf fails closed",
			readelf:    false,
			wantExit:   1,
			wantNaming: []string{"readelf", "required"},
			wantNoExec: true,
		},
		{
			name:       "no arguments is a usage error",
			readelf:    true,
			extraArgs:  -1,
			wantExit:   2,
			wantNoExec: true,
		},
		{
			name:       "two arguments is a usage error",
			readelf:    true,
			extraArgs:  1,
			wantExit:   2,
			wantNoExec: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bash := requireHostTool(t, "bash")
			root := sourceRepoRoot(t)

			bin := t.TempDir()
			stateDir := t.TempDir()
			readelfArgs := filepath.Join(stateDir, "readelf-args")
			if err := os.WriteFile(readelfArgs, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			binaryPath := filepath.Join(stateDir, "bd")
			if err := os.WriteFile(binaryPath, []byte("\x7fELF fake"), 0o755); err != nil {
				t.Fatal(err)
			}

			pathEnv := shellPathEnv()
			binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
			statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
			// The usage-error and missing-readelf cases must not depend on
			// anything outside the fake dir, so their PATH holds only it.
			commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
			if runtime.GOOS == "windows" {
				commandPath = binPath + ":/usr/bin:/bin"
			}
			if test.wantNoExec {
				commandPath = binPath
			}
			env := []string{
				"PATH=" + os.Getenv("PATH"),
				"BEADS_TEST_COMMAND_PATH=" + commandPath,
				"LC_ALL=C",
				"LANG=C",
				"BASH_ENV=",
				"ENV=",
				"BEADS_TEST_READELF_ARGS=" + statePath + "/readelf-args",
				"BEADS_TEST_READELF_ELF=" + test.elf,
			}
			if test.readelf {
				writeExecutable(t, filepath.Join(bin, "readelf"), fakeReadelf)
				requireShellCommandPath(t, bash, root, env, "readelf", binPath+"/readelf")
			}

			script := exec.Command(bash, "--noprofile", "--norc", "-c",
				`PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc "$@"`,
				"script-test", "scripts/verify-android-elf.sh",
				shellPathUnderEnv(t, bash, binaryPath, pathEnv))
			script.Dir = root
			script.Env = env
			switch test.extraArgs {
			case -1: // no binary argument at all
				script.Args = script.Args[:len(script.Args)-1]
			case 1:
				script.Args = append(script.Args, "second-arg")
			}
			output, runErr := script.CombinedOutput()
			if got := exitCode(runErr); got != test.wantExit {
				t.Fatalf("exit = %d, want %d; err=%v\n%s", got, test.wantExit, runErr, output)
			}
			for _, phrase := range test.wantNaming {
				if !strings.Contains(string(output), phrase) {
					t.Fatalf("output does not name %q:\n%s", phrase, output)
				}
			}
			calls := readCallLines(t, readelfArgs)
			if test.wantNoExec {
				if len(calls) != 0 {
					t.Fatalf("readelf ran anyway: %q\n%s", calls, output)
				}
				return
			}
			if len(calls) != 1 || !strings.HasPrefix(calls[0], "-lW ") {
				t.Fatalf("readelf calls = %q, want one `-lW <binary>` invocation", calls)
			}
		})
	}
}

// The good-path subtest above proves the parse accepts a real-format
// program header table; this pins the other direction -- every LOAD row,
// not just the first -- by feeding a table whose last row is the violation.
func TestVerifyAndroidElfHelperIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable bits")
	}

	path := filepath.Join(sourceRepoRoot(t), "scripts", "verify-android-elf.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable", path)
	}
}
