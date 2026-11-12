package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runLegacyRepoRefsGuard runs check-legacy-repo-refs.sh over a throwaway git
// repository holding files (repo-relative path to content). The guard greps
// with `git grep` and reads the module path from go.mod, so the fixture is a
// real git repository, laid out the way scripts/repochecks/run_check.sh
// materializes the checkout under `bazel test`. The guard's own copy travels
// with the fixture: it is on its own path allowlist.
func runLegacyRepoRefsGuard(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guard is a Bash boundary")
	}
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "check-legacy-repo-refs.sh"))
	if err != nil {
		t.Fatalf("read check-legacy-repo-refs.sh: %v", err)
	}
	dir := t.TempDir()
	all := map[string]string{"scripts/check-legacy-repo-refs.sh": string(script)}
	for rel, content := range files {
		all[rel] = content
	}
	for rel, content := range all {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-f", "-A", "."},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "check-legacy-repo-refs.sh"))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const legacyRepoRefsModuleGoMod = "module github.com/steveyegge/beads\n\ngo 1.24\n"

// TestCheckLegacyRepoRefsAcceptsModulePathShapes pins the shapes the module
// path keeps legal while go.mod still declares github.com/steveyegge/beads:
// import paths, go install guidance, the regex-escaped depguard form, the
// Go Report Card badge, the bare author-profile URL, attribution, the
// legacy winget id (case differs), and the two line-allowlist entries.
func TestCheckLegacyRepoRefsAcceptsModulePathShapes(t *testing.T) {
	out, err := runLegacyRepoRefsGuard(t, map[string]string{
		"go.mod": legacyRepoRefsModuleGoMod,
		"main.go": "// A plain import path follows the module path in go.mod.\n" +
			"import \"github.com/steveyegge/beads/internal/types\"\n",
		"Makefile": "install:\n\tCGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest\n",
		".golangci.yml": "          pkg: '^github\\.com/steveyegge/beads/internal/types$'\n",
		"README.md": "[![Go Report Card](https://goreportcard.com/badge/github.com/steveyegge/beads)](https://goreportcard.com/report/github.com/steveyegge/beads)\n",
		"plugin.json": "{\n  \"author\": { \"url\": \"https://github.com/steveyegge\" }\n}\n",
		"CONTRIBUTING.md": "Ported from @steveyegge's #4740 review.\n",
		"winget/SteveYegge.beads.locale.en-US.yaml": "PackageIdentifier: SteveYegge.beads\n",
		// Line allowlist: release history text and the legacy-tap removal step.
		"cmd/bd/info.go": "// npm publish fix moved URLs from steveyegge/beads to gastownhall/beads.\n",
		"docs/getting-started/upgrading.md": "brew untap steveyegge/beads 2>/dev/null || true\n",
	})
	if err != nil {
		t.Fatalf("guard rejected module-path shapes: %v\n%s", err, out)
	}
	if strings.Contains(out, "error:") {
		t.Fatalf("guard printed errors on module-path shapes:\n%s", out)
	}
}

// TestCheckLegacyRepoRefsRejectsRepositoryURLs pins the shapes that only
// resolve through GitHub's transfer redirect: each one fails with a
// file:line error and exit 1.
func TestCheckLegacyRepoRefsRejectsRepositoryURLs(t *testing.T) {
	for _, tc := range []struct {
		name, file, content string
	}{
		{
			name:    "github repository URL",
			file:    "docs/history.md",
			content: "See https://github.com/steveyegge/beads/issues/1 for history.\n",
		},
		{
			name:    "raw.githubusercontent URL",
			file:    "docs/install.md",
			content: "curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash\n",
		},
		{
			name:    "github pages URL",
			file:    "docs/quickstart.md",
			content: "Read the docs at https://steveyegge.github.io/beads/quickstart.\n",
		},
		{
			name:    "marketplace add",
			file:    "cmd/doctor.go",
			content: "Fix: \"/plugin marketplace add steveyegge/beads\"\n",
		},
		{
			name:    "author profile followed by a repository path",
			file:    "docs/follow.md",
			content: "Follow https://github.com/steveyegge/beads-sub for news.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLegacyRepoRefsGuard(t, map[string]string{
				"go.mod":    legacyRepoRefsModuleGoMod,
				tc.file:     tc.content,
			})
			if err == nil {
				t.Fatalf("guard accepted a repository reference (%s):\n%s", tc.name, out)
			}
			if code := exitCode(err); code != 1 {
				t.Fatalf("guard exit = %d, want 1:\n%s", code, out)
			}
			want := "error: " + tc.file + ":1: legacy steveyegge repository reference"
			if !strings.Contains(out, want) {
				t.Fatalf("guard output missing %q:\n%s", want, out)
			}
		})
	}
}
