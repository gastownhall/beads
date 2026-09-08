package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

func TestReleaseScriptUsesVerifiedInstalledBDBeforeStaleRepoBD(t *testing.T) {
	repo := copyReleaseScriptFixture(t)
	writeExecutable(t, filepath.Join(repo, "bd"), `#!/bin/sh
echo "stale repo bd should not run" >&2
exit 42
`)

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBD(t, bin, repo)
	writeExecutable(t, filepath.Join(bin, "jq"), `#!/bin/sh
echo "jq should not be called" >&2
exit 99
`)

	output, err := runReleaseDryRun(t, repo, bin)
	if err != nil {
		t.Fatalf("release.sh failed: %v\n%s", err, output)
	}
	if strings.Contains(output, "stale repo bd should not run") {
		t.Fatalf("release.sh used stale repo-root bd:\n%s", output)
	}
	if !strings.Contains(output, "bd mol wisp beads-release --var version=1.2.3") {
		t.Fatalf("release.sh did not use installed bd command:\n%s", output)
	}
}

func TestReleaseScriptInstalledBDSelectionDoesNotRequireJQ(t *testing.T) {
	repo := copyReleaseScriptFixture(t)

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBD(t, bin, repo)
	writeExecutable(t, filepath.Join(bin, "jq"), `#!/bin/sh
echo "jq should not be called" >&2
exit 99
`)

	output, err := runReleaseDryRun(t, repo, bin)
	if err != nil {
		t.Fatalf("release.sh failed without jq: %v\n%s", err, output)
	}
	if strings.Contains(output, "jq should not be called") {
		t.Fatalf("release.sh invoked jq during bd selection:\n%s", output)
	}
}

func TestReleaseScriptRejectsExplicitBDThatDoesNotResolveRepoFormula(t *testing.T) {
	repo := copyReleaseScriptFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	staleBD := filepath.Join(bin, "bd-stale")
	writeExecutable(t, staleBD, `#!/bin/sh
echo '{"source":"/tmp/stale.formula.toml"}'
`)

	output, err := runReleaseDryRunWithEnv(t, repo, bin, "BD="+staleBD)
	if err == nil {
		t.Fatalf("release.sh succeeded with stale explicit BD:\n%s", output)
	}
	if !strings.Contains(output, "BD is set but does not resolve the checked-in beads-release formula") {
		t.Fatalf("release.sh did not explain stale explicit BD:\n%s", output)
	}
}

func TestReleaseFormulaCleanupStaleDoltOrphansHandlesLocalModeWithoutJQ(t *testing.T) {
	repoRoot := sourceRepoRoot(t)
	formulaPath := filepath.Join(repoRoot, ".beads", "formulas", "beads-release.formula.toml")
	if _, err := formula.NewParser().ParseFile(formulaPath); err != nil {
		t.Fatalf("beads-release formula does not parse: %v", err)
	}

	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, `id = "cleanup-stale-dolt-orphans"`)
	if start < 0 {
		t.Fatal("cleanup-stale-dolt-orphans step not found")
	}
	step := text[start:]
	if next := strings.Index(step[len(`id = "cleanup-stale-dolt-orphans"`):], "\n[[steps]]"); next >= 0 {
		step = step[:len(`id = "cleanup-stale-dolt-orphans"`)+next]
	}

	for _, unwanted := range []string{"jq", "Could not determine Dolt mode"} {
		if strings.Contains(step, unwanted) {
			t.Fatalf("cleanup-stale-dolt-orphans still contains %q:\n%s", unwanted, step)
		}
	}
	for _, want := range []string{`case "$MODE" in`, "embedded)", "external)", "running|data_dir", `bd dolt killall`} {
		if !strings.Contains(step, want) {
			t.Fatalf("cleanup-stale-dolt-orphans missing %q:\n%s", want, step)
		}
	}
}

func copyReleaseScriptFixture(t *testing.T) string {
	t.Helper()

	src := filepath.Join(sourceRepoRoot(t), "scripts", "release.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".beads", "formulas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "release.sh"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "formulas", "beads-release.formula.toml"), []byte("formula = \"beads-release\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func sourceRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

func runReleaseDryRun(t *testing.T, repo, bin string) (string, error) {
	t.Helper()
	return runReleaseDryRunWithEnv(t, repo, bin, "BD=")
}

func runReleaseDryRunWithEnv(t *testing.T, repo, bin string, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repo, "scripts", "release.sh"), "1.2.3", "--dry-run")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFakeBD(t *testing.T, bin, repo string) {
	t.Helper()
	source := filepath.Join(repo, ".beads", "formulas", "beads-release.formula.toml")
	body := fmt.Sprintf(`#!/bin/sh
SOURCE=%q
if [ "$1 $2 $3 $4" = "formula show beads-release --json" ]; then
  printf '%%s\n' "{\"source\":\"$SOURCE\"}"
  exit 0
fi
if [ "$1 $2 $3" = "formula show beads-release" ]; then
  printf '   ├── preflight\n'
  printf '   └── release-complete\n'
  exit 0
fi
echo "unexpected fake bd invocation: $*" >&2
exit 64
`, source)
	writeExecutable(t, filepath.Join(bin, "bd"), body)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
