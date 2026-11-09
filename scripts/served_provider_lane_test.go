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
}
