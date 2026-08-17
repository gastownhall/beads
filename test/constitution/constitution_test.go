package constitution

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func TestCLAUDEMDIsSymlinkToAgentsMD(t *testing.T) {
	root := repoRoot(t)
	claude := filepath.Join(root, "CLAUDE.md")
	info, err := os.Lstat(claude)
	if err != nil {
		t.Fatalf("lstat CLAUDE.md: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("CLAUDE.md must be a symlink to AGENTS.md")
	}
	target, err := os.Readlink(claude)
	if err != nil {
		t.Fatalf("readlink CLAUDE.md: %v", err)
	}
	if filepath.Base(target) != "AGENTS.md" {
		t.Fatalf("CLAUDE.md symlink target = %q, want AGENTS.md", target)
	}
}

func TestAgentsMDIsCanonicalConstitution(t *testing.T) {
	agents := readRepoFile(t, "AGENTS.md")
	if strings.Contains(agents, "<!-- bd-doctor-divergence: ok -->") {
		t.Fatal("AGENTS.md still opts out of CLAUDE.md identity; CLAUDE.md is a symlink")
	}
	if strings.Contains(agents, "This file exists for compatibility") {
		t.Fatal("AGENTS.md still describes itself as a compatibility shim")
	}
}

func TestDefaultBuildIsNoCGO(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	if strings.Contains(makefile, "export CGO_ENABLED := 1") {
		t.Fatal("Makefile still forces CGO_ENABLED=1")
	}
	if !strings.Contains(makefile, "CGO_ENABLED ?= 0") {
		t.Fatal("Makefile must default CGO_ENABLED to 0")
	}

	script := `unset CGO_ENABLED
# shellcheck source=/dev/null
source .buildflags
printf '%s' "$CGO_ENABLED"
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source .buildflags: %v\n%s", err, out)
	}
	if got := string(out); got != "0" {
		t.Fatalf(".buildflags default CGO_ENABLED = %q, want 0", got)
	}
}

func TestAgentsAndContributingDirectCappedDocker(t *testing.T) {
	recipe := readRepoFile(t, "scripts/dev-docker.sh")
	for _, needle := range []string{
		"--cpus=2",
		"--memory=4g",
		"--memory-swap=4g",
		"--pids-limit=512",
		"source \"$ROOT/.buildflags\"",
	} {
		if !strings.Contains(recipe, needle) {
			t.Errorf("scripts/dev-docker.sh missing %q", needle)
		}
	}
	for _, rel := range []string{"AGENTS.md", "CONTRIBUTING.md"} {
		body := readRepoFile(t, rel)
		if !strings.Contains(body, "scripts/dev-docker.sh") {
			t.Errorf("%s does not direct scripts/dev-docker.sh", rel)
		}
		if strings.Contains(body, "--cpus=2") {
			t.Errorf("%s recaches Docker caps; point at scripts/dev-docker.sh", rel)
		}
	}
}
