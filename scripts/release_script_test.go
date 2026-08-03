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

	bin := filepath.Join(repo, "bin")
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

	bin := filepath.Join(repo, "bin")
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
	bin := filepath.Join(repo, "bin")
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

func TestReleaseFormulaHomebrewCoreProcedureCoversTemplateAndBottles(t *testing.T) {
	repoRoot := sourceRepoRoot(t)
	formulaPath := filepath.Join(repoRoot, ".beads", "formulas", "beads-release.formula.toml")
	if _, err := formula.NewParser().ParseFile(formulaPath); err != nil {
		t.Fatalf("beads-release formula does not parse: %v", err)
	}

	homebrewStep := releaseFormulaStep(t, formulaPath, `id = "verify-homebrew-core"`)
	for _, want := range []string{
		"brew bump-formula-pr",
		"--url=\"https://github.com/gastownhall/beads/archive/refs/tags/v{{version}}.tar.gz\"",
		"Homebrew's current PR template",
		"Do not remove or hand-edit the bottle block.",
		"Do not open a duplicate manual PR.",
		"homepage \"https://github.com/gastownhall/beads\"",
		"Homebrew/homebrew-core@main",
	} {
		if !strings.Contains(homebrewStep, want) {
			t.Fatalf("verify-homebrew-core step missing %q:\n%s", want, homebrewStep)
		}
	}

	localInstallStep := releaseFormulaStep(t, formulaPath, `id = "local-install"`)
	for _, want := range []string{
		`needs = ["verify-github", "verify-npm", "verify-pypi", "verify-homebrew-core"]`,
		"brew upgrade beads",
	} {
		if !strings.Contains(localInstallStep, want) {
			t.Fatalf("local-install step missing %q:\n%s", want, localInstallStep)
		}
	}
	if strings.Contains(localInstallStep, "brew upgrade bd") {
		t.Fatalf("local-install step still references old Homebrew formula name:\n%s", localInstallStep)
	}
}

func releaseFormulaStep(t *testing.T, formulaPath, marker string) string {
	t.Helper()
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("release formula step marker not found: %s", marker)
	}
	step := text[start:]
	if next := strings.Index(step[len(marker):], "\n[[steps]]"); next >= 0 {
		step = step[:len(marker)+next]
	}
	return step
}

func copyReleaseScriptFixture(t *testing.T) string {
	t.Helper()

	src := filepath.Join(sourceRepoRoot(t), "scripts", "release.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	repo := releaseTestTempDir(t)
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

func releaseTestTempDir(t *testing.T) string {
	t.Helper()
	// Keep fixtures out of the source tree (t.TempDir handles cleanup and
	// works from read-only checkouts); shellPath converts the resulting
	// host path for Bash separately at the call sites that need it.
	return t.TempDir()
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
	assignments := []string{
		// Fake bin first, then the caller's PATH (Nix/Guix coreutils), then a
		// /usr/bin:/bin baseline: on Windows git-bash the coreutils release.sh
		// needs live there but are absent from the Go process's PATH.
		"PATH=" + shSingleQuote(shellPath(t, bin)+":"+bashPathList(t, os.Getenv("PATH"))+":/usr/bin:/bin"),
		"BD_FAKE_FORMULA_SOURCE=" + shSingleQuote(shellPath(t, filepath.Join(repo, ".beads", "formulas", "beads-release.formula.toml"))),
	}
	for _, env := range extraEnv {
		key, value, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		if key == "BD" && value != "" {
			value = shellPath(t, value)
		}
		assignments = append(assignments, key+"="+shSingleQuote(value))
	}
	cmd := exec.Command("bash", "-lc", strings.Join(assignments, " ")+" bash scripts/release.sh 1.2.3 --dry-run")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func msysPath(path string) string {
	path = filepath.Clean(path)
	path = filepath.ToSlash(path)
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' {
		return "/" + strings.ToLower(path[:1]) + path[2:]
	}
	return path
}

func shellPath(t *testing.T, path string) string {
	t.Helper()
	clean := filepath.Clean(path)
	dir := clean
	base := ""
	if info, err := os.Stat(clean); err == nil && !info.IsDir() {
		dir = filepath.Dir(clean)
		base = filepath.Base(clean)
	}
	cmd := exec.Command("bash", "-lc", "pwd")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		converted := strings.TrimSpace(string(out))
		if converted != "" {
			if base != "" {
				return converted + "/" + filepath.ToSlash(base)
			}
			return converted
		}
	}
	for _, tool := range []string{"wslpath", "cygpath"} {
		out, err := exec.Command(tool, "-u", path).Output()
		if err == nil {
			converted := strings.TrimSpace(string(out))
			if converted != "" {
				return converted
			}
		}
	}
	return msysPath(path)
}

// bashPathList converts a host-style PATH value (entries separated by
// os.PathListSeparator) into a Bash-visible, colon-separated PATH so the
// caller's PATH is preserved (not just /usr/bin:/bin) when it is prepended
// with the fake bd directory. This matters on systems such as Nix/Guix
// where bash and core utilities live outside /usr/bin and /bin.
func bashPathList(t *testing.T, hostPath string) string {
	t.Helper()
	entries := filepath.SplitList(hostPath)
	converted := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		converted = append(converted, shellPath(t, entry))
	}
	return strings.Join(converted, ":")
}

func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func writeFakeBD(t *testing.T, bin, repo string) {
	t.Helper()
	// Let bash itself canonicalize the formula path. release.sh derives
	// FORMULA_PATH from `pwd`, which on Windows under Git Bash/MSYS produces
	// mount-aware forms. Keep the fake bd path repo-relative so bash can
	// `cd` there portably, then let `pwd` resolve it to the exact form that
	// release.sh will compare against.
	formulaDir := ".beads/formulas"
	body := fmt.Sprintf(`#!/bin/sh
if [ -n "${BD_FAKE_FORMULA_SOURCE:-}" ]; then
  SOURCE="$BD_FAKE_FORMULA_SOURCE"
else
  SOURCE="$(cd %q && pwd)/beads-release.formula.toml"
fi
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
`, formulaDir)
	writeExecutable(t, filepath.Join(bin, "bd"), body)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		chmodPath := shellPath(t, path)
		if wd, err := os.Getwd(); err == nil {
			if rel, relErr := filepath.Rel(wd, path); relErr == nil {
				chmodPath = filepath.ToSlash(rel)
			}
		}
		cmd := exec.Command("bash", "-lc", "chmod +x "+shSingleQuote(chmodPath))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("chmod +x %s failed: %v\n%s", path, err, out)
		}
	}
}
