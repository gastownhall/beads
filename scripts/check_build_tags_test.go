package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runCheckBuildTags runs check-build-tags.sh in a throwaway git repository
// holding the given tracked files plus a .bazelrc that sets gms_pure_go. The
// script resolves the repository from its own location, so it is copied in.
func runCheckBuildTags(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("checker is a Bash boundary")
	}
	requireHostTool(t, "git")
	// The checker needs bash >= 4 (mapfile); macOS ships bash 3.2 as /bin/bash.
	if err := exec.Command("bash", "-c", "type mapfile").Run(); err != nil {
		skipOrFailWithoutHostTool(t, "bash lacks mapfile (bash >= 4 required)")
	}
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "check-build-tags.sh"))
	if err != nil {
		t.Fatalf("read check-build-tags.sh: %v", err)
	}
	dir := t.TempDir()
	all := map[string]string{
		"scripts/check-build-tags.sh": string(script),
		".bazelrc":                    "build --@rules_go//go/config:tags=gms_pure_go\n",
	}
	for rel, content := range files {
		all[rel] = content
	}
	for rel, content := range all {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "check-build-tags.sh"))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A Bazel invocation on the same line must not exempt a bare `go` command in
// another shell command segment of that line.
func TestCheckBuildTagsBazelLineDoesNotExemptBareGo(t *testing.T) {
	for name, line := range map[string]string{
		"and-list":     "bazel build //cmd/bd && go test ./internal/...",
		"sequence":     "bazel query //... >/dev/null; go build ./cmd/bd",
		"substitution": "go run ./cmd/bd -- $(bazel run //:gazelle)",
		"pipe":         "bazel query //... | go run ./cmd/bd",
		"make var":     "\t$(BAZEL) build //... || go install ./cmd/bd",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runCheckBuildTags(t, map[string]string{"scripts/ci.sh": "#!/usr/bin/env bash\n" + line + "\n"})
			if err == nil {
				t.Fatalf("bare go next to a Bazel invocation was accepted: %q\n%s", line, out)
			}
			if !strings.Contains(out, "scripts/ci.sh:2") {
				t.Errorf("expected scripts/ci.sh:2 to be reported, got:\n%s", out)
			}
		})
	}
}

func TestCheckBuildTagsExemptsBazelInvocations(t *testing.T) {
	out, err := runCheckBuildTags(t, map[string]string{
		"scripts/ci.sh": "#!/usr/bin/env bash\n" +
			"bazel run @rules_go//go -- test ./...\n" +
			"bazel test //... && go test -tags=gms_pure_go ./...\n" +
			"bazel build //cmd/bd | tee build.log\n",
		"Makefile": "BAZEL ?= bazel\nsync:\n\t$(BAZEL) run //:gazelle\n\t$(BAZEL) mod tidy\n",
	})
	if err != nil {
		t.Fatalf("Bazel invocations should be exempt: %v\n%s", err, out)
	}
}

func TestCheckBuildTagsRequiresBazelrcTagForBazelUsers(t *testing.T) {
	out, err := runCheckBuildTags(t, map[string]string{
		"scripts/ci.sh": "#!/usr/bin/env bash\nbazel build //...\n",
		".bazelrc":      "build --incompatible_strict_action_env\n",
	})
	if err == nil {
		t.Fatalf(".bazelrc without gms_pure_go was accepted:\n%s", out)
	}
}

// Workflows are not scanned for `go` commands here, but they still feed the
// Bazel census: a workflow that invokes Bazel requires the .bazelrc tag and is
// named as the reason.
func TestCheckBuildTagsCountsWorkflowBazelUsers(t *testing.T) {
	out, err := runCheckBuildTags(t, map[string]string{
		".github/workflows/ci.yml": "jobs:\n" +
			"  build:\n" +
			"    runs-on: ubuntu-latest\n" +
			"    steps:\n" +
			"      - run: bazel build //...\n",
		".bazelrc": "build --incompatible_strict_action_env\n",
	})
	if err == nil {
		t.Fatalf(".bazelrc without gms_pure_go was accepted for a workflow Bazel user:\n%s", out)
	}
	if !strings.Contains(out, ".github/workflows/ci.yml") {
		t.Errorf("expected .github/workflows/ci.yml to be reported as a Bazel user, got:\n%s", out)
	}
}

// Workflow `run` steps are checked structurally by scripts/checkworkflowtags;
// the line scan here must not police them as well.
func TestCheckBuildTagsSkipsWorkflowGoCommands(t *testing.T) {
	out, err := runCheckBuildTags(t, map[string]string{
		".github/workflows/ci.yml": "jobs:\n" +
			"  test:\n" +
			"    runs-on: ubuntu-latest\n" +
			"    steps:\n" +
			"      - run: go test ./...\n",
	})
	if err != nil {
		t.Fatalf("a workflow go command was scanned by check-build-tags.sh: %v\n%s", err, out)
	}
}
