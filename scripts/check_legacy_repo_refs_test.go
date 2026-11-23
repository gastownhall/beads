package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const legacyModuleGoMod = "module github.com/steveyegge/beads\n\ngo 1.26\n"

// runLegacyRepoRefsGuard runs check-legacy-repo-refs.sh from the root of a
// throwaway git repository holding files (all `git add`ed, as run_check.sh
// does). The script stays outside the repository, so its own path allowlist
// plays no part.
func runLegacyRepoRefsGuard(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guard is a Bash boundary")
	}
	requireHostTool(t, "git")
	requireHostTool(t, "awk")
	script := filepath.Join(sourceRepoRoot(t), "scripts", "check-legacy-repo-refs.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat check-legacy-repo-refs.sh: %v", err)
	}
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-f", "-A", "."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCheckLegacyRepoRefsAccepts: the module path go.mod declares (outside a
// URL), the author profile, attribution and the legacy winget id are not stale
// repository references.
func TestCheckLegacyRepoRefsAccepts(t *testing.T) {
	for _, c := range []struct{ name, file, line string }{
		{"import path", "cmd/x/main.go", `import "github.com/steveyegge/beads/internal/types"`},
		{"go install", "docs/install.md", "CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest"},
		{"go get", "examples/README.md", "go get github.com/steveyegge/beads@latest"},
		{"require and replace", "examples/x/go.mod", "replace github.com/steveyegge/beads => ../.."},
		{"regex-escaped module path", ".golangci.yml", `pkg: '^github\.com/steveyegge/beads/internal/types$'`},
		{"go report card badge", "README.md", "[![Go Report Card](https://goreportcard.com/badge/github.com/steveyegge/beads)](https://goreportcard.com/report/github.com/steveyegge/beads)"},
		{"author profile", "plugin.json", `"url": "https://github.com/steveyegge"`},
		{"author profile at end of line", "winget/x.yaml", "PublisherUrl: https://github.com/steveyegge"},
		{"at-mention", "NOTES.md", "Thanks @steveyegge for the review."},
		{"possessive attribution", "x_test.go", "// steveyegge's PR #5393 review flagged this"},
		{"legacy winget id", "docs/install.md", "winget install SteveYegge.beads"},
		{"info.go line allowlist", "cmd/bd/info.go", `"FIX: URLs from steveyegge/beads to gastownhall/beads so provenance passes",`},
		{"upgrading.md line allowlist", "docs/getting-started/upgrading.md", "brew untap steveyegge/beads 2>/dev/null || true"},
		{"changelog history", "CHANGELOG.md", "- fix (https://github.com/steveyegge/beads/pull/12)"},
		{"release gate history", "release-gates/x.md", "see https://github.com/steveyegge/beads/issues/3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := runLegacyRepoRefsGuard(t, map[string]string{
				"go.mod": legacyModuleGoMod,
				c.file:   "first line\n" + c.line + "\n",
			})
			if err != nil {
				t.Fatalf("guard refused %q in %s: %v\n%s", c.line, c.file, err, out)
			}
		})
	}
}

// TestCheckLegacyRepoRefsRejects: any other steveyegge reference is a stale
// repository reference, reported as file:line with exit 1.
func TestCheckLegacyRepoRefsRejects(t *testing.T) {
	for _, c := range []struct{ name, file, line string }{
		{"issue url", "cmd/bd/doctor/git.go", `const u = "https://github.com/steveyegge/beads/issues/1"`},
		{"repo url", "README.md", "[repo](https://github.com/steveyegge/beads)"},
		{"raw install script", "cmd/bd/doctor/version.go", "curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash"},
		{"github pages", "docs/x.md", "https://steveyegge.github.io/beads/"},
		{"marketplace add", "cmd/bd/doctor/claude.go", "/plugin marketplace add steveyegge/beads"},
		{"issue shorthand", "internal/x.go", "// See steveyegge/beads#1692."},
		{"refused ref beside an allowed import", "x.go", `import "github.com/steveyegge/beads" // https://github.com/steveyegge/beads/issues/2`},
		{"profile url with a path", "x.md", "https://github.com/steveyegge/other"},
		{"line allowlist is per file", "README.md", "brew untap steveyegge/beads"},
		{"line allowlist does not widen", "cmd/bd/info.go", `"from steveyegge/beads to gastownhall/beads, see steveyegge/beads#9",`},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := runLegacyRepoRefsGuard(t, map[string]string{
				"go.mod": legacyModuleGoMod,
				c.file:   "first line\n" + c.line + "\n",
			})
			if code := exitCode(err); code != 1 {
				t.Fatalf("guard exit = %d for %q in %s, want 1\n%s", code, c.line, c.file, out)
			}
			want := "error: " + c.file + ":2: legacy steveyegge repository reference"
			if !strings.Contains(out, want) {
				t.Errorf("guard output lacks %q:\n%s", want, out)
			}
		})
	}
}

// TestCheckLegacyRepoRefsFollowsGoMod: the allowance is keyed on go.mod, so
// after a module rename every leftover old import path is refused.
func TestCheckLegacyRepoRefsFollowsGoMod(t *testing.T) {
	out, err := runLegacyRepoRefsGuard(t, map[string]string{
		"go.mod":        "module github.com/gastownhall/beads\n",
		"cmd/x/main.go": "package main\n\nimport _ \"github.com/steveyegge/beads/internal/types\"\n",
	})
	if code := exitCode(err); code != 1 {
		t.Fatalf("guard exit = %d after a module rename, want 1\n%s", code, out)
	}
	if want := "error: cmd/x/main.go:3: legacy steveyegge repository reference"; !strings.Contains(out, want) {
		t.Errorf("guard output lacks %q:\n%s", want, out)
	}
}

// TestCheckLegacyRepoRefsCleanTree: a tree with no steveyegge reference passes.
func TestCheckLegacyRepoRefsCleanTree(t *testing.T) {
	out, err := runLegacyRepoRefsGuard(t, map[string]string{
		"go.mod":    "module github.com/gastownhall/beads\n",
		"README.md": "https://github.com/gastownhall/beads\n",
	})
	if err != nil {
		t.Fatalf("clean tree refused: %v\n%s", err, out)
	}
}
