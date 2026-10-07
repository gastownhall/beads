package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Lint and vet are nogo (//tools/nogo): go test's vet checks plus the
// golangci-lint linters .golangci.yml enables, validated beside every Go
// compile. bazel.yml's required lanes gate them: natively inside
// `bazel test //... --config=ci` (test lane), and for the files only other
// platforms compile inside the pure lane's release cross-compile
// (//tools/bazel:release_cross, every release platform). These pin that
// wiring and that golangci-lint is gone from CI, hooks and make.

// nogoBazelrcLines are .bazelrc's nogo configs, exactly.
var nogoBazelrcLines = []string{
	"build:nogo --@rules_go//go/config:race",
	"build:nogo --keep_going",
	"build:nogo --output_groups=nogo_fix",
	"build:nogo-cross --keep_going",
	"build:nogo-cross --output_groups=nogo_fix",
}

func TestLintAndVetRunAsNogo(t *testing.T) {
	root := sourceRepoRoot(t)

	rc := readPolicyFile(t, root, ".bazelrc")
	var got []string
	for _, line := range strings.Split(rc, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "build:nogo") {
			got = append(got, line)
		}
	}
	if !equalStrings(got, nogoBazelrcLines) {
		t.Errorf(".bazelrc nogo configs = %q, want %q", got, nogoBazelrcLines)
	}
	// Every lane validates: a lane that skipped nogo would let findings in
	// the files only it compiles (integration-tagged, pure) through.
	if strings.Contains(rc, "run_validations") {
		t.Error(".bazelrc turns nogo validation off for some lane")
	}

	// The cross-platform half: the pure lane's release cross-compile builds
	// every library and binary for every release platform, and nogo
	// validates each compile (TestReleaseCrossCompileRunsInBazelPureLane
	// pins the step). Nothing on that path may opt out of validation.
	if script := readPolicyFile(t, root, "scripts/ci/bazel-release-cross-compile.sh"); strings.Contains(script, "run_validations") {
		t.Error("scripts/ci/bazel-release-cross-compile.sh turns nogo validation off")
	}
	for _, line := range strings.Split(rc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "build:release-cross") && strings.Contains(line, "validation") {
			t.Errorf(".bazelrc %q changes validation for the release cross-compile", line)
		}
	}

	// No workflow installs or runs golangci-lint.
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		for name, j := range readCIWorkflow(t, e.Name()).Jobs {
			for _, st := range j.Steps {
				if strings.Contains(st.Run, "golangci") || strings.Contains(st.Uses, "golangci") {
					t.Errorf("%s %s step %q runs golangci-lint; nogo replaces it", e.Name(), name, st.Name)
				}
			}
		}
	}
	for _, gone := range []string{"scripts/ci/install-golangci-lint.sh", "scripts/ci/go-test-vet.sh"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s is back; nogo replaces it", gone)
		}
	}

	// Local entrypoints: make, the pre-commit hook and pre-commit's config.
	makefile := readPolicyFile(t, root, "Makefile")
	for _, want := range []string{
		"ci-pr-lint:\n\t@./scripts/ci/pr-lint.sh\n",
		"lint: ci-pr-lint\n",
		"vet: lint\n",
		"\t$(BAZEL) build --config=nogo -- $$packages\n",
	} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile lacks %q", want)
		}
	}
	for _, hook := range []string{".githooks/pre-commit", ".pre-commit-config.yaml"} {
		body := readPolicyFile(t, root, hook)
		if strings.Contains(body, "golangci-lint run") || strings.Contains(body, "golangci-lint@") || strings.Contains(body, "golangci/golangci-lint") {
			t.Errorf("%s still runs golangci-lint", hook)
		}
		if !strings.Contains(body, "make lint-changed LINT_CHANGED_SCOPE=staged") {
			t.Errorf("%s does not lint with nogo (make lint-changed LINT_CHANGED_SCOPE=staged)", hook)
		}
	}
	// gofmt is //scripts/repochecks:fmt_test's, not the lint wrapper's
	// (ga-96smfk.41).
	if wrapper := readPolicyFile(t, root, "scripts/ci/pr-lint.sh"); regexp.MustCompile(`fmt-check|gofmt-bin`).MatchString(wrapper) {
		t.Error("scripts/ci/pr-lint.sh runs gofmt; //scripts/repochecks:fmt_test gates formatting")
	}
}
