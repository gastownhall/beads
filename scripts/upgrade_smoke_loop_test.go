package scripts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUpgradeSmokeMultiVersionDispatch(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, candidate, failVersion string
		wantExit                     int
	}{
		{"prebuilt", "candidate with spaces/bd", "", 0},
		{"automatic", "", "", 0},
		{"continues after failure", "candidate with spaces/bd", "v0.61.0", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const scriptPath = "scripts/upgrade-smoke-test.sh"
			const candidateHelper = "scripts/lib/smoke-candidate.sh"
			script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), scriptPath))
			if err != nil {
				t.Fatal(err)
			}
			shebang, body, ok := strings.Cut(string(script), "\n")
			if !ok || !strings.HasPrefix(shebang, "#!") {
				t.Fatal("upgrade smoke entrypoint must start with a shebang")
			}
			// Observe the child's script body, where Bash 3.2 has assigned positional arguments.
			// The actual dispatch body below this insertion stays byte-for-byte unchanged.
			const observer = `if [ -z "${BEADS_LOOP_ROOT:-}" ]; then
    export BEADS_LOOP_ROOT=1
else
    if [ -z "${1:-}" ]; then
        printf 'child version is empty (BASH_VERSION=%s)\n' "${BASH_VERSION:-unknown}" >&2
        exit 43
    fi
    printf '%s\t%s\t%s\n' "$1" "${CANDIDATE_BIN:-}" "${SMOKE_VERSIONS:-}" >> calls
    if [ -n "${SMOKE_VERSIONS:-}" ]; then
        printf 'child inherited loop control (BASH_VERSION=%s)\n' "${BASH_VERSION:-unknown}" >&2
        exit 42
    fi
    [ "$1" != "${BEADS_LOOP_FAIL:-}" ] || exit 19
    exit 0
fi
`
			script = []byte(shebang + "\n" + observer + body)
			for _, path := range []string{scriptPath, ".buildflags", candidateHelper} {
				data := script
				mode := os.FileMode(0644)
				if path == scriptPath {
					mode = 0755
				} else {
					data, err = os.ReadFile(filepath.Join(sourceRepoRoot(t), path))
					if err != nil {
						// Standalone sources predate the helper; a composed reference requires it.
						if path == candidateHelper && os.IsNotExist(err) && !strings.Contains(body, "smoke-candidate.sh") {
							continue
						}
						t.Fatal(err)
					}
				}
				destination := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			path := os.Getenv("PATH")
			if runtime.GOOS == "windows" {
				path = "/usr/bin:/bin"
			}
			// Relative fixture paths use the same mount under both Bash invocations.
			// HOME stays absent to stop a child missing the observer before smoke setup.
			env := []string{"PATH=" + path, "LC_ALL=C", "LANG=C", "ENV=", "BASH_ENV=",
				"BEADS_LOOP_FAIL=" + tc.failVersion, "CANDIDATE_BIN=" + tc.candidate,
				"SMOKE_VERSIONS=v0.62.0 v0.61.0 v0.60.0"}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "scripts/upgrade-smoke-test.sh")
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("dispatch exit = %v, want %d: %s", err, tc.wantExit, out)
			}
			got, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatalf("read child dispatch: %v; output: %s", err, out)
			}
			var want strings.Builder
			for _, version := range []string{"v0.62.0", "v0.61.0", "v0.60.0"} {
				want.WriteString(version + "\t" + tc.candidate + "\t\n")
			}
			if string(got) != want.String() {
				t.Fatalf("child dispatch = %q, want %q; output: %s", got, want.String(), out)
			}
		})
	}
}
