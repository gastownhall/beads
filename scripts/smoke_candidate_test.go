package scripts_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSmokeCandidateContract(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	baseEnv := append(shellPathEnv(), "GOFLAGS=-trimpath")
	root := shellPathUnderEnv(t, bash, sourceRepoRoot(t), baseEnv)
	flags := exec.Command(bash, "--noprofile", "--norc", "-c", `source "$1/.buildflags" || exit $?; printf '%s' "$GOFLAGS"`, "flags", root)
	flags.Env = baseEnv
	wantFlags, err := flags.Output()
	if err != nil {
		t.Fatal(err)
	}
	type candidateCase struct {
		name, override, cgo   string
		buildExit, wantExit   int
		partial, cleanupFails bool
	}
	cases := []candidateCase{
		{"relative prebuilt", "relative", "", 93, 0, false, false},
		{"absolute prebuilt", "absolute", "", 93, 0, false, false},
		{"missing prebuilt", "missing", "", 93, 2, false, false},
		{"directory prebuilt", "directory", "", 93, 2, false, false},
		{"unset builds", "unset", "", 0, 0, false, false},
		{"empty builds", "", "", 0, 0, false, false},
		{"explicit nocgo", "", "0", 0, 0, false, false},
		{"compiler failure", "", "", 23, 23, false, false},
		{"compiler partial output", "", "", 23, 23, true, false},
		{"compiler cleanup failure", "", "", 23, 23, true, true},
	}
	// Windows does not provide the POSIX executable-bit refusal boundary.
	if runtime.GOOS != "windows" {
		cases = append(cases, candidateCase{"nonexecutable prebuilt", "nonexecutable", "", 93, 2, false, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, cache := filepath.Join(dir, "tools"), filepath.Join(dir, "cache with spaces")
			for _, path := range []string{bin, cache} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(cache, "keep"), []byte("unrelated cache entry"), 0600); err != nil {
				t.Fatal(err)
			}
			const fakeGo = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$GOFLAGS" "$CGO_ENABLED" "$PWD" "$@" > "$BEADS_CANDIDATE_LOG"
if [ "$BEADS_CANDIDATE_EXIT" = 0 ] || [ "$BEADS_CANDIDATE_PARTIAL" = true ]; then
    [ "$1" = build ] && [ "$2" = -o ] || exit 92
    printf 'owned candidate' > "$3"
fi
exit "$BEADS_CANDIDATE_EXIT"
`
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0755); err != nil {
				t.Fatal(err)
			}
			if tc.cleanupFails {
				const fakeRM = "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$BEADS_CANDIDATE_RM_LOG\"\nexit 47\n"
				if err := os.WriteFile(filepath.Join(bin, "rm"), []byte(fakeRM), 0755); err != nil {
					t.Fatal(err)
				}
			}
			prebuilt := filepath.Join(dir, "candidate with spaces")
			if err := os.WriteFile(prebuilt, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			if tc.override == "nonexecutable" {
				if err := os.Chmod(prebuilt, 0644); err != nil {
					t.Fatal(err)
				}
			}
			dirPath := shellPathUnderEnv(t, bash, dir, baseEnv)
			binPath := shellPathUnderEnv(t, bash, bin, baseEnv)
			cachePath := shellPathUnderEnv(t, bash, cache, baseEnv)
			prebuiltPath := dirPath + "/candidate with spaces"
			env := append([]string{}, baseEnv...)
			commandPath := binPath
			for _, entry := range baseEnv {
				if strings.HasPrefix(entry, "PATH=") {
					commandPath += ":" + strings.TrimPrefix(entry, "PATH=")
				}
			}
			env = append(env, "BEADS_TEST_COMMAND_PATH="+commandPath, "PROJECT_ROOT="+root,
				"CACHE_DIR="+cachePath, "BEADS_CANDIDATE_LOG="+dirPath+"/calls", "BEADS_CANDIDATE_RM_LOG="+dirPath+"/rm-calls")
			env = append(env, "BEADS_CANDIDATE_EXIT="+strconv.Itoa(tc.buildExit), "BEADS_CANDIDATE_PARTIAL="+strconv.FormatBool(tc.partial))
			if tc.cgo != "" {
				env = append(env, "CGO_ENABLED="+tc.cgo)
			}
			value := ""
			switch tc.override {
			case "relative":
				value = "candidate with spaces"
			case "absolute", "nonexecutable":
				value = prebuiltPath
			case "missing":
				value = dirPath + "/missing"
			case "directory":
				value = cachePath
			}
			if tc.override != "unset" {
				env = append(env, "CANDIDATE_BIN="+value)
			}
			requireShellCommandPath(t, bash, dir, env, "go", binPath+"/go")
			if tc.cleanupFails {
				requireShellCommandPath(t, bash, dir, env, "rm", binPath+"/rm")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", `
PATH="$BEADS_TEST_COMMAND_PATH"; export PATH
source "$PROJECT_ROOT/scripts/lib/smoke-candidate.sh" || exit $?
[ "$GOFLAGS" = -trimpath ] || exit 91
candidate=$(build_candidate); status=$?
printf '%s' "$candidate"
exit "$status"
`)
			cmd.Dir, cmd.Env = dir, env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("exit = %v, want %d: %s", err, tc.wantExit, &stderr)
			}
			if tc.wantExit != 0 && len(out) != 0 {
				t.Fatalf("failed selection emitted a path: %q", out)
			}
			if got, err := os.ReadFile(filepath.Join(cache, "keep")); err != nil || string(got) != "unrelated cache entry" {
				t.Fatalf("unrelated cache entry changed: %q, %v", got, err)
			}
			calls, callErr := os.ReadFile(filepath.Join(dir, "calls"))
			if value != "" {
				if !os.IsNotExist(callErr) {
					t.Fatalf("prebuilt selection invoked go: %s, %v", calls, callErr)
				}
				if tc.wantExit == 0 && string(out) != prebuiltPath {
					t.Fatalf("selected %q, want %q", out, prebuiltPath)
				}
				if tc.wantExit == 2 && !strings.Contains(stderr.String(), "CANDIDATE_BIN must name an executable file") {
					t.Fatalf("missing explicit override diagnostic: %s", &stderr)
				}
				return
			}
			fields := strings.Split(strings.TrimSpace(string(calls)), "\n")
			cgo := tc.cgo
			if cgo == "" {
				cgo = "1"
			}
			if callErr != nil || len(fields) != 7 || fields[0] != string(wantFlags) || fields[1] != cgo || fields[2] != root ||
				fields[3] != "build" || fields[4] != "-o" || !strings.HasPrefix(fields[5], cachePath+"/bd-candidate-") || fields[6] != "./cmd/bd" {
				t.Fatalf("unexpected compiler call: %q, %v", calls, callErr)
			}
			if tc.wantExit == 0 && string(out) != fields[5] {
				t.Fatalf("returned %q, built %q", out, fields[5])
			}
			if tc.cleanupFails {
				got, err := os.ReadFile(filepath.Join(dir, "rm-calls"))
				if err != nil || string(got) != "-f\n--\n"+fields[5]+"\n" {
					t.Fatalf("cleanup did not target only the attempted output: %q, %v", got, err)
				}
			}
			entries, err := os.ReadDir(cache)
			wantFiles := 1 // The unrelated cache entry always survives.
			if tc.buildExit == 0 || tc.cleanupFails {
				wantFiles++
			}
			if err != nil || len(entries) != wantFiles {
				t.Fatalf("compiler fixture outputs = %v, want %d: %v", entries, wantFiles, err)
			}
		})
	}
}

func TestSmokeCandidateEntrypointOrdering(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	baseEnv := shellPathEnv()
	root := shellPathUnderEnv(t, bash, sourceRepoRoot(t), baseEnv)
	for _, tc := range []struct {
		name, script, override string
		wantExit               int
	}{
		{"upgrade invalid", "upgrade-smoke-test.sh", "missing", 2},
		{"cross invalid", "cross-version-smoke-test.sh", "missing", 2},
		{"upgrade download failure automatic", "upgrade-smoke-test.sh", "", 1},
		{"upgrade download failure prebuilt", "upgrade-smoke-test.sh", "prebuilt", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, home := filepath.Join(dir, "tools"), filepath.Join(dir, "home")
			for _, path := range []string{bin, home} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			const forbidden = "#!/usr/bin/env bash\nprintf '%s\\n' \"$0 $*\" >> \"$BEADS_CANDIDATE_FORBIDDEN\"\nexit 97\n"
			tools := []string{"curl", "wget", "go", "git", "dolt"}
			for _, tool := range tools {
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(forbidden), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.wantExit == 1 {
				const failedDownload = "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$BEADS_CANDIDATE_DOWNLOAD\"\nexit 63\n"
				if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(failedDownload), 0755); err != nil {
					t.Fatal(err)
				}
			}
			const prebuiltBytes = "#!/bin/sh\nexit 0\n"
			prebuilt := filepath.Join(dir, "provided candidate")
			if err := os.WriteFile(prebuilt, []byte(prebuiltBytes), 0755); err != nil {
				t.Fatal(err)
			}
			dirPath := shellPathUnderEnv(t, bash, dir, baseEnv)
			binPath := shellPathUnderEnv(t, bash, bin, baseEnv)
			homePath := shellPathUnderEnv(t, bash, home, baseEnv)
			candidate := ""
			if tc.override == "missing" {
				candidate = dirPath + "/missing candidate"
			} else if tc.override == "prebuilt" {
				candidate = dirPath + "/provided candidate"
			}
			commandPath := binPath
			for _, entry := range baseEnv {
				if strings.HasPrefix(entry, "PATH=") {
					commandPath += ":" + strings.TrimPrefix(entry, "PATH=")
				}
			}
			// The isolated HOME has no cached release; no inherited version loop is present.
			env := append(append([]string{}, baseEnv...), "HOME="+homePath, "TMPDIR="+dirPath,
				"BEADS_TEST_COMMAND_PATH="+commandPath, "CANDIDATE_BIN="+candidate,
				"BEADS_CANDIDATE_FORBIDDEN="+dirPath+"/forbidden", "BEADS_CANDIDATE_DOWNLOAD="+dirPath+"/download")
			for _, tool := range tools {
				requireShellCommandPath(t, bash, dir, env, tool, binPath+"/"+tool)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// An explicit version reaches download if the caller loses its refusal guard.
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", `
PATH="$BEADS_TEST_COMMAND_PATH"; export PATH
exec "$BASH" --noprofile --norc "$1" v0.62.0
`, "candidate-entrypoint", root+"/scripts/"+tc.script)
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("entrypoint exit = %v, want %d: %s", err, tc.wantExit, out)
			}
			if tc.wantExit == 2 && !bytes.Contains(out, []byte("CANDIDATE_BIN must name an executable file:")) {
				t.Fatalf("missing invalid-override diagnostic: %s", out)
			}
			if calls, err := os.ReadFile(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
				t.Fatalf("entrypoint reached a forbidden compiler/downloader/database tool: %s, %v", calls, err)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "download"))
			if tc.wantExit == 1 {
				if err != nil || bytes.Count(calls, []byte{'\n'}) != 1 || !bytes.Contains(out, []byte("Failed to download")) {
					t.Fatalf("previous-release failure was not exercised: %q, %v: %s", calls, err, out)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("invalid override attempted a download: %q, %v", calls, err)
			}
			if got, err := os.ReadFile(prebuilt); err != nil || string(got) != prebuiltBytes {
				t.Fatalf("prebuilt input changed: %q, %v", got, err)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".cache", "beads-regression"))
			if (err != nil && !os.IsNotExist(err)) || len(entries) != 0 {
				t.Fatalf("failed entrypoint left cached output: %v, %v", entries, err)
			}
		})
	}
}

func TestSmokeCandidateLibraryImport(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	env := shellPathEnv()
	root := shellPathUnderEnv(t, bash, sourceRepoRoot(t), env)
	env = append(env, "HOME="+shellPathUnderEnv(t, bash, t.TempDir(), env), "GOFLAGS=-trimpath")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// legacy-bridge-test.sh imports verification functions without PROJECT_ROOT.
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-uc", `
source "$1/scripts/migration-test/lib/binary.sh" || exit $?
[ "$GOFLAGS" = -trimpath ] && [ -z "${CGO_ENABLED+x}" ] || exit 91
declare -f verify_release_binary_version >/dev/null && declare -f build_candidate >/dev/null
`, "library-import", root)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("historical library import: %v\n%s", err, out)
	}
}
