package scripts_test

import (
	"regexp"
	"strings"
	"testing"
)

// MODULE.bazel substitutes tools/bazel/go_repository_tools.bzl for
// gazelle's @bazel_gazelle_go_repository_tools, which serves fetch_repo and
// gazelle from the repository cache by pinned sha256 instead of compiling
// them in every fresh output base. A gazelle or Go SDK bump without new pins
// still builds (from source) but silently loses that, so the pins must name
// the versions MODULE.bazel pins.

const goRepositoryToolsRule = "tools/bazel/go_repository_tools.bzl"

var goRepositoryToolsNames = []string{"fetch_repo", "gazelle", "generate_repo_config"}

func TestGoRepositoryToolsOverridesGazelle(t *testing.T) {
	module := readPolicyFile(t, bazelPolicyRoot(t), hermeticCCModuleFile)
	for _, want := range []string{
		`gazelle_non_module_deps = use_extension("@gazelle//internal/bzlmod:non_module_deps.bzl", "non_module_deps")`,
		`go_repository_tools = use_repo_rule("//tools/bazel:go_repository_tools.bzl", "go_repository_tools")`,
		`go_repository_tools(name = "gazelle_go_repository_tools")`,
	} {
		if !strings.Contains(module, want) {
			t.Errorf("%s must contain %s", hermeticCCModuleFile, want)
		}
	}
	override, err := moduleCall(module, "override_repo", "gazelle_non_module_deps")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(override, `bazel_gazelle_go_repository_tools = "gazelle_go_repository_tools"`) {
		t.Errorf("override_repo(gazelle_non_module_deps, ...) must map bazel_gazelle_go_repository_tools to gazelle_go_repository_tools, got:\n%s", override)
	}
}

func TestGoRepositoryToolsPinnedForModuleVersions(t *testing.T) {
	root := bazelPolicyRoot(t)
	module := readPolicyFile(t, root, hermeticCCModuleFile)
	gazelle := regexp.MustCompile(`(?m)^bazel_dep\(name = "gazelle", version = "([^"]+)"\)`).FindStringSubmatch(module)
	if gazelle == nil {
		t.Fatalf("%s: no bazel_dep(name = \"gazelle\", version = ...)", hermeticCCModuleFile)
	}
	sdk, err := moduleCall(module, "go_sdk.download", `name = "go_sdk"`)
	if err != nil {
		t.Fatal(err)
	}
	goVersion := regexp.MustCompile(`version = "([^"]+)"`).FindStringSubmatch(sdk)
	if goVersion == nil {
		t.Fatalf("%s: go_sdk.download(name = \"go_sdk\") has no version", hermeticCCModuleFile)
	}

	// CI's lanes run on linux/amd64; that host must always be pinned.
	key := gazelle[1] + "/go" + goVersion[1] + "/linux_amd64"
	rule := readPolicyFile(t, root, goRepositoryToolsRule)
	entry := regexp.MustCompile(`(?s)"` + regexp.QuoteMeta(key) + `": \{(.*?)\}`).FindStringSubmatch(rule)
	if entry == nil {
		t.Fatalf("%s: _PINS has no %q entry (gazelle %s, Go %s from %s); fetch @gazelle_go_repository_tools on linux/amd64 and copy the digests it prints",
			goRepositoryToolsRule, key, gazelle[1], goVersion[1], hermeticCCModuleFile)
	}
	pins := quotedPairs(entry[1])
	if len(pins) != len(goRepositoryToolsNames) {
		t.Errorf("_PINS[%q] = %v, want exactly %v", key, pins, goRepositoryToolsNames)
	}
	for _, tool := range goRepositoryToolsNames {
		if !sha256HexRE.MatchString(pins[tool]) {
			t.Errorf("_PINS[%q][%q] = %q, want a sha256", key, tool, pins[tool])
		}
	}
}
