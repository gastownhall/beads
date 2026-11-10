package scripts_test

import (
	"strings"
	"testing"
)

// servedProviderRuleLines are the literal lines of
// //internal/httpclient:httpclient_served_provider_test that make it the
// served tier's PROVIDER source run: the variant wrapper over the package's
// test binary, the bd its unit-of-work provider spawns as the db-proxy-child,
// and the env that selects the provider source, makes the tier required, and
// makes a dolt sql-server that cannot start a failure instead of a skip.
//
// TestBazelRetiredLanesCannotBeNarrowed pins the target's args
// (extraRunVariants) but not its env; this pins the env. Without
// BEADS_HTTP_SERVED_SOURCE the target would rerun the roles source and pass;
// without BEADS_HTTP_TEST_REQUIRED or BEADS_TEST_REQUIRE_DOLT_CONTAINER every
// case could skip while the target reports success.
var servedProviderRuleLines = []string{
	`srcs = ["//tools/bazel:go_test_variant.sh"],`,
	`":httpclient_test",`,
	`"//cmd/bd:bd_for_tests",`,
	`"BEADS_HTTP_SERVED_SOURCE": "provider",`,
	`"BEADS_HTTP_TEST_REQUIRED": "1",`,
	`"BEADS_TEST_BD_BINARY": "$(rlocationpath //cmd/bd:bd_for_tests)",`,
	`"BEADS_TEST_DOLT_SERVER": "local",`,
	`"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1",`,
}

// checkServedProviderRule reports every way rule is not the provider-source
// variant servedProviderRuleLines describes.
func checkServedProviderRule(rule string) []string {
	if rule == "" {
		return []string{"internal/httpclient/BUILD.bazel has no httpclient_served_provider_test rule"}
	}
	have := map[string]bool{}
	for _, line := range strings.Split(rule, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var errs []string
	for _, want := range servedProviderRuleLines {
		if !have[want] {
			errs = append(errs, "httpclient_served_provider_test lacks the line "+want)
		}
	}
	// The provider source needs no embedded engine; switching it on would let
	// the embedded-only cases the curated set might grow into run on roles.
	if strings.Contains(rule, "BEADS_TEST_EMBEDDED_DOLT") {
		errs = append(errs, "httpclient_served_provider_test sets BEADS_TEST_EMBEDDED_DOLT; the provider source runs without the embedded engine")
	}
	return errs
}

// servedProviderBDVisibility is the line //cmd/bd:bd_for_tests's visibility
// needs for the variant's data dependency on it to pass Bazel analysis. Nothing
// else text-based notices its absence: the rule's own lines are all present,
// and only `bazel build` refuses the target, which the dolt-server tag puts in
// every //... lane.
const servedProviderBDVisibility = `"//internal/httpclient:__pkg__",`

// checkBDForTestsVisibility reports whether cmd/bd/BUILD.bazel's bd_for_tests
// rule lets //internal/httpclient depend on it.
func checkBDForTestsVisibility(cmdBuild string) []string {
	rule := bazelRuleBlock(cmdBuild, "bd_for_tests")
	if rule == "" {
		return []string{"cmd/bd/BUILD.bazel has no bd_for_tests rule"}
	}
	for _, line := range strings.Split(rule, "\n") {
		if strings.TrimSpace(line) == servedProviderBDVisibility {
			return nil
		}
	}
	return []string{"//cmd/bd:bd_for_tests's visibility lacks " + servedProviderBDVisibility +
		" so //internal/httpclient:httpclient_served_provider_test fails Bazel analysis"}
}

// TestServedProviderVariantIsWired pins the provider-source variant's env, data
// and wrapper, and proves the check fires by dropping each pinned line from the
// real rule.
func TestServedProviderVariantIsWired(t *testing.T) {
	build := readPolicyFile(t, sourceRepoRoot(t), "internal/httpclient/BUILD.bazel")
	rule := bazelRuleBlock(build, "httpclient_served_provider_test")
	if rule == "" {
		t.Fatal("internal/httpclient/BUILD.bazel has no httpclient_served_provider_test rule")
	}
	for _, err := range checkServedProviderRule(rule) {
		t.Error(err)
	}
	// The dolt-server lane runs it, and holds it to that lane's rules (a local
	// server, REQUIRE=1, remote-executable). The tags line stays one line, or
	// TestBazelDoltRaceFitsTheJobsBudget cannot count its shards.
	if !strings.Contains(rule, "\n    tags = [\"dolt-server\"],\n") {
		t.Error(`httpclient_served_provider_test is not tagged ["dolt-server"]; no lane runs it`)
	}
	for _, err := range checkDoltServerRules("internal/httpclient", build) {
		t.Error(err)
	}
	cmdBuild := readPolicyFile(t, sourceRepoRoot(t), "cmd/bd/BUILD.bazel")
	for _, err := range checkBDForTestsVisibility(cmdBuild) {
		t.Error(err)
	}

	// Falsification: the check must report each pinned line's absence, and an
	// embedded switch.
	for _, want := range servedProviderRuleLines {
		var kept []string
		for _, line := range strings.Split(rule, "\n") {
			if strings.TrimSpace(line) != want {
				kept = append(kept, line)
			}
		}
		if len(checkServedProviderRule(strings.Join(kept, "\n"))) == 0 {
			t.Errorf("dropping %q went unreported", want)
		}
	}
	embedded := strings.Replace(rule, "    env = {\n", "    env = {\n        \"BEADS_TEST_EMBEDDED_DOLT\": \"1\",\n", 1)
	if embedded == rule {
		t.Fatal("the embedded-switch mutation did not apply")
	}
	if len(checkServedProviderRule(embedded)) == 0 {
		t.Error("adding BEADS_TEST_EMBEDDED_DOLT went unreported")
	}
	if len(checkServedProviderRule("")) == 0 {
		t.Error("a missing rule went unreported")
	}
	hidden := strings.Replace(cmdBuild, "\n        "+servedProviderBDVisibility, "", 1)
	if hidden == cmdBuild {
		t.Fatal("the hidden-bd_for_tests mutation did not apply")
	}
	if len(checkBDForTestsVisibility(hidden)) == 0 {
		t.Error("dropping //internal/httpclient from bd_for_tests's visibility went unreported")
	}
}
