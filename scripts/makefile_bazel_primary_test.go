package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Bazel is beads' build and test system: bazel.yml's lanes gate every PR,
// and nogo (lint + vet), formatting and the repository guards exist only as
// Bazel targets. The primary make targets run the commands those lanes run,
// everywhere (GitHub Actions included), and each keeps a plain go twin under
// an explicit -go name. A workflow that runs a Go-native suite calls the -go
// name, so its engine is written in the workflow, not sniffed from the
// environment.

const fakeMakeBazel = "/fake/bazel"

// makeDryRun prints the recipe `make TARGET` would run, with BAZEL pointing
// at a fake path so a bazel invocation is recognizable.
func makeDryRun(t *testing.T, target string, env ...string) string {
	t.Helper()
	makeBin := requireHostTool(t, "make")
	root := sourceRepoRoot(t)
	cmd := exec.Command(makeBin, "--no-print-directory", "-n",
		"-f", filepath.Join(root, "Makefile"),
		"BAZEL="+fakeMakeBazel,
		target)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GITHUB_ACTIONS", "BAZEL_FLAGS", "MAKEFLAGS", "MAKELEVEL", "MFLAGS":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", target, err, out)
	}
	return string(out)
}

// goEngineRE matches a recipe line that tests with the Go toolchain.
var goEngineRE = regexp.MustCompile(`(^|\s)go test\s|scripts/test\.sh`)

func TestMakePrimaryTargetsRunBazel(t *testing.T) {
	for target, wants := range map[string][]string{
		"test": {fakeMakeBazel + " test  //... --config=ci"},
		"check": {
			"./scripts/check-testing-short.sh",
			"./scripts/ci/pr-lint.sh",
			fakeMakeBazel + " test  //... --config=ci",
		},
		"check-docs": {
			fakeMakeBazel + " test  //test/docsync:docsync_test //scripts/repochecks:doc_freshness_test --config=ci",
			"./scripts/check-doc-flags.sh",
		},
	} {
		for _, env := range [][]string{nil, {"GITHUB_ACTIONS=true"}} {
			t.Run(target+strings.Join(env, ","), func(t *testing.T) {
				out := makeDryRun(t, target, env...)
				for _, want := range wants {
					if !strings.Contains(out, want) {
						t.Errorf("make %s (env %v) does not run %q:\n%s", target, env, want, out)
					}
				}
				if goEngineRE.MatchString(out) {
					t.Errorf("make %s (env %v) runs go test instead of bazel:\n%s", target, env, out)
				}
			})
		}
	}
}

func TestMakeBazelFlagsReachEveryBazelTest(t *testing.T) {
	out := makeDryRun(t, "test", "BAZEL_FLAGS=--config=fork-cache")
	if want := fakeMakeBazel + " test --config=fork-cache //... --config=ci"; !strings.Contains(out, want) {
		t.Errorf("make test with BAZEL_FLAGS does not run %q:\n%s", want, out)
	}
}

func TestMakeGoTwinsRunGoTest(t *testing.T) {
	for target, want := range map[string]string{
		"test-go":       "./scripts/test.sh",
		"check-go":      "./scripts/test.sh",
		"check-docs-go": "go test -tags=gms_pure_go ./test/docsync",
	} {
		t.Run(target, func(t *testing.T) {
			out := makeDryRun(t, target)
			if !strings.Contains(out, want) {
				t.Errorf("make %s does not run %q:\n%s", target, want, out)
			}
			if strings.Contains(out, fakeMakeBazel+" test") {
				t.Errorf("make %s runs bazel test:\n%s", target, out)
			}
		})
	}
}

// primaryMakeCallRE matches a call of a bazel-backed primary make target (not
// its -go twin or another target sharing the prefix).
var primaryMakeCallRE = regexp.MustCompile(`\bmake (test|check|check-docs)(\s|$|["'])`)

// TestWorkflowsNameTheirTestEngine: a workflow runs bazel directly, or a
// Go-native suite through an explicit -go make target, never a primary name
// whose engine a reader would have to look up in the Makefile.
func TestWorkflowsNameTheirTestEngine(t *testing.T) {
	root := sourceRepoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob workflows: %v (%d files)", err, len(files))
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "name:") {
				continue
			}
			if m := primaryMakeCallRE.FindStringSubmatch(line); m != nil {
				t.Errorf("%s:%d calls `make %s`, which runs bazel; call `make %s-go` for the Go-native suite or bazel directly",
					filepath.Base(file), i+1, m[1], m[1])
			}
		}
	}
}
