package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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
//
// nogo validation is duplicated across lanes that compile the same Bazel
// configuration (race: test, embedded, doltserver, doltserver-proxied,
// dolt-race and the package gates). TestLintAndVetRunAsNogo used to forbid
// `run_validations` anywhere in .bazelrc ("every lane validates"); that
// blanket ban is replaced by TestNogoConfigurationsHaveOneValidatingLane
// (T1), TestNogoOwnersAreRequired (T2), TestNogoRaceOwnerBuildsEverything
// (T3), TestNoUnconditionalValidationOff (T5) and
// TestRunValidationsOnlyOnAllowlistedLines (T6), which together pin "every
// configuration's nogo runs in exactly one required lane" instead.
// --norun_validations is a build-request option, not a configuration flag:
// it changes no action key and discards no analysis.

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

// --- F5 S1: one validating lane per nogo configuration -----------------

// nogoKeyFlagPattern matches the build-affecting flags f5-spec.md §6 T1
// calls out as fingerprint-relevant: they select a distinct Bazel
// configuration (and so a distinct set of nogo action keys). Flags like
// --test_tag_filters, --keep_going or --test_arg narrow which tests run but
// do not change what is compiled, so they are not in the fingerprint.
var nogoKeyFlagPattern = regexp.MustCompile(`--@rules_go//go/config:(race|pure|tags=\S+)|--platforms=\S+|//tools/bazel:release_platforms\S*`)

// expandBazelrcConfig returns every "<cmd> <opt>" line (as bazelRCConfigLines
// formats them) that --config=name pulls in, recursively following any
// nested --config=Y reference within those lines (as .bazelrc's own comments
// describe, e.g. test:ci --config=prcore). seen prevents infinite recursion
// on a cycle.
func expandBazelrcConfig(rc, name string, seen map[string]bool) []string {
	if seen[name] {
		return nil
	}
	seen[name] = true
	var out []string
	for _, l := range bazelRCConfigLines(rc, name) {
		if v, ok := strings.CutPrefix(l, "build --config="); ok {
			out = append(out, expandBazelrcConfig(rc, v, seen)...)
			continue
		}
		if v, ok := strings.CutPrefix(l, "test --config="); ok {
			out = append(out, expandBazelrcConfig(rc, v, seen)...)
			continue
		}
		out = append(out, l)
	}
	return out
}

// nogoFingerprint reduces a config's expanded lines to its key-affecting
// flags, sorted and deduplicated, so two configs that build the same thing
// compare equal regardless of line order or non-key-affecting flags.
func nogoFingerprint(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		for _, m := range nogoKeyFlagPattern.FindAllString(l, -1) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// bazelrcConfigSkipsValidations reports whether --config=name's own
// (non-recursive) .bazelrc lines pass --norun_validations. Non-recursive
// because each non-owner config sets the flag itself (D1); inheriting it
// through a nested --config would make the policy table here miss a
// config that silently stopped validating by referencing one that did.
func bazelrcConfigSkipsValidations(rc, name string) bool {
	for _, l := range bazelRCConfigLines(rc, name) {
		if strings.Contains(l, "--norun_validations") {
			return true
		}
	}
	return false
}

// nogoConfigurations: f5-spec.md §6 T1's table. Each row is one Bazel
// configuration (one nogo fingerprint): the .bazelrc --config names (or, for
// the package gates, a pseudo-config checked against their pinned command
// line) that build it, the bazel.yml job that owns validating it, and the
// jobs that must pass --norun_validations instead.
//
// Only the race group is classified here (F5 S1). The race+integration
// group (integration, doltserver-integration, doltserver-cmd) and `pure`'s
// optional D4a are later slices (f5-spec.md §5 S2, S3): every lane in them
// still validates today, so they are intentionally left out of this table
// rather than added as a row that TestNogoConfigurationsHaveOneValidatingLane
// could never satisfy. `pure`, js/wasm and release-cross are unaffected by
// any slice (single-lane configurations: there is no other lane to
// deduplicate against), so they are included as trivial one-member rows for
// completeness and as a guard against a future lane quietly joining them.
var nogoConfigurations = []struct {
	name      string
	configs   []string // .bazelrc --config names sharing this fingerprint, owner first
	owner     string   // bazel.yml job name required to validate
	nonOwners []string // bazel.yml job names required to pass --norun_validations
}{
	{
		name:      "race",
		configs:   []string{"ci", "embedded", "doltserver", "doltserver-proxied", "dolt-race"},
		owner:     bazelJobName,
		nonOwners: []string{bazelEmbedJobName, bazelDoltJobName, bazelProxiedJobName, bazelDoltRaceJobName},
	},
	{
		name:    "pure",
		configs: []string{"pure"},
		owner:   bazelPureJobName,
	},
	{
		name:    "js-wasm",
		configs: []string{"js-wasm"},
		owner:   bazelPureJobName,
	},
}

// nogoPackageGateJobs: the package gates do not select a named .bazelrc
// --config; their `bazel build` command line spells the race flag out
// (pr_lanes_bazel_coverage_test.go pins the exact command). They are race
// non-owners, checked against the "race" row's fingerprint and
// --norun_validations requirement directly from that pinned command.
var nogoPackageGateJobs = []string{bazelPackageMCPJobName, bazelPackageNPMJobName}

// T1 TestNogoConfigurationsHaveOneValidatingLane: every classified Bazel
// configuration has exactly one validating (required) lane; every lane that
// builds the same configuration passes --norun_validations instead; and
// every config within a row shares that row's fingerprint, so a key-affecting
// flag added to a non-owner (which would split it into a different, now
// unvalidated, configuration) fails here instead of silently losing
// coverage.
func TestNogoConfigurationsHaveOneValidatingLane(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	workflow := readCIWorkflow(t, bazelWorkflowName)

	seenConfig := map[string]string{} // config -> row name, to catch a config in two rows
	for _, row := range nogoConfigurations {
		if row.owner == "" {
			t.Fatalf("nogoConfigurations row %q has no owner", row.name)
		}
		if len(row.configs) == 0 {
			t.Fatalf("nogoConfigurations row %q has no configs", row.name)
		}
		ownerFP := nogoFingerprint(expandBazelrcConfig(rc, row.configs[0], map[string]bool{}))
		for _, c := range row.configs {
			if prev, ok := seenConfig[c]; ok {
				t.Errorf("--config=%s appears in both row %q and row %q", c, prev, row.name)
			}
			seenConfig[c] = row.name
			fp := nogoFingerprint(expandBazelrcConfig(rc, c, map[string]bool{}))
			if !sameStringSet(fp, ownerFP) {
				t.Errorf("row %q: --config=%s fingerprint %v != owner %s's %v (a key-affecting flag split this config out of the row)",
					row.name, c, fp, row.configs[0], ownerFP)
			}
		}

		ownerJob := workflow.job(t, row.owner)
		if ownerJob.ContinueOnError {
			t.Errorf("row %q owner %s has continue-on-error; a skipped finding there would never fail anything", row.name, row.owner)
		}
		if bazelrcConfigSkipsValidations(rc, row.configs[0]) {
			t.Errorf("row %q owner config --config=%s passes --norun_validations; it is the row's only validating lane", row.name, row.configs[0])
		}

		nonOwners := append([]string{}, row.nonOwners...)
		if row.name == "race" {
			nonOwners = append(nonOwners, nogoPackageGateJobs...)
		}
		for i, job := range nonOwners {
			if job == row.owner {
				t.Errorf("row %q lists its own owner %s as a non-owner", row.name, job)
			}
			isPackageGate := slices.Contains(nogoPackageGateJobs, job)
			if isPackageGate {
				// The package gates spell their flags out on the command
				// line rather than through a named --config
				// (pr_lanes_bazel_coverage_test.go pins the exact string);
				// confirmed here against their job's steps instead of
				// .bazelrc.
				step := workflow.job(t, job).step(t, "bazel build //cmd/bd:bd_for_tests")
				if !strings.Contains(step.Run, "--@rules_go//go/config:race") {
					t.Errorf("row %q non-owner %s's build step does not build the race configuration: %q", row.name, job, step.Run)
				}
				if !strings.Contains(step.Run, "--norun_validations") {
					t.Errorf("row %q non-owner %s's build step does not pass --norun_validations", row.name, job)
				}
				continue
			}
			// Map job -> its own --config, which is row.configs[1+i'] in
			// declaration order (owner's config is row.configs[0]).
			idx := i + 1
			if idx >= len(row.configs) {
				t.Fatalf("row %q has more non-owner jobs than configs (idx %d, configs %v)", row.name, idx, row.configs)
			}
			cfg := row.configs[idx]
			if !bazelrcConfigSkipsValidations(rc, cfg) {
				t.Errorf("row %q non-owner %s (--config=%s) does not pass --norun_validations; it duplicates %s's nogo", row.name, job, cfg, row.owner)
			}
		}
	}
}

// T2 TestNogoOwnersAreRequired: each owner job is required by pr.yml's CI
// Gate (or, for a flag-gated lane, the flag that is pinned true) with no
// job-level continue-on-error, and its `if:` runs at least everywhere its
// non-owners' do — otherwise a mode could run a non-owner's compile with no
// owner validating it at all (f5-spec.md §9).
func TestNogoOwnersAreRequired(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	gate := readCIWorkflow(t, "pr.yml").job(t, "ci-gate")
	gateEnv := gate.step(t, "Evaluate CI gate").Env
	required := strings.Fields(gateEnv["CI_GATE_REQUIRED"])

	ownerGateToken := map[string]string{
		bazelJobName:     "BAZEL_TEST",
		bazelPureJobName: "BAZEL_PURE",
	}

	events := []string{"pull_request", "merge_group", "pull_request_target", "push", "schedule", "workflow_dispatch"}

	for _, row := range nogoConfigurations {
		ownerJob := workflow.job(t, row.owner)
		if token, ok := ownerGateToken[row.owner]; ok && !contains(required, token) {
			t.Errorf("row %q owner %s: CI_GATE_REQUIRED %v lacks %s", row.name, row.owner, required, token)
		}
		if ownerJob.ContinueOnError {
			t.Errorf("row %q owner %s has continue-on-error", row.name, row.owner)
		}

		nonOwners := append([]string{}, row.nonOwners...)
		if row.name == "race" {
			nonOwners = append(nonOwners, nogoPackageGateJobs...)
		}
		for _, job := range nonOwners {
			if slices.Contains(nogoPackageGateJobs, job) {
				// The package gates' bazel build only ever runs in mode
				// remote (TestPackageGateJobs pins the surrounding `if:`),
				// and the owner's `if:` (mode != 'skip') is true whenever
				// mode == 'remote': "remote" is never "skip".
				step := workflow.job(t, job).step(t, "bazel build //cmd/bd:bd_for_tests")
				if !strings.Contains(step.If, "mode == 'remote'") {
					t.Errorf("%s's bazel-build step if %q no longer implies the owner's mode != 'skip'", job, step.If)
				}
				continue
			}
			nonOwnerJob := workflow.job(t, job)
			for _, mode := range bazelRBEModes {
				for _, event := range events {
					ctx := map[string]string{
						"needs.rbe.outputs.mode":    mode,
						"needs.rbe.outputs.enabled": bazelModeEnabled(mode),
						"github.event_name":         event,
					}
					if evalRRCIf(t, nonOwnerJob.If, ctx) && !evalRRCIf(t, ownerJob.If, ctx) {
						t.Errorf("row %q: %s runs in mode=%s event=%s but owner %s does not (owner if %q, non-owner if %q)",
							row.name, job, mode, event, row.owner, ownerJob.If, nonOwnerJob.If)
					}
				}
			}
		}
	}
}

// T3 TestNogoRaceOwnerBuildsEverything: the race owner's target set covers
// every file any race-group lane compiles (f5-spec.md §2.1): its test step
// runs over the whole tree with no "-//" exclusion, and test:ci's expansion
// narrows no target selection (--build_tests_only, --build_tag_filters)
// — only test execution (--test_tag_filters etc., which T1's fingerprint
// deliberately ignores).
func TestNogoRaceOwnerBuildsEverything(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	ownerJob := workflow.job(t, bazelJobName)
	test := ownerJob.step(t, "bazel test //... --config=ci")
	if !strings.Contains(test.Run, "bazel test //... --config=ci") {
		t.Fatalf("%s step does not run bazel test //... --config=ci:\n%s", bazelJobName, test.Run)
	}
	if regexp.MustCompile(`bazel test[^\n]*\s-//`).MatchString(test.Run) {
		t.Errorf("%s excludes a target (-//...) from //...: it would stop validating what it excludes", bazelJobName)
	}
	// The race owner's validating step must be unconditional: a step-level
	// `if:` or continue-on-error here would let the suite report green while
	// race-group nogo silently did not run, with nothing else in this file
	// (or T1/T2, which only check job-level fields) noticing.
	if test.If != "" {
		t.Errorf("%s step %q has if %q; the race owner's validating step must be unconditional", bazelJobName, test.Name, test.If)
	}
	if test.ContinueOnError != nil && test.ContinueOnError != false {
		t.Errorf("%s step %q has continue-on-error %v; a failed race-group nogo finding there would never fail the job", bazelJobName, test.Name, test.ContinueOnError)
	}
	if ownerJob.ContinueOnError {
		t.Errorf("%s job has continue-on-error; the race owner's job must be able to fail", bazelJobName)
	}

	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for _, l := range expandBazelrcConfig(rc, "ci", map[string]bool{}) {
		if strings.Contains(l, "build_tests_only") || strings.Contains(l, "build_tag_filters") {
			t.Errorf("test:ci (or a config it includes) narrows the target set: %q; the race owner must build //... whole", l)
		}
	}
}

// T5 TestNoUnconditionalValidationOff: no common/build/test line without a
// :config suffix touches run_validations (that would turn validation off
// everywhere, defeating every row's owner), and neither does build:nogo*,
// build:release-cross, build:js-wasm, scripts/ci/bazel-release-cross-compile.sh,
// or any row's own owner config (an owner opting itself out would leave its
// configuration with no validating lane at all).
func TestNoUnconditionalValidationOff(t *testing.T) {
	root := sourceRepoRoot(t)
	rc := readPolicyFile(t, root, ".bazelrc")

	for _, raw := range strings.Split(rc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, _, _ := strings.Cut(line, " ")
		cmd, _, hasCfg := strings.Cut(head, ":")
		if cmd != "common" && cmd != "build" && cmd != "test" {
			continue
		}
		if !hasCfg && strings.Contains(line, "run_validations") {
			t.Errorf(".bazelrc %q: an unconfigured line must not touch run_validations; it would apply to every lane, including every row's owner", line)
		}
	}

	for _, prefix := range []string{"build:nogo", "build:release-cross", "build:js-wasm"} {
		for _, raw := range strings.Split(rc, "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, prefix) && strings.Contains(line, "validation") {
				t.Errorf(".bazelrc %q changes validation for %s, which has no non-owner duplicate to remove", line, prefix)
			}
		}
	}

	if script := readPolicyFile(t, root, "scripts/ci/bazel-release-cross-compile.sh"); strings.Contains(script, "run_validations") {
		t.Error("scripts/ci/bazel-release-cross-compile.sh turns nogo validation off; release-cross has no non-owner duplicate to remove")
	}

	for _, row := range nogoConfigurations {
		if bazelrcConfigSkipsValidations(rc, row.configs[0]) {
			t.Errorf("row %q owner config --config=%s passes --norun_validations; it is the row's only validating lane", row.name, row.configs[0])
		}
	}
}

// T6 TestRunValidationsOnlyOnAllowlistedLines: the only
// run_validations/norun_validations lines anywhere in .bazelrc or bazel.yml
// are exactly the ones T1's race row already requires: one
// "test:<non-owner config> --norun_validations" line per non-owner in
// .bazelrc, and the two package-gate jobs' bazel-build steps. Main's
// TestLintAndVetRunAsNogo used to ban run_validations outright, which caught
// a configuration outside T1's table (e.g. test:integration,
// test:doltserver-cmd -- the race+integration group is a later slice,
// f5-spec.md §5 S2) quietly adding --norun_validations with no owner to
// notice. T5 does not re-check that case: it only forbids unconfigured
// lines and a short prefix list. This allowlist restores the ban for every
// other line without re-banning the four the race row requires.
func TestRunValidationsOnlyOnAllowlistedLines(t *testing.T) {
	root := sourceRepoRoot(t)

	var raceNonOwners []string
	for _, row := range nogoConfigurations {
		if row.name == "race" {
			raceNonOwners = row.configs[1:]
		}
	}
	if len(raceNonOwners) == 0 {
		t.Fatal("race row has no non-owner configs to allowlist")
	}
	allowed := map[string]bool{}
	for _, cfg := range raceNonOwners {
		allowed["test:"+cfg+" --norun_validations"] = true
	}

	rc := readPolicyFile(t, root, ".bazelrc")
	for _, raw := range strings.Split(rc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "run_validations") && !allowed[line] {
			t.Errorf(".bazelrc %q: run_validations is only allowed as one of %v; any other line (an unlisted config, an --integration config, or a bare --run_validations toggle) would turn validation off with no owner lane catching it", line, sortedKeys(allowed))
		}
	}

	// bazel.yml: only the two package-gate jobs' bazel-build steps may pass
	// --norun_validations (they spell the race build flags out on the
	// command line rather than through a .bazelrc --config; T1 pins their
	// content). Anywhere else -- a new job, a new step, a non-package-gate
	// job -- would turn a lane's validation off unnoticed.
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for name, j := range workflow.Jobs {
		for _, st := range j.Steps {
			if !strings.Contains(st.Run, "run_validations") {
				continue
			}
			if !slices.Contains(nogoPackageGateJobs, name) {
				stepLabel := st.Name
				if stepLabel == "" {
					stepLabel = st.Uses
				}
				t.Errorf("%s job %q step %q: run_validations outside the package-gate build steps", bazelWorkflowName, name, stepLabel)
			}
		}
	}
}
