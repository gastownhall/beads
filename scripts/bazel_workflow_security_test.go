package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// F12 (rbe-full-bazel-ci-plan 5.3) retired bazel-farm.yml, the
// pull_request_target caller that handed allowlisted fork authors' PR code
// the RBE secrets: allowlisted forks now get rbe-fork tier rw (mode
// fork-rw) inside their own pull_request run. These tests keep that shape:
// no privileged trigger runs PR code, bazel.yml's checkouts are the
// event's, and what bazel.yml restores is verified.

// The users on .github/bazel-farm-allowlist.txt, numeric id -> login (gh api
// users/<login> --jq .id), the same people as gascity's
// .github/blacksmith-allowlist.txt. rbe-fork-mint reads the file from the
// default branch to grant tier rw. A change here is a trust decision.
var rbeForkAllowlistUsers = map[string]string{
	"8082291":  "julianknutsen",
	"91582":    "quad341",
	"36544495": "sjarmak",
	"2568253":  "csells",
}

// rbeForkAllowlist keeps its bazel-farm.yml-era path: it is the mint's
// configuration (MINT_REPOS).
const rbeForkAllowlist = ".github/bazel-farm-allowlist.txt"

// The allowlist is the trust decision: exactly these numeric user ids, each
// with its login as a comment for humans (ids are what the mint matches: a
// renamed account's old login can be registered by anyone, and a line that
// is not a numeric id makes the mint treat the list as empty).
func TestRBEForkAllowlist(t *testing.T) {
	got := map[string]string{}
	entry := regexp.MustCompile(`^([1-9][0-9]{0,19}) # ([a-z0-9][a-z0-9-]{0,38})$`)
	for _, line := range strings.Split(readPolicyFile(t, sourceRepoRoot(t), rbeForkAllowlist), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := entry.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("allowlist line %q is not `<numeric id> # <login>`", line)
			continue
		}
		got[m[1]] = m[2]
	}
	if !reflect.DeepEqual(got, rbeForkAllowlistUsers) {
		t.Errorf("allowlist = %v, want %v (changing it is a trust decision: update rbeForkAllowlistUsers deliberately)", got, rbeForkAllowlistUsers)
	}
}

// prRefExpr: a checkout ref or repository naming PR code, the pull
// request's head or merge commit or the triggering run's head.
var prRefExpr = regexp.MustCompile(`github\.event\.pull_request|github\.event\.number|github\.head_ref|github\.event\.workflow_run\.head|refs/pull/|\binputs\.`)

// privilegedPRCheckout is the one accepted privileged checkout of PR code:
// update-vendor-hash.yml checks out a Dependabot PR's head (same repository
// only) to push a default.nix hash fix with a write token. It predates F12
// and runs no Bazel; its job if pins it to dependabot[bot] PRs from this
// repository. Anything else is a pwn request.
var privilegedPRCheckout = struct {
	workflow, job, ref string
	jobIf              []string
}{
	workflow: "update-vendor-hash.yml",
	job:      "update-vendor-hash",
	ref:      "${{ github.event.pull_request.head.sha }}",
	jobIf: []string{
		"github.actor == 'dependabot[bot]'",
		"github.event.pull_request.user.login == 'dependabot[bot]'",
		"github.event.pull_request.head.repo.full_name == github.repository",
	},
}

// TestNoPrivilegedWorkflowRunsPRCode (design 3.4, plan F12): no workflow
// triggered by pull_request_target or workflow_run may check out PR code
// (a checkout ref or repository naming the pull request, its head or the
// triggering run's head), set allow-unsafe-pr-checkout, call bazel.yml, or
// call a local workflow or composite action that does any of that. Forks
// run Bazel in their own pull_request run, with no secrets.
func TestNoPrivilegedWorkflowRunsPRCode(t *testing.T) {
	privileged := 0
	sawException := false
	for _, entry := range mustReadWorkflowDir(t) {
		rel := filepath.Join(".github", "workflows", entry)
		var trigger string
		for _, on := range yamlMapKeys(readYAMLNode(t, rel), "on") {
			if on == "pull_request_target" || on == "workflow_run" {
				trigger = on
			}
		}
		if trigger == "" {
			continue
		}
		privileged++
		for jobName, job := range readCIWorkflow(t, entry).Jobs {
			where := fmt.Sprintf("%s (%s) job %s", entry, trigger, jobName)
			if job.Uses != "" {
				if strings.HasSuffix(job.Uses, "/"+bazelWorkflowName) {
					t.Errorf("%s calls %s; no privileged trigger may run the Bazel lanes", where, bazelWorkflowName)
				}
				for k, v := range job.With {
					if prRefExpr.MatchString(v) {
						t.Errorf("%s passes %s = %q (PR code) to %s", where, k, v, job.Uses)
					}
				}
				if strings.HasPrefix(job.Uses, "./") {
					called := strings.TrimPrefix(job.Uses, "./.github/workflows/")
					for calledJob, cj := range readCIWorkflow(t, called).Jobs {
						checkPrivilegedSteps(t, where+" -> "+called+" job "+calledJob, cj.Steps, nil)
					}
				}
				continue
			}
			var exception *string
			if entry == privilegedPRCheckout.workflow && jobName == privilegedPRCheckout.job {
				sawException = true
				for _, cond := range privilegedPRCheckout.jobIf {
					if !strings.Contains(job.If, cond) {
						t.Errorf("%s if = %q lacks %q; its PR-head checkout is accepted only for Dependabot PRs from this repository", where, job.If, cond)
					}
				}
				exception = &privilegedPRCheckout.ref
			}
			checkPrivilegedSteps(t, where, job.Steps, exception)
		}
	}
	if privileged == 0 {
		t.Fatal("found no pull_request_target or workflow_run workflow; the scan is broken (bazel-autofix.yml, docs-autofix.yml)")
	}
	if !sawException {
		t.Errorf("%s job %s is gone; drop privilegedPRCheckout", privilegedPRCheckout.workflow, privilegedPRCheckout.job)
	}
}

// checkPrivilegedSteps applies TestNoPrivilegedWorkflowRunsPRCode's rule to
// steps, following local composite actions. exceptionRef, when set, is the
// one PR ref a single checkout may use.
func checkPrivilegedSteps(t *testing.T, where string, steps []ciWorkflowStep, exceptionRef *string) {
	t.Helper()
	excepted := 0
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, "./") {
			var action ciCompositeAction
			raw := readPolicyFile(t, sourceRepoRoot(t), filepath.ToSlash(filepath.Join(strings.TrimPrefix(step.Uses, "./"), "action.yml")))
			if err := yaml.Unmarshal([]byte(raw), &action); err != nil {
				t.Fatalf("%s: parse %s: %v", where, step.Uses, err)
			}
			checkPrivilegedSteps(t, where+" -> "+step.Uses, action.Runs.Steps, nil)
			continue
		}
		if actionFamily(step.Uses) != "actions/checkout" {
			continue
		}
		if _, ok := step.With["allow-unsafe-pr-checkout"]; ok {
			t.Errorf("%s: checkout sets allow-unsafe-pr-checkout", where)
		}
		for _, key := range []string{"ref", "repository"} {
			v := step.With[key]
			if !prRefExpr.MatchString(v) {
				continue
			}
			if key == "ref" && exceptionRef != nil && v == *exceptionRef {
				excepted++
				continue
			}
			t.Errorf("%s: checkout %s = %q checks out PR code", where, key, v)
		}
	}
	if exceptionRef != nil && excepted != 1 {
		t.Errorf("%s: %d checkouts of %s, want exactly the one accepted", where, excepted, *exceptionRef)
	}
}

// bazel.yml's checkouts: the event's own commit (no ref), no token in
// .git/config, and nothing that could select or admit fork code. F12
// removed the checkout-sha and fork-farm inputs and every
// allow-unsafe-pr-checkout opt-in; no workflow may set that input again.
func TestBazelLaneCheckouts(t *testing.T) {
	call := readBazelWorkflowCall(t)
	for _, gone := range []string{"checkout-sha", "fork-farm"} {
		if _, ok := call.Inputs[gone]; ok {
			t.Errorf("%s declares workflow_call input %s; F12 retired it with bazel-farm.yml", bazelWorkflowName, gone)
		}
	}
	workflow := readCIWorkflow(t, bazelWorkflowName)
	checkouts := 0
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Errorf("%s job %s step %q interpolates an expression into its script", bazelWorkflowName, name, step.Name)
			}
			if actionFamily(step.Uses) != "actions/checkout" {
				continue
			}
			checkouts++
			want := map[string]string{"persist-credentials": "false"}
			// F3: package-mcp/package-npm's detect step diffs PR_BASE_SHA
			// against PR_HEAD_SHA (scripts/ci/detect-package-gates.sh, same
			// as pr.yml's legacy detect job used); that needs full history,
			// unlike every other lane's shallow, history-free checkout.
			if bazelPackageJobs[name] {
				want["fetch-depth"] = "0"
			}
			if !reflect.DeepEqual(step.With, want) {
				t.Errorf("%s job %s checkout with = %v, want %v", bazelWorkflowName, name, step.With, want)
			}
		}
	}
	// Every lane checks out the PR except rbe (decides the mode before any
	// checkout) and rbe-prewarm (no checkout at all, by design: B1, security
	// review of bdef342d5 - its dispatch logic is inlined into the job's own
	// `run:` instead of a checked-out script file, so no step in this job
	// ever reads repository content under the shared gascity credential).
	if checkouts != len(workflow.Jobs)-2 {
		t.Errorf("%d checkouts in %s, want one per lane excluding %s and %s (%d)", checkouts, bazelWorkflowName, bazelRBEJobName, bazelRBEPrewarmJobName, len(workflow.Jobs)-2)
	}
	for _, entry := range mustReadWorkflowDir(t) {
		walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", entry)), "", func(path string, key bool, value string) {
			if key && value == "allow-unsafe-pr-checkout" {
				t.Errorf("%s: %s opts into checking out fork PR code", entry, path)
			}
		})
	}
}

func mustReadWorkflowDir(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// bazel.yml and setup-bazel run PR code. GitHub gives untrusted triggers a
// read-only cache token by default, and a declared cache-mode would
// override that default, so they may declare none but read or none
// (bazel-farm.yml's rule, kept after F12: nothing here needs more).
func TestBazelWorkflowCacheModeReadOrNone(t *testing.T) {
	for _, rel := range []string{
		filepath.Join(".github", "workflows", bazelWorkflowName),
		filepath.Join(setupBazelActionDir, "action.yml"),
	} {
		root := readYAMLNode(t, rel)
		var check func(node *yaml.Node, path string)
		check = func(node *yaml.Node, path string) {
			switch node.Kind {
			case yaml.MappingNode:
				for i := 0; i+1 < len(node.Content); i += 2 {
					k, v := node.Content[i].Value, node.Content[i+1]
					if k == "cache-mode" && (v.Kind != yaml.ScalarNode || (v.Value != "read" && v.Value != "none")) {
						t.Errorf("%s: %s.cache-mode = %q; only read or none (PR code must not write caches trusted runs restore)", rel, path, v.Value)
					}
					check(v, path+"."+k)
				}
			case yaml.SequenceNode:
				for i, item := range node.Content {
					check(item, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
		check(root, "")
	}
}

// bazel.yml's token: exactly contents: read, at the top and on any job that
// declares permissions (a call cannot exceed its caller's, but pin it).
func TestBazelWorkflowPermissionsReadOnly(t *testing.T) {
	var doc struct {
		Permissions any `yaml:"permissions"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &doc); err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]any{"contents": "read"}
	if !reflect.DeepEqual(doc.Permissions, readOnly) {
		t.Errorf("%s permissions = %v, want exactly %v", bazelWorkflowName, doc.Permissions, readOnly)
	}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if job.Permissions != nil && !reflect.DeepEqual(job.Permissions, readOnly) {
			t.Errorf("%s job %s permissions = %v, want none or exactly %v", bazelWorkflowName, name, job.Permissions, readOnly)
		}
	}
}

// Nothing restored from the runner cache is executed unverified: the Bazel
// binary is downloaded fresh into a Bazelisk home outside the cache and
// checked against a sha256 pinned for .bazelversion, the repo contents cache
// (unverified extracted repos) is off, and restored Go modules are checked
// against go.sum before use.
func TestBazelRestoredCachesAreVerified(t *testing.T) {
	version := strings.TrimSpace(readPolicyFile(t, sourceRepoRoot(t), ".bazelversion"))
	var install, wrapper ciWorkflowStep
	for _, step := range readSetupBazelAction(t).Runs.Steps {
		switch step.Name {
		case "Install Bazelisk":
			install = step
		case "Install bazel wrapper":
			wrapper = step
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		pin := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(version+"/"+arch) + `\) bazel_sha=[0-9a-f]{64} ;;$`)
		if !pin.MatchString(install.Run) {
			t.Errorf("setup-bazel pins no sha256 for Bazel %s (.bazelversion) on %s", version, arch)
		}
	}
	for _, want := range []string{
		`echo "BAZELISK_HOME=$RUNNER_TEMP/bazelisk-home"`,
		`echo "BAZELISK_VERIFY_SHA256=$bazel_sha"`,
		`echo "BAZEL_CI_BAZEL_SHA256=$bazel_sha"`,
		`bazel_version="$(tr -d '[:space:]' < .bazelversion)"`,
	} {
		if !strings.Contains(install.Run, want) {
			t.Errorf("setup-bazel Install Bazelisk lacks %q", want)
		}
	}
	if strings.Contains(install.Run, "bazel-ci-cache") {
		t.Errorf("setup-bazel puts Bazelisk's home in the runner cache; the Bazel binary must never be restored from it")
	}
	for _, want := range []string{
		"/usr/local/bin/bazelisk --version",
		`echo "${BAZEL_CI_BAZEL_SHA256}  $bin" | sha256sum -c -`,
		`find "$BAZELISK_HOME/downloads" -type f -path '*/bin/bazel' -print0`,
		`if [ "$n" -eq 0 ]; then`,
	} {
		if !strings.Contains(wrapper.Run, want) {
			t.Errorf("setup-bazel Install bazel wrapper lacks %q", want)
		}
	}
	if strings.Index(wrapper.Run, "/usr/local/bin/bazelisk --version") > strings.Index(wrapper.Run, "sha256sum -c") {
		t.Errorf("setup-bazel verifies the Bazel binary before downloading it")
	}

	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	restore := job.stepIndex(t, "Restore Go module cache")
	verify := job.stepIndex(t, "Verify restored Go modules")
	if verify != restore+1 || strings.TrimSpace(job.Steps[verify].Run) != "go mod verify" || job.Steps[verify].If != "" {
		t.Errorf("%s: want an unconditional `go mod verify` step right after the Go module cache restore", bazelJobName)
	}
	for name, j := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		for i, step := range j.Steps {
			if actionFamily(step.Uses) == cacheRestoreActionFamily && strings.Contains(step.With["path"], "go/pkg/mod") &&
				(i+1 >= len(j.Steps) || strings.TrimSpace(j.Steps[i+1].Run) != "go mod verify") {
				t.Errorf("%s job %s restores the Go module cache without verifying it next", bazelWorkflowName, name)
			}
		}
	}
}
