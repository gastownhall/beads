package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
// configuration's nogo runs in exactly one required lane" instead;
// TestNogoPolicyRejectsMutations pins that T1, T3, T5 and T6 reject known-bad
// .bazelrc/bazel.yml edits.
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

// nogoPolicyInputs is what T1, T3, T5 and T6 check: .bazelrc's text and
// bazel.yml parsed. TestNogoPolicyRejectsMutations feeds them mutated copies.
type nogoPolicyInputs struct {
	rc       string
	workflow ciWorkflow
}

func readNogoPolicyInputs(t *testing.T) nogoPolicyInputs {
	t.Helper()
	return nogoPolicyInputs{
		rc:       readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"),
		workflow: readCIWorkflow(t, bazelWorkflowName),
	}
}

// nogoKeyFlags returns the build-affecting flags f5-spec.md §6 T1 calls out
// as fingerprint-relevant among line's whitespace-separated tokens: they
// select a distinct Bazel configuration (and so a distinct set of nogo action
// keys). Flags like --test_tag_filters, --keep_going or --test_arg narrow
// which tests run but do not change what is compiled, so they are not in the
// fingerprint. Each token is parsed whole (flag name, then value), never
// matched by prefix: --@rules_go//go/config:race=false is not the race
// configuration, and must not fingerprint as --@rules_go//go/config:race.
// Boolean flags are normalized to name=true / name=false (a bare
// --@rules_go//go/config:race is race=true; --no@rules_go//go/config:race is
// race=false).
func nogoKeyFlags(line string) []string {
	var out []string
	for _, tok := range strings.Fields(line) {
		name, value, hasValue := strings.Cut(tok, "=")
		switch name {
		case "--@rules_go//go/config:race", "--@rules_go//go/config:pure":
			if !hasValue {
				value = "true"
			}
			out = append(out, name+"="+value)
		case "--no@rules_go//go/config:race", "--no@rules_go//go/config:pure":
			if !hasValue {
				out = append(out, "--"+strings.TrimPrefix(name, "--no")+"=false")
			}
		case "--@rules_go//go/config:tags", "--platforms", "--//tools/bazel:release_platforms":
			out = append(out, tok)
		}
	}
	return out
}

// expandBazelrcConfig returns every "<cmd> <opt>" line (as bazelRCConfigLines
// formats them) that --config=name pulls in, recursively following any
// nested --config=Y reference within those lines (as .bazelrc's own comments
// describe, e.g. test:ci --config=prcore). seen prevents infinite recursion
// on a cycle.
//
// A nested reference is recognized wherever a --config= token appears: on a
// "common:" line (common applies to build and test alike, so common:ci
// --config=prcore pulls prcore in exactly as test:ci --config=prcore would),
// and anywhere among a line's options, not only as the first one (a line
// "build:ci --stripopt=foo --config=prcore" pulls prcore in too). An earlier
// version matched only a whole line beginning "build --config=" or "test
// --config=", so a --config= reference on a common: line, or not in first
// position, silently expanded to nothing (review of #7482, should-fix 1).
func expandBazelrcConfig(rc, name string, seen map[string]bool) []string {
	if seen[name] {
		return nil
	}
	seen[name] = true
	var out []string
	for _, l := range bazelRCConfigLines(rc, name) {
		out = append(out, l)
		for _, tok := range strings.Fields(l) {
			if v, ok := strings.CutPrefix(tok, "--config="); ok {
				out = append(out, expandBazelrcConfig(rc, v, seen)...)
			}
		}
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
		for _, m := range nogoKeyFlags(l) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// buildOrCommonLines drops expandBazelrcConfig's test:-sourced lines (those
// are returned as "test <opt>", per bazelRCConfigLines), keeping "build
// <opt>" and "common <opt>" ones. Used wherever a fingerprint stands in for
// a `bazel build` invocation (bee-ghosttrack review of #7486, should-fix):
// such an invocation never reads a test: line, so including one there would
// let a step's fingerprint agree with a configuration's full (test-inclusive)
// fingerprint for a flag the step's own command would never actually see.
func buildOrCommonLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "test ") {
			out = append(out, l)
		}
	}
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

// bazelrcConfigExpansionSkipsValidations reports whether --config=name, with
// every --config it pulls in followed recursively, passes
// --norun_validations. Owners are checked this way: an owner's validation is
// off if any config in its chain (test:ci -> test:prcore, or a config the
// owner's command line adds, like sole-run) turns it off.
func bazelrcConfigExpansionSkipsValidations(rc, name string) bool {
	for _, l := range expandBazelrcConfig(rc, name, map[string]bool{}) {
		if strings.Contains(l, "--norun_validations") {
			return true
		}
	}
	return false
}

// bazelInvocations lives in bazel_invocations_test.go (a helper-only file,
// shared with nogo_integration_scan_test.go's dedicated go_test target;
// independent review of #7486, cost fix).

var (
	shellVarRefPattern   = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
	configInValuePattern = regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`)
)

// jobBazelConfigs returns every --config name job's `bazel test`/`bazel
// build` commands select: spelled out (--config=X or --config X), or through
// a shell variable the step, job or workflow env defines (bazel.yml's
// BAZEL_SOLE_RUN and BAZEL_FRESH), in which case every --config=X the
// variable's expression can produce counts. This is the job's real
// job -> config mapping, read from what it runs, not from table order.
func jobBazelConfigs(workflow ciWorkflow, job ciWorkflowJob) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, st := range job.Steps {
		for _, args := range bazelInvocations(st.Run, "test", "build") {
			for i, a := range args {
				if v, ok := strings.CutPrefix(a, "--config="); ok {
					add(v)
				} else if a == "--config" && i+1 < len(args) {
					add(args[i+1])
				}
				for _, ref := range shellVarRefPattern.FindAllStringSubmatch(a, -1) {
					value, ok := st.Env[ref[1]]
					if !ok {
						value, ok = job.Env[ref[1]]
					}
					if !ok {
						value = workflow.Env[ref[1]]
					}
					for _, m := range configInValuePattern.FindAllStringSubmatch(value, -1) {
						add(m[1])
					}
				}
			}
		}
	}
	return out
}

// checkNonOwnerCommandLineAddsNoKeyFlag checks a job's own bazel command
// line: whichever of its `test`/`build` invocations selects one of
// laneConfigs (literally, by --config=X or --config X -- not only through a
// shell variable; jobBazelConfigs already resolved those into laneConfigs)
// must add no key-affecting flag (nogoKeyFlags) beyond ownerFP. A lane's
// --config=X already has its own .bazelrc fingerprint pinned equal to the
// owner's elsewhere (the row's per-config fingerprint check, or, for a
// nogo-only-step row, checkNogoOnlyStepRow's own fingerprint check) -- but
// that only reads .bazelrc text. A job could still pass a key-affecting flag
// directly on its own invocation, e.g. `--config=embedded
// --@rules_go//go/config:race=false`, or `--config=doltserver-cmd
// --@rules_go//go/config:tags=gms_pure_go,integration,extra`, overriding
// what --config=X's own .bazelrc lines say while still "running" the config
// name every other check here expects, silently building a different
// configuration than the one being deduplicated against the owner
// (independent review of #7486, must-fix 7).
//
// job is usually a true non-owner, but for a nogo-only-step row
// (checkNogoOnlyStepRow) it is also called with job set to the row's own
// owner: under Variant A no full lane validates that row's configuration at
// all, so the owner's own full lane (its `bazel test //...` step, as
// distinct from the nogo-only step that actually validates) is, for this
// purpose, just another lane that must not silently drift onto a different
// configuration (independent review of #7486, must-fix 3). The error message
// below says only the job's name, not "owner" or "non-owner", since it
// applies to either role identically.
func checkNonOwnerCommandLineAddsNoKeyFlag(t *testing.T, workflow ciWorkflow, rowName, job string, laneConfigs, ownerFP []string, errorf func(string, ...any)) {
	t.Helper()
	for _, st := range workflow.job(t, job).Steps {
		for _, args := range bazelInvocations(st.Run, "test", "build") {
			selectsLane := false
			for i, a := range args {
				if v, ok := strings.CutPrefix(a, "--config="); ok && slices.Contains(laneConfigs, v) {
					selectsLane = true
				} else if a == "--config" && i+1 < len(args) && slices.Contains(laneConfigs, args[i+1]) {
					selectsLane = true
				}
			}
			if !selectsLane {
				continue
			}
			for _, f := range nogoKeyFlags(strings.Join(args, " ")) {
				if !slices.Contains(ownerFP, f) {
					errorf("row %q %s's bazel command line passes %s directly (not through its --config's own .bazelrc lines); its nogo is no longer provably redundant with the row's owner", rowName, job, f)
				}
			}
		}
	}
}

// nogoConfigurationRow is one row of nogoConfigurations: one Bazel
// configuration (one nogo fingerprint).
type nogoConfigurationRow struct {
	name      string
	configs   []string // .bazelrc --config names sharing this fingerprint, owner first
	owner     string   // bazel.yml job name required to validate
	nonOwners []string // bazel.yml job names required to pass --norun_validations
	// ownerIsNogoOnlyStep: f5-spec.md §2.3/§5 S2 Variant A. True for a row
	// where no full lane validates the configuration at all -- every config
	// in the row, owner's own full lane included, passes --norun_validations
	// -- and the owner job instead runs a narrow `bazel build
	// --output_groups=nogo_fix` step that rebuilds only the configuration's
	// non-test library targets. checkNogoConfigurationsHaveOneValidatingLane
	// branches on this instead of requiring the owner's full lane to
	// validate, which Variant A's owner deliberately does not do.
	ownerIsNogoOnlyStep bool
	// nogoOnlyStepTargets: required when ownerIsNogoOnlyStep. The nogo-only
	// step's exact bazel target label(s), pinned literally (independent
	// review of #7486, must-fix 2): this file's go_test target has no
	// //:repo_go_srcs (cost fix, same review), so it cannot re-derive the
	// label from source the way nogo_integration_scan_test.go's own
	// TestNogoIntegrationStepCoversItsConfiguration does, and must be kept
	// in sync with that scan by hand. A package-only comparison would accept
	// the step retargeted at a different rule in the same package (e.g.
	// :repo_doc_files), silently validating nothing.
	nogoOnlyStepTargets []string
}

// nogoConfigurations: f5-spec.md §6 T1's table. Each row is one Bazel
// configuration (one nogo fingerprint): the .bazelrc --config names (or, for
// the package gates, a pseudo-config checked against their pinned command
// line) that build it, the bazel.yml job that owns validating it, and the
// jobs that must pass --norun_validations instead.
//
// The race group (F5 S1) and the race+integration group (F5 S2 Variant A:
// integration, doltserver-integration, doltserver-cmd -- same fingerprint,
// race=true plus tags=gms_pure_go,integration) are both classified here.
// Under Variant A no full lane validates the integration group at all (every
// member config passes --norun_validations); its owner is instead the
// bazel-integration job's narrow nogo-only step over the group's non-test
// library package, //internal/testutil/integration (ownerIsNogoOnlyStep;
// f5-spec.md §2.3). `pure`'s optional D4a split is a later slice (f5-spec.md
// §5 S3): it still validates in every lane today, so it is intentionally
// left out of this table rather than added as a row that
// TestNogoConfigurationsHaveOneValidatingLane could never satisfy. `pure`,
// js/wasm and release-cross are unaffected by any slice (single-lane
// configurations: there is no other lane to deduplicate against), so they
// are included as trivial one-member rows for completeness and as a guard
// against a future lane quietly joining them.
var nogoConfigurations = []nogoConfigurationRow{
	{
		name:      "race",
		configs:   []string{"ci", "embedded", "doltserver", "doltserver-proxied", "dolt-race"},
		owner:     bazelJobName,
		nonOwners: []string{bazelEmbedJobName, bazelDoltJobName, bazelProxiedJobName, bazelDoltRaceJobName},
	},
	{
		name:                "integ",
		configs:             []string{"integration", "doltserver-integration", "doltserver-cmd", "dolt-int"},
		owner:               bazelIntegJobName,
		nonOwners:           []string{bazelServerJobName, bazelCmdDoltJobName, bazelDoltIntJobName},
		ownerIsNogoOnlyStep: true,
		nogoOnlyStepTargets: []string{"//internal/testutil/integration:integration"},
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
	checkNogoConfigurationsHaveOneValidatingLane(t, readNogoPolicyInputs(t), t.Errorf)
}

func checkNogoConfigurationsHaveOneValidatingLane(t *testing.T, in nogoPolicyInputs, errorf func(string, ...any)) {
	t.Helper()
	rc, workflow := in.rc, in.workflow

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
				errorf("--config=%s appears in both row %q and row %q", c, prev, row.name)
			}
			seenConfig[c] = row.name
			fp := nogoFingerprint(expandBazelrcConfig(rc, c, map[string]bool{}))
			if !sameStringSet(fp, ownerFP) {
				errorf("row %q: --config=%s fingerprint %v != owner %s's %v (a key-affecting flag split this config out of the row)",
					row.name, c, fp, row.configs[0], ownerFP)
			}
		}

		if row.ownerIsNogoOnlyStep {
			checkNogoOnlyStepRow(t, rc, workflow, row, ownerFP, errorf)
			continue
		}

		ownerJob := workflow.job(t, row.owner)
		if ownerJob.ContinueOnError {
			errorf("row %q owner %s has continue-on-error; a skipped finding there would never fail anything", row.name, row.owner)
		}
		// Every config the owner's bazel commands select, each expanded
		// recursively: test:ci -> test:prcore, and the sole-run/fresh configs
		// bazel.yml adds through BAZEL_SOLE_RUN/BAZEL_FRESH. Any of them
		// passing --norun_validations (itself or through a --config it pulls
		// in) leaves the row with no validating lane.
		ownerConfigs := jobBazelConfigs(workflow, ownerJob)
		if !slices.Contains(ownerConfigs, row.configs[0]) {
			errorf("row %q owner %s runs no bazel test/build with --config=%s (its commands select %v)", row.name, row.owner, row.configs[0], ownerConfigs)
		}
		for _, c := range ownerConfigs {
			if bazelrcConfigExpansionSkipsValidations(rc, c) {
				errorf("row %q owner %s's --config=%s (or a config it includes) passes --norun_validations; the owner is the row's only validating lane", row.name, row.owner, c)
			}
		}

		nonOwners := append([]string{}, row.nonOwners...)
		if row.name == "race" {
			nonOwners = append(nonOwners, nogoPackageGateJobs...)
		}
		usedConfigs := map[string]bool{}
		for _, job := range nonOwners {
			if job == row.owner {
				errorf("row %q lists its own owner %s as a non-owner", row.name, job)
			}
			isPackageGate := slices.Contains(nogoPackageGateJobs, job)
			if isPackageGate {
				// The package gates spell their flags out on the command
				// line rather than through a named --config
				// (pr_lanes_bazel_coverage_test.go pins the exact string);
				// confirmed here against their job's steps instead of
				// .bazelrc. The step's single `bazel build` invocation is
				// parsed into tokens and fingerprinted exactly as a
				// .bazelrc --config is (nogoFingerprint), rather than
				// substring-matched against the raw run text: a substring
				// check would accept --@rules_go//go/config:race=false (it
				// contains the race=true flag's text as a prefix) and would
				// never notice an unrelated key-affecting flag, like
				// --platforms, drifting between this step and the row's
				// owner (review of #7482, should-fix 3).
				step := workflow.job(t, job).step(t, "bazel build //cmd/bd:bd_for_tests")
				invocations := bazelInvocations(step.Run, "build")
				if len(invocations) != 1 {
					errorf("row %q non-owner %s's build step runs %d `bazel build` commands, want exactly 1", row.name, job, len(invocations))
					continue
				}
				args := invocations[0]
				fp := nogoFingerprint([]string{"build " + strings.Join(args, " ")})
				if !sameStringSet(fp, ownerFP) {
					errorf("row %q non-owner %s's build step fingerprint %v != owner %s's %v", row.name, job, fp, row.configs[0], ownerFP)
				}
				if !slices.Contains(args, "--norun_validations") {
					errorf("row %q non-owner %s's build step does not pass --norun_validations (as its own argument)", row.name, job)
				}
				continue
			}
			// Map job -> the row config(s) its own bazel commands select.
			jobConfigs := jobBazelConfigs(workflow, workflow.job(t, job))
			if slices.Contains(jobConfigs, row.configs[0]) {
				errorf("row %q non-owner %s runs the owner's --config=%s; it duplicates %s's nogo", row.name, job, row.configs[0], row.owner)
			}
			var laneConfigs []string
			for _, c := range jobConfigs {
				if slices.Contains(row.configs[1:], c) {
					laneConfigs = append(laneConfigs, c)
				}
			}
			if len(laneConfigs) == 0 {
				errorf("row %q non-owner %s runs none of the row's non-owner configs %v (its commands select %v)", row.name, job, row.configs[1:], jobConfigs)
			}
			for _, cfg := range laneConfigs {
				usedConfigs[cfg] = true
				if !bazelrcConfigSkipsValidations(rc, cfg) {
					errorf("row %q non-owner %s (--config=%s) does not pass --norun_validations; it duplicates %s's nogo", row.name, job, cfg, row.owner)
				}
			}

			// A lane's .bazelrc config fingerprint was already pinned equal
			// to the owner's, above (the per-row.configs loop). But that
			// only checks .bazelrc text: a job could still pass a
			// key-affecting flag directly on its own bazel command line,
			// overriding what --config=cfg's own lines say while still
			// "running" the config name this loop expects, with nothing
			// above noticing (independent review of #7486, must-fix 7).
			checkNonOwnerCommandLineAddsNoKeyFlag(t, workflow, row.name, job, laneConfigs, ownerFP, errorf)
		}
		for _, c := range row.configs[1:] {
			if !usedConfigs[c] {
				errorf("row %q config --config=%s is run by none of its non-owner jobs %v", row.name, c, row.nonOwners)
			}
		}
	}
}

// checkNogoOnlyStepRow checks a row (f5-spec.md §2.3, S2 Variant A) whose
// owner is not a full validating lane but a narrow nogo-only `bazel build`
// step: every row.configs member -- the nominal owner's own full lane
// included -- must pass --norun_validations (Variant A: no full lane
// validates this configuration at all), and the owner job must run exactly
// one `bazel build --output_groups=nogo_fix` step whose fingerprint matches
// the row's, unconditionally capable of failing the job, so it stays the
// configuration's one validating lane and a drifting flag cannot quietly
// make it validate something else.
func checkNogoOnlyStepRow(t *testing.T, rc string, workflow ciWorkflow, row nogoConfigurationRow, ownerFP []string, errorf func(string, ...any)) {
	t.Helper()
	for _, c := range row.configs {
		if !bazelrcConfigSkipsValidations(rc, c) {
			errorf("row %q (nogo-only-step owner): --config=%s does not pass --norun_validations; under Variant A no full lane validates this configuration, only %s's nogo-only step does", row.name, c, row.owner)
		}
	}

	ownerJob := workflow.job(t, row.owner)
	if ownerJob.ContinueOnError {
		errorf("row %q owner %s has continue-on-error; a failed nogo-only step there would never fail the job", row.name, row.owner)
	}

	var nogoSteps []ciWorkflowStep
	for _, st := range ownerJob.Steps {
		for _, args := range bazelInvocations(st.Run, "build") {
			if slices.Contains(args, "--output_groups=nogo_fix") {
				nogoSteps = append(nogoSteps, st)
			}
		}
	}
	if len(nogoSteps) != 1 {
		errorf("row %q: owner %s has %d `bazel build --output_groups=nogo_fix` steps, want exactly 1; it is this configuration's only validating lane", row.name, row.owner, len(nogoSteps))
		return
	}
	step := nogoSteps[0]

	if step.ContinueOnError != nil && step.ContinueOnError != false {
		errorf("row %q: owner %s's nogo-only step %q has continue-on-error; a finding there would never fail the job", row.name, row.owner, step.Name)
	}
	if !strings.Contains(step.Run, "--keep_going") {
		errorf("row %q: owner %s's nogo-only step %q has no --keep_going; one finding would hide another", row.name, row.owner, step.Name)
	}

	// The step's `if:` must be pinned exactly: it has to still run after the
	// job's own test step fails (always()), but not after that step was
	// itself skipped (steps.<id>.outcome != 'skipped') -- anything looser (a
	// bare `if: ${{ false }}`, or a condition that only evaluates true for
	// some events, like `github.event_name == 'push'`) would let the step
	// silently stop running, or run only sometimes, with nothing else here
	// noticing it had become conditional on something other than the test
	// step's own outcome.
	var testStepID string
	for _, st := range ownerJob.Steps {
		if st.ID == "test" {
			testStepID = st.ID
			break
		}
	}
	if testStepID == "" {
		errorf("row %q: owner %s has no step with id \"test\" to gate the nogo-only step's `if:` on", row.name, row.owner)
	} else {
		wantIf := fmt.Sprintf("${{ always() && steps.%s.outcome != 'skipped' }}", testStepID)
		if step.If != wantIf {
			errorf("row %q: owner %s's nogo-only step %q has if %q, want exactly %q; a looser or narrower condition could stop it running, or run it only for some events, with nothing else here noticing", row.name, row.owner, step.Name, step.If, wantIf)
		}
	}

	// The step's shell must be bash's own default (pipefail is on by
	// default for GitHub's bash, but this file does not rely on that: it
	// requires an explicit `set -o pipefail` below too, so the step still
	// fails correctly even if that default ever changes or the step's shell
	// is overridden to something pipefail does not apply to).
	if step.Shell != "" && step.Shell != "bash" {
		errorf("row %q: owner %s's nogo-only step %q sets shell=%q; only bash is pinned here", row.name, row.owner, step.Name, step.Shell)
	}
	hasPipefail := false
	for _, l := range strings.Split(step.Run, "\n") {
		if strings.TrimSpace(l) == "set -o pipefail" {
			hasPipefail = true
			break
		}
	}
	if !hasPipefail {
		errorf("row %q: owner %s's nogo-only step %q has no \"set -o pipefail\"; its `bazel build | tee` pipeline would report tee's exit code (0) instead of bazel's, so a nogo finding would never fail the step", row.name, row.owner, step.Name)
	}
	for _, l := range strings.Split(step.Run, "\n") {
		if strings.Contains(l, "tee") && strings.Contains(l, "bazel-nogo-") && strings.Contains(l, "|| true") {
			errorf("row %q: owner %s's nogo-only step %q line %q swallows bazel's exit code with \"|| true\" instead of capturing it (\"|| rc=$?\")", row.name, row.owner, step.Name, strings.TrimSpace(l))
		}
	}
	runLines := strings.Split(strings.TrimRight(step.Run, "\n"), "\n")
	if last := strings.TrimSpace(runLines[len(runLines)-1]); last != `exit "$rc"` {
		errorf(`row %q: owner %s's nogo-only step %q does not end with exit "$rc" (ends %q); a dropped or hardcoded exit would let a finding pass`, row.name, row.owner, step.Name, last)
	}

	var lines, targets []string
	for _, args := range bazelInvocations(step.Run, "build") {
		if !slices.Contains(args, "--output_groups=nogo_fix") {
			continue
		}
		lines = append(lines, "build "+strings.Join(args, " "))
		// The command line's own --config=X (e.g. --config=integration)
		// pulls in its .bazelrc flags (build:integration's tags, here) the
		// same way an owner config's does; expanded recursively so a
		// nested --config reference is not missed either. Only the
		// build:/common: lines it pulls in: this step is `bazel build`,
		// which never reads a test: line (bee-ghosttrack review of #7486,
		// should-fix). .bazelrc's own test:integration sets this
		// configuration's race flag (there is no build:integration race
		// line), so if this included test: lines too, a step that dropped
		// its own explicit --@rules_go//go/config:race (this row's only
		// real source of that flag for a `bazel build`) would still
		// fingerprint as race-on from the never-applicable test: line,
		// silently validating an unowned configuration while every check
		// here kept passing.
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--config="); ok {
				lines = append(lines, buildOrCommonLines(expandBazelrcConfig(rc, v, map[string]bool{}))...)
			}
			// The step's exact target label(s) (independent review of
			// #7486, must-fix 2): a "-//" exclusion pattern is rejected
			// outright, as TestNogoRaceOwnerBuildsEverything (T3) already
			// does for the race owner's own target set, and the remaining
			// "//" labels are compared to row.nogoOnlyStepTargets exactly
			// below -- not just by package -- so a step retargeted at a
			// different rule in the same package (e.g. :repo_doc_files)
			// does not pass a package-only comparison while silently
			// validating nothing.
			switch {
			case strings.HasPrefix(a, "-//"):
				errorf("row %q: owner %s's nogo-only step %q excludes a target (%s) with a negative pattern; every member of %v must be built, not excluded", row.name, row.owner, step.Name, a, row.nogoOnlyStepTargets)
			case strings.HasPrefix(a, "//"):
				targets = append(targets, a)
			}
		}
	}
	if !sameStringSet(targets, row.nogoOnlyStepTargets) {
		errorf("row %q: owner %s's nogo-only step %q targets %v, want exactly %v; a retargeted or additional label would silently stop validating, or validate, the wrong thing", row.name, row.owner, step.Name, targets, row.nogoOnlyStepTargets)
	}

	fp := nogoFingerprint(lines)
	if !sameStringSet(fp, ownerFP) {
		errorf("row %q: owner %s's nogo-only step fingerprint %v != --config=%s's %v; it must build the configuration it is meant to validate", row.name, row.owner, fp, row.configs[0], ownerFP)
	}

	// Every config in the row, including the owner's own (row.configs[0]),
	// must actually be run by the job listed for it -- the owner job itself
	// for row.configs[0] -- read from what each job's bazel commands
	// select, not from table order, exactly as the full-lane case checks
	// its non-owners.
	jobFor := func(job string) []string { return jobBazelConfigs(workflow, workflow.job(t, job)) }
	if !slices.Contains(jobFor(row.owner), row.configs[0]) {
		errorf("row %q owner %s runs no bazel test/build with --config=%s (its commands select %v)", row.name, row.owner, row.configs[0], jobFor(row.owner))
	}
	// The owner job's own full lane (its `bazel test //... --config=X` step,
	// not the nogo-only step just checked above) is, under Variant A,
	// effectively a non-owner: no full lane validates this row's
	// configuration at all, so that command line must add no key-affecting
	// flag beyond ownerFP either, exactly as a true non-owner's must not
	// (independent review of #7486, must-fix 3). ownerFP here is the row's
	// full (test:-inclusive) fingerprint -- the right one to compare
	// against, since a `bazel test` command (unlike the nogo-only step's
	// `bazel build`) does read test: lines.
	checkNonOwnerCommandLineAddsNoKeyFlag(t, workflow, row.name, row.owner, []string{row.configs[0]}, ownerFP, errorf)
	usedConfigs := map[string]bool{row.configs[0]: true}
	for _, job := range row.nonOwners {
		jobConfigs := jobFor(job)
		var laneConfigs []string
		for _, c := range jobConfigs {
			if slices.Contains(row.configs[1:], c) {
				laneConfigs = append(laneConfigs, c)
			}
		}
		if len(laneConfigs) == 0 {
			errorf("row %q non-owner %s runs none of the row's non-owner configs %v (its commands select %v)", row.name, job, row.configs[1:], jobConfigs)
		}
		for _, c := range laneConfigs {
			usedConfigs[c] = true
		}
		// See checkNonOwnerCommandLineAddsNoKeyFlag's doc comment (independent
		// review of #7486, must-fix 7): a command-line-only flag, like
		// --config=doltserver-cmd --@rules_go//go/config:tags=X,extra, would
		// not show up in either check above.
		checkNonOwnerCommandLineAddsNoKeyFlag(t, workflow, row.name, job, laneConfigs, ownerFP, errorf)
	}
	for _, c := range row.configs {
		if !usedConfigs[c] {
			errorf("row %q config --config=%s is run by none of its jobs (owner %s, non-owners %v)", row.name, c, row.owner, row.nonOwners)
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
		bazelJobName:      "BAZEL_TEST",
		bazelPureJobName:  "BAZEL_PURE",
		bazelIntegJobName: "BAZEL_INTEGRATION",
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
	checkNogoRaceOwnerBuildsEverything(t, readNogoPolicyInputs(t), t.Errorf)
}

func checkNogoRaceOwnerBuildsEverything(t *testing.T, in nogoPolicyInputs, errorf func(string, ...any)) {
	t.Helper()
	workflow := in.workflow
	ownerJob := workflow.job(t, bazelJobName)
	test := ownerJob.step(t, "bazel test //... --config=ci")
	if !strings.Contains(test.Run, "bazel test //... --config=ci") {
		errorf("%s step does not run bazel test //... --config=ci:\n%s", bazelJobName, test.Run)
		return
	}
	// A negative target pattern anywhere in the command, including on a
	// backslash-continued line, narrows //.... Every argument of the
	// command is checked, not just its first line.
	for _, args := range bazelInvocations(test.Run, "test") {
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				errorf("%s excludes a target (%s) from //...: it would stop validating what it excludes", bazelJobName, a)
			}
		}
	}
	// The race owner's validating step must be unconditional: a step-level
	// `if:` or continue-on-error here would let the suite report green while
	// race-group nogo silently did not run, with nothing else in this file
	// (or T1/T2, which only check job-level fields) noticing.
	if test.If != "" {
		errorf("%s step %q has if %q; the race owner's validating step must be unconditional", bazelJobName, test.Name, test.If)
	}
	if test.ContinueOnError != nil && test.ContinueOnError != false {
		errorf("%s step %q has continue-on-error %v; a failed race-group nogo finding there would never fail the job", bazelJobName, test.Name, test.ContinueOnError)
	}
	if ownerJob.ContinueOnError {
		errorf("%s job has continue-on-error; the race owner's job must be able to fail", bazelJobName)
	}

	// Every --config the owner's own command line selects, each expanded
	// recursively: not only "ci" (test:ci -> test:prcore), but also any
	// config bazel.yml adds through a shell variable, like BAZEL_SOLE_RUN's
	// test:sole-run --build_tests_only. An earlier version checked only
	// "ci" statically and so missed a narrowing flag reachable solely
	// through such a variable (review of #7482, should-fix 2).
	for _, c := range jobBazelConfigs(workflow, ownerJob) {
		for _, l := range expandBazelrcConfig(in.rc, c, map[string]bool{}) {
			if strings.Contains(l, "build_tests_only") || strings.Contains(l, "build_tag_filters") {
				errorf("%s's --config=%s (or a config it includes) narrows the target set: %q; the race owner must build //... whole", bazelJobName, c, l)
			}
		}
	}
}

// T5 TestNoUnconditionalValidationOff: no common/build/test line without a
// :config suffix touches run_validations (that would turn validation off
// everywhere, defeating every row's owner), and neither does build:nogo*,
// build:release-cross, build:js-wasm, scripts/ci/bazel-release-cross-compile.sh,
// .github/actions/setup-bazel/write-bazelrc.sh, or any row's own owner
// config (an owner opting itself out would leave its configuration with no
// validating lane at all).
//
// write-bazelrc.sh (independent review of #7486, latent item) generates the
// CI-only bazelrc setup-bazel's "Set up Bazel" step points every lane at
// (bazel_policy_test.go pins that wiring); its own output lines are read from
// .bazelrc's own comments and bazel_rrc_test.go/bazel_cache_mode_test.go, not
// re-derived here, so it is checked the same coarse way as
// bazel-release-cross-compile.sh: a static scan of its source text for the
// literal string "run_validations", since every line it could emit lacks a
// :config suffix (they are common/build/startup lines for the run's RBE
// mode, not per-nogo-row configs) and so would be exactly T5's "turns
// validation off everywhere" case if it ever emitted one.
func TestNoUnconditionalValidationOff(t *testing.T) {
	root := sourceRepoRoot(t)
	checkNoUnconditionalValidationOff(readNogoPolicyInputs(t), t.Errorf)
	if script := readPolicyFile(t, root, "scripts/ci/bazel-release-cross-compile.sh"); strings.Contains(script, "run_validations") {
		t.Error("scripts/ci/bazel-release-cross-compile.sh turns nogo validation off; release-cross has no non-owner duplicate to remove")
	}
	if script := readPolicyFile(t, root, ".github/actions/setup-bazel/write-bazelrc.sh"); strings.Contains(script, "run_validations") {
		t.Error(".github/actions/setup-bazel/write-bazelrc.sh emits run_validations; every line it writes lacks a :config suffix, so this would turn validation off everywhere, defeating every row's owner, with no owner lane here to catch it")
	}
}

func checkNoUnconditionalValidationOff(in nogoPolicyInputs, errorf func(string, ...any)) {
	rc := in.rc
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
			errorf(".bazelrc %q: an unconfigured line must not touch run_validations; it would apply to every lane, including every row's owner", line)
		}
	}

	for _, prefix := range []string{"build:nogo", "build:release-cross", "build:js-wasm"} {
		for _, raw := range strings.Split(rc, "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, prefix) && strings.Contains(line, "validation") {
				errorf(".bazelrc %q changes validation for %s, which has no non-owner duplicate to remove", line, prefix)
			}
		}
	}

	for _, row := range nogoConfigurations {
		if row.ownerIsNogoOnlyStep {
			// Variant A (f5-spec.md §2.3): this row's owner config is
			// deliberately not the validating lane -- the owner job's
			// nogo-only step is -- so it is expected (and required
			// elsewhere, by T1/T6) to pass --norun_validations too.
			continue
		}
		if bazelrcConfigExpansionSkipsValidations(rc, row.configs[0]) {
			errorf("row %q owner config --config=%s (or a config it includes) passes --norun_validations; it is the row's only validating lane", row.name, row.configs[0])
		}
	}
}

// T6 TestRunValidationsOnlyOnAllowlistedLines: the only
// run_validations/norun_validations lines anywhere in .bazelrc or bazel.yml
// are exactly the ones nogoConfigurations' rows already require: one
// "test:<config> --norun_validations" line per non-owner config in a
// full-lane row (race), or per config (owner's own full lane included) in a
// nogo-only-step row (integ, f5-spec.md §5 S2 Variant A), plus the two
// package-gate jobs' bazel-build steps. Main's TestLintAndVetRunAsNogo used
// to ban run_validations outright, which caught a configuration outside
// T1's table quietly adding --norun_validations with no owner to notice. T5
// does not re-check that case: it only forbids unconfigured lines and a
// short prefix list. This allowlist restores the ban for every other line
// without re-banning the ones the rows require.
func TestRunValidationsOnlyOnAllowlistedLines(t *testing.T) {
	checkRunValidationsOnlyOnAllowlistedLines(t, readNogoPolicyInputs(t), t.Errorf)
}

func checkRunValidationsOnlyOnAllowlistedLines(t *testing.T, in nogoPolicyInputs, errorf func(string, ...any)) {
	t.Helper()
	// Every row classifies its own allowed lines: a full-lane row's
	// non-owner configs (configs[1:] -- the owner's own full lane must
	// still validate), or, for a nogo-only-step row (f5-spec.md §5 S2
	// Variant A), every config in it, owner's own full lane included,
	// since under Variant A none of them validates.
	allowed := map[string]bool{}
	for _, row := range nogoConfigurations {
		cfgs := row.configs[1:]
		if row.ownerIsNogoOnlyStep {
			cfgs = row.configs
		}
		for _, cfg := range cfgs {
			allowed["test:"+cfg+" --norun_validations"] = true
		}
	}
	if len(allowed) == 0 {
		t.Fatal("no row has a config to allowlist")
	}

	for _, raw := range strings.Split(in.rc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "run_validations") && !allowed[line] {
			errorf(".bazelrc %q: run_validations is only allowed as one of %v; any other line (an unlisted config, an --integration config, or a bare --run_validations toggle) would turn validation off with no owner lane catching it", line, sortedKeys(allowed))
		}
	}

	// bazel.yml: only the two package-gate jobs' bazel-build steps may pass
	// --norun_validations (they spell the race build flags out on the
	// command line rather than through a .bazelrc --config; T1 pins their
	// content). Anywhere else -- a new job, a new step, a non-package-gate
	// job -- would turn a lane's validation off unnoticed.
	for name, j := range in.workflow.Jobs {
		for _, st := range j.Steps {
			if !strings.Contains(st.Run, "run_validations") {
				continue
			}
			if !slices.Contains(nogoPackageGateJobs, name) {
				stepLabel := st.Name
				if stepLabel == "" {
					stepLabel = st.Uses
				}
				errorf("%s job %q step %q: run_validations outside the package-gate build steps", bazelWorkflowName, name, stepLabel)
			}
		}
	}
}

// TestNogoPolicyRejectsMutations: each mutation below is a .bazelrc or
// bazel.yml edit that would leave some nogo configuration with no validating
// required lane (or a non-owner duplicating its owner), and must be rejected
// by T1, T3, T5 or T6 on its own -- not only by an unrelated exact-content
// pin elsewhere in //scripts that an author would update alongside it. The
// unmutated files must pass.
func TestNogoPolicyRejectsMutations(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	wf := readPolicyFile(t, sourceRepoRoot(t), filepath.Join(".github", "workflows", bazelWorkflowName))

	const ownerCmd = `bazel test //... --config=ci ${BAZEL_SOLE_RUN:+"$BAZEL_SOLE_RUN"} ${BAZEL_FRESH:+"$BAZEL_FRESH"} \` + "\n"
	const ownerProfile = `            --profile="$RUNNER_TEMP/bazel-profile.json" \` + "\n"
	addRC := func(line string) func(string, string) (string, string) {
		return func(rc, wf string) (string, string) { return rc + "\n" + line + "\n", wf }
	}
	replace := func(inRC bool, old, new string) func(string, string) (string, string) {
		return func(rc, wf string) (string, string) {
			target := &wf
			if inRC {
				target = &rc
			}
			if !strings.Contains(*target, old) {
				t.Fatalf("mutation anchor %q not found", old)
			}
			*target = strings.Replace(*target, old, new, 1)
			return rc, wf
		}
	}

	mutations := []struct {
		name   string
		mutate func(rc, wf string) (string, string)
	}{
		// 1a/1b: under F5 S2 Variant A, test:integration and
		// test:doltserver-cmd already pass --norun_validations (their row's
		// owner is bazel-integration's nogo-only step, not a full lane), so
		// adding the line again is a no-op, not a policy violation; see the
		// s2* mutations below for this row's own negative cases.
		{"1c unknown new lane skips validations", addRC("test:newlane --norun_validations")},
		{"1d owner command line skips validations", replace(false, ownerCmd,
			strings.Replace(ownerCmd, "--config=ci ", "--config=ci --norun_validations ", 1))},
		{"1d owner continuation line skips validations", replace(false, ownerProfile,
			ownerProfile+"            --norun_validations \\\n")},
		{"1e owner-inherited prcore skips validations", addRC("test:prcore --norun_validations")},
		{"1e owner command's sole-run skips validations", addRC("test:sole-run --norun_validations")},
		{"1e owner command's sole-run inherits a non-owner config", addRC("test:sole-run --config=doltserver")},
		{"1e owner-inherited prcore inherits a non-owner config", addRC("test:prcore --config=dolt-race")},
		{"1f non-owner embedded race=false", replace(true, "test:embedded --@rules_go//go/config:race\n",
			"test:embedded --@rules_go//go/config:race=false\n")},
		{"1f non-owner doltserver race=false", replace(true, "test:doltserver --@rules_go//go/config:race\n",
			"test:doltserver --@rules_go//go/config:race=false\n")},
		{"1f non-owner embedded pure=false", replace(true, "test:embedded --@rules_go//go/config:race\n",
			"test:embedded --@rules_go//go/config:race\ntest:embedded --@rules_go//go/config:pure=false\n")},
		{"1g owner excludes a target on its first line", replace(false, ownerCmd,
			strings.Replace(ownerCmd, "--config=ci ", "--config=ci -//cmd/bd/... ", 1))},
		{"1g owner excludes a target on a continuation line", replace(false, ownerProfile,
			ownerProfile+"            -//cmd/bd/... \\\n")},
		{"2 non-owner job runs the owner's config", replace(false,
			"bazel test //... --config=embedded ${BAZEL_FRESH", "bazel test //... --config=ci ${BAZEL_FRESH")},
		// Must-fix 7 (independent review of #7486): a non-owner's own bazel
		// command line can pass a key-affecting flag directly, overriding
		// what its --config's .bazelrc lines say, while the config name
		// itself still matches what every other check here expects.
		{"7a non-owner embedded command line overrides race on the command line", replace(false,
			"bazel test //... --config=embedded ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\",
			"bazel test //... --config=embedded --@rules_go//go/config:race=false ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\")},
		{"7b non-owner doltserver-cmd command line adds an extra tag", replace(false,
			"bazel test //... --config=doltserver-cmd ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\",
			"bazel test //... --config=doltserver-cmd --@rules_go//go/config:tags=gms_pure_go,integration,extra ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\")},

		// F5 S2 Variant A (the "integ" row, ownerIsNogoOnlyStep): its owner
		// is bazel-integration's nogo-only `bazel build
		// --output_groups=nogo_fix` step, not a full lane.
		{"s2a nogo-only step removed", replace(false,
			"--keep_going \\\n            --output_groups=nogo_fix \\\n            -- //internal/testutil/integration:integration \\\n",
			"--keep_going \\\n            -- //internal/testutil/integration:integration \\\n")},
		{"s2b nogo-only step drifts onto a different configuration", replace(false,
			"bazel build --config=integration --@rules_go//go/config:race --keep_going \\\n",
			"bazel build --config=embedded --@rules_go//go/config:race --keep_going \\\n")},
		{"s2c nogo-only step has continue-on-error", replace(false,
			"        id: nogo-integration\n        if: ${{ always() && steps.test.outcome != 'skipped' }}\n        timeout-minutes: 10\n",
			"        id: nogo-integration\n        if: ${{ always() && steps.test.outcome != 'skipped' }}\n        continue-on-error: true\n        timeout-minutes: 10\n")},
		// s2d-g (independent review of #7486): checkNogoOnlyStepRow's `if:`,
		// pipefail and exit-code checks must each reject their own drift,
		// not only be present as dead code the other three T1 checks happen
		// to also catch.
		{"s2d nogo-only step's if becomes unconditionally false", replace(false,
			"        if: ${{ always() && steps.test.outcome != 'skipped' }}\n        timeout-minutes: 10\n        run: |\n          set -o pipefail\n",
			"        if: ${{ false }}\n        timeout-minutes: 10\n        run: |\n          set -o pipefail\n")},
		{"s2e nogo-only step's if narrows to one event", replace(false,
			"        if: ${{ always() && steps.test.outcome != 'skipped' }}\n        timeout-minutes: 10\n        run: |\n          set -o pipefail\n",
			"        if: ${{ always() && steps.test.outcome != 'skipped' && github.event_name == 'push' }}\n        timeout-minutes: 10\n        run: |\n          set -o pipefail\n")},
		{"s2f nogo-only step loses set -o pipefail", replace(false,
			"        run: |\n          set -o pipefail\n          start=$(date +%s)\n          rc=0\n          bazel build --config=integration --@rules_go//go/config:race --keep_going \\\n            --output_groups=nogo_fix \\\n            -- //internal/testutil/integration:integration \\\n",
			"        run: |\n          start=$(date +%s)\n          rc=0\n          bazel build --config=integration --@rules_go//go/config:race --keep_going \\\n            --output_groups=nogo_fix \\\n            -- //internal/testutil/integration:integration \\\n")},
		{"s2g nogo-only step's exit is hardcoded to 0", replace(false,
			"echo \"nogo-integration: exit $rc, $(( $(date +%s) - start ))s wall${procs:+, $procs}\" | tee -a \"$GITHUB_STEP_SUMMARY\"\n          exit \"$rc\"\n",
			"echo \"nogo-integration: exit $rc, $(( $(date +%s) - start ))s wall${procs:+, $procs}\" | tee -a \"$GITHUB_STEP_SUMMARY\"\n          exit 0\n")},
		{"s2h nogo-only step's tee pipeline gains || true", replace(false,
			"-- //internal/testutil/integration:integration \\\n            2>&1 | tee \"$RUNNER_TEMP/bazel-nogo-integration.log\" || rc=$?\n",
			"-- //internal/testutil/integration:integration \\\n            2>&1 | tee \"$RUNNER_TEMP/bazel-nogo-integration.log\" || true\n")},
		// s2i (bee-ghosttrack review of #7486, should-fix): the step
		// drops its own explicit --@rules_go//go/config:race, the only
		// real source of that flag for this `bazel build` invocation
		// (.bazelrc sets it for "integration" only via a test: line,
		// which `bazel build` never reads). Before buildOrCommonLines,
		// the step's fingerprint still picked the flag up from that
		// test: line through the --config=integration expansion, so
		// this drift fingerprinted as race-on and passed undetected
		// while the step's real `bazel build` invocation had silently
		// stopped validating the race build at all.
		{"s2i nogo-only step drops its own explicit race flag (its only real source for a `bazel build`)", replace(false,
			"bazel build --config=integration --@rules_go//go/config:race --keep_going \\\n",
			"bazel build --config=integration --keep_going \\\n")},
		// s2j (independent review of #7486, must-fix 3): under Variant A, the
		// owner job's own full lane (`bazel test //... --config=integration`)
		// validates nothing -- it is effectively a non-owner, like
		// bazel-embedded is for the "race" row -- so a key-affecting flag
		// added directly to its command line, overriding what --config=
		// integration's own .bazelrc lines say, must be rejected exactly as
		// 7a/7b reject it for a true non-owner.
		{"s2j owner's own full lane overrides tags on the command line", replace(false,
			"bazel test //... --config=integration ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\",
			"bazel test //... --config=integration --@rules_go//go/config:tags=gms_pure_go,integration,extra ${BAZEL_FRESH:+\"$BAZEL_FRESH\"} \\")},
		// s2k/s2l (independent review of #7486, must-fix 2): the nogo-only
		// step's target set must be pinned by exact label, and a "-//"
		// exclusion rejected outright, exactly as T3 does for the race
		// owner's own target set.
		{"s2k nogo-only step retargets to a different rule in the same package", replace(false,
			"-- //internal/testutil/integration:integration \\\n",
			"-- //internal/testutil/integration:repo_doc_files \\\n")},
		{"s2l nogo-only step gains a \"-//\" exclusion pattern", replace(false,
			"-- //internal/testutil/integration:integration \\\n",
			"-- //internal/testutil/integration:integration -//internal/testutil/integration:integration \\\n")},

		// Should-fix 1 (review of #7486, on top of #7482's own should-fix
		// 1): the earlier should-fix-1 mutation below
		// ("common: config" at the top level) is also rejected by T6
		// (checkRunValidationsOnlyOnAllowlistedLines) on its own, since it
		// adds a new, unlisted "test:sf1lane --norun_validations" line --
		// so it does not actually exercise expandBazelrcConfig's
		// common:-line / non-first-position fix in
		// checkNogoConfigurationsHaveOneValidatingLane. This case reaches
		// an *already-allowlisted* non-owner line (test:doltserver
		// --norun_validations, the "race" row's own) only through a
		// common:sole-run config that the owner's BAZEL_SOLE_RUN reaches:
		// T6 sees no new line, so only checkNogoConfigurationsHaveOneValidatingLane's
		// expansion of common:-prefixed config lines can catch it. Verified
		// by temporarily restricting expandBazelrcConfig's bazelRCConfigLines
		// lookup to "build"/"test" commands only (the pre-should-fix-1
		// behavior): this case passed undetected; T1's existing coverage
		// below detects it as expected.
		{"should-fix 1 (exercises the fix): owner reaches an already-allowlisted norun_validations only via a common: config", addRC("common:sole-run --config=doltserver")},

		// Should-fix 1 (review of #7482): expandBazelrcConfig must follow a
		// --config= reference on a common: line, not only a whole line
		// beginning "build --config=" / "test --config=". Before the fix,
		// test:ci's own chain would never reach sf1lane, and this
		// --norun_validations line would have passed unnoticed.
		{"should-fix 1: owner inherits validations-off through a common: config", func(rc, wf string) (string, string) {
			return rc + "\ncommon:ci --config=sf1lane\ntest:sf1lane --norun_validations\n", wf
		}},
		// Should-fix 2 (review of #7482): TestNogoRaceOwnerBuildsEverything
		// must expand every config the owner's command line reaches,
		// including one selected only through a shell variable
		// (BAZEL_SOLE_RUN -> --config=sole-run), not only the literal "ci"
		// it types out. Before the fix, a narrowing flag reachable solely
		// through sole-run would have passed unnoticed.
		{"should-fix 2: sole-run (reached only via BAZEL_SOLE_RUN) narrows the target set", addRC("test:sole-run --build_tests_only")},
		// Should-fix 3 (review of #7482): the package-gate non-owner check
		// must fingerprint its bazel-build step's full argument list, not
		// substring-match the raw run text. Before the fix, an added
		// key-affecting flag (here --platforms, which changes what the
		// step actually builds) passed unnoticed as long as the race flag
		// and --norun_validations substrings were still present somewhere
		// in the line.
		{"should-fix 3: package gate build step gains an unfingerprinted key flag", replace(false,
			"bazel build --@rules_go//go/config:race //cmd/bd:bd_for_tests --remote_download_regex='.*/bin/cmd/bd/bd_for_tests/bd$' --norun_validations",
			"bazel build --@rules_go//go/config:race //cmd/bd:bd_for_tests --remote_download_regex='.*/bin/cmd/bd/bd_for_tests/bd$' --platforms=//tools/bazel:fake_platform --norun_validations")},
	}

	check := func(rc, wf string) []string {
		var workflow ciWorkflow
		if err := yaml.Unmarshal([]byte(wf), &workflow); err != nil {
			t.Fatalf("parse mutated %s: %v", bazelWorkflowName, err)
		}
		in := nogoPolicyInputs{rc: rc, workflow: workflow}
		var problems []string
		errorf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
		checkNogoConfigurationsHaveOneValidatingLane(t, in, errorf)
		checkNogoRaceOwnerBuildsEverything(t, in, errorf)
		checkNoUnconditionalValidationOff(in, errorf)
		checkRunValidationsOnlyOnAllowlistedLines(t, in, errorf)
		return problems
	}

	if problems := check(rc, wf); len(problems) != 0 {
		t.Fatalf("unmutated .bazelrc/%s fail the nogo policy: %q", bazelWorkflowName, problems)
	}
	for _, m := range mutations {
		mrc, mwf := m.mutate(rc, wf)
		if mrc == rc && mwf == wf {
			t.Fatalf("mutation %q changed nothing", m.name)
		}
		if problems := check(mrc, mwf); len(problems) == 0 {
			t.Errorf("mutation %q: accepted by T1/T3/T5/T6; it must be rejected", m.name)
		} else {
			t.Logf("mutation %q: rejected: %s", m.name, problems[0])
		}
	}
}
