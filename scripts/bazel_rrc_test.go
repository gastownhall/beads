package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Bazel's remote repo contents cache on rbe-west (ga-vnycm2.29, ported from
// gascity's bazel.yml, gascity #7467 and #7478). bazel.yml's rbe job turns
// vars.RBE_REPO_CONTENTS_CACHE into its rrc output (off, seed, canary, on);
// each reading lane's reader step appends bazelRRCReadLines to
// .bazelrc.local; the rrc-seed job, on push to main only, runs every lane's
// loading and analysis (.github/scripts/rrc-lane-commands.txt, through
// rrc-run-lanes.sh) and uploads repository trees with a 30-minute
// rbe-rrc-writer-beads certificate (rrc-writer-credential.sh); the
// rrc-verify job, nightly, compares the cache with a cold fetch
// (tools/bazel/rrc_verify.py).

const (
	bazelRRCReadStep    = "Remote repo contents cache (read)"
	bazelRRCModeStepID  = "rrc"
	bazelRRCVar         = "vars.RBE_REPO_CONTENTS_CACHE"
	bazelRRCSeedJob     = "rrc-seed"
	bazelRRCSeedJobIf   = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' && needs.rbe.outputs.mode == 'remote' && needs.rbe.outputs.rrc != 'off' }}"
	bazelRRCVerifyJob   = "rrc-verify"
	bazelRRCVerifyJobIf = "${{ (github.event_name == 'schedule' || github.event_name == 'workflow_dispatch') && needs.rbe.outputs.mode == 'remote' && needs.rbe.outputs.rrc != 'off' }}"
	bazelRRCCanaryLane  = "bazel-test"
	bazelRRCCredential  = ".github/scripts/rrc-writer-credential.sh"
	bazelRRCRunLanes    = ".github/scripts/rrc-run-lanes.sh"
	bazelRRCLaneCmds    = ".github/scripts/rrc-lane-commands.txt"
	bazelRRCVerifyTool  = "tools/bazel/rrc_verify.py"
	bazelRRCVerifyTest  = "tools/bazel/rrc_verify_test.py"
	bazelSetupBazelUses = "./.github/actions/setup-bazel"
)

// bazelRRCReadLanes: the lanes that read the cache under `on` (bazel-test
// alone under `canary`). Not bazel-release-cross (other target platforms,
// not seeded) nor the package gates.
var bazelRRCReadLanes = []string{
	"bazel-cmd-dolt", "bazel-dolt-race", "bazel-doltserver", "bazel-embedded",
	"bazel-integration", "bazel-proxied", "bazel-pure", "bazel-server-storage",
	"bazel-test",
}

// bazelRRCReadLines: the reader's .bazelrc.local lines; both are key neutral
// (a startup option, and loading parallelism).
var bazelRRCReadLines = []string{
	"startup --experimental_remote_repo_contents_cache",
	"common --loading_phase_threads=64",
}

// bazelRRCJobs: bazel.yml's jobs that are not lanes (no result output, never
// gated): the remote repo contents cache's writer and its nightly check.
var bazelRRCJobs = []string{bazelRRCSeedJob, bazelRRCVerifyJob}

// isBazelRRCJob reports whether a bazel.yml job is one of bazelRRCJobs.
func isBazelRRCJob(name string) bool { return slices.Contains(bazelRRCJobs, name) }

// evalRRCIf evaluates a job or step if: with ctx, failing the test on an
// expression the evaluator cannot read.
func evalRRCIf(t *testing.T, expr string, ctx map[string]string) bool {
	t.Helper()
	v, err := evalGHExpr(expr, ctx)
	if err != nil {
		t.Fatalf("evalGHExpr(%q): %v", expr, err)
	}
	return ghTruthy(v)
}

// runRRCScript runs a step's script (or a script file) under GitHub's bash
// flags in dir with env, returning its combined output.
func runRRCScript(t *testing.T, dir, script string, env map[string]string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// rrcOutputs reads a $GITHUB_OUTPUT file (key=value lines).
func rrcOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func rrcStep(t *testing.T, job ciWorkflowJob, what string, pick func(ciWorkflowStep) bool) ciWorkflowStep {
	t.Helper()
	var found []ciWorkflowStep
	for _, s := range job.Steps {
		if pick(s) {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %s step, got %d", what, len(found))
	}
	return found[0]
}

// TestBazelRRCModeStep runs the rbe job's rrc step for every variable value
// and execution mode: only mode remote enables anything, unset and off are
// off, and an unknown value is off with a warning (a typo never fails the
// required gate). It reads no secret and runs before the checkout.
func TestBazelRRCModeStep(t *testing.T) {
	rbe := readCIWorkflow(t, bazelWorkflowName).Jobs[bazelRBEJobName]
	if got := rbe.Outputs["rrc"]; got != "${{ steps.rrc.outputs.rrc }}" {
		t.Errorf("rbe job output rrc = %q", got)
	}
	step := rrcStep(t, rbe, "rrc", func(s ciWorkflowStep) bool { return s.ID == bazelRRCModeStepID })
	if want := map[string]string{"RRC": "${{ " + bazelRRCVar + " }}", "MODE": "${{ steps.decide.outputs.mode }}"}; !reflect.DeepEqual(step.Env, want) {
		t.Errorf("rrc step env %v, want %v", step.Env, want)
	}
	if step.If != "" {
		t.Errorf("rrc step if %q; it must always set the output", step.If)
	}
	for i, s := range rbe.Steps {
		if s.Uses != "" {
			t.Errorf("rbe step %d (%s) precedes nothing it should: the rrc step must come before any checkout", i, s.Uses)
		}
		if s.ID == bazelRRCModeStepID {
			break
		}
	}
	for _, mode := range []string{"remote", "fork-ro", "fork-rw", "cache", "local", "skip"} {
		for value, want := range map[string]string{"": "off", "off": "off", "seed": "seed", "canary": "canary", "on": "on", "On": "off", "true": "off", "on;x": "off"} {
			if mode != "remote" {
				want = "off"
			}
			dir := t.TempDir()
			output := filepath.Join(dir, "output")
			out, err := runRRCScript(t, dir, step.Run, map[string]string{
				"RRC": value, "MODE": mode, "GITHUB_OUTPUT": output, "GITHUB_STEP_SUMMARY": filepath.Join(dir, "summary"),
			})
			if err != nil {
				t.Errorf("mode %s value %q: %v\n%s", mode, value, err, out)
				continue
			}
			if got := rrcOutputs(t, output)["rrc"]; got != want {
				t.Errorf("mode %s value %q: rrc=%q, want %q", mode, value, got, want)
			}
			unknown := !slices.Contains([]string{"", "off", "seed", "canary", "on"}, value)
			if warned := strings.Contains(out, "::warning"); warned != unknown {
				t.Errorf("mode %s value %q: warning %v, want %v:\n%s", mode, value, warned, unknown, out)
			}
		}
	}
}

// TestBazelRRCReadSteps: exactly bazelRRCReadLanes read through these steps,
// in mode remote only (fork modes and local never; mode cache reads through
// setup-bazel's rc instead, behind cache-rrc-probe.sh: TestSetupBazelRCWriter),
// bazel-test under canary and every one of them under on; each
// writes exactly bazelRRCReadLines, never mentions uploads, and comes before
// the job's Set up Bazel (so no Bazel server starts without it).
func TestBazelRRCReadSteps(t *testing.T) {
	wf := readCIWorkflow(t, bazelWorkflowName)
	var readers []string
	for name, job := range wf.Jobs {
		read, setup := -1, -1
		for i, s := range job.Steps {
			switch {
			case s.Name == bazelRRCReadStep:
				if read >= 0 {
					t.Errorf("%s has two %q steps", name, bazelRRCReadStep)
				}
				read = i
			case s.Uses == bazelSetupBazelUses && setup < 0:
				setup = i
			}
		}
		if read < 0 {
			continue
		}
		readers = append(readers, name)
		if setup < 0 || read > setup {
			t.Errorf("%s: %q at step %d, Set up Bazel at %d; the reader must come first", name, bazelRRCReadStep, read, setup)
		}
		step := job.Steps[read]
		for _, mode := range []string{"remote", "fork-ro", "fork-rw", "cache", "local"} {
			for _, rrc := range []string{"off", "seed", "canary", "on"} {
				want := mode == "remote" && (rrc == "on" || (rrc == "canary" && name == bazelRRCCanaryLane))
				if got := evalRRCIf(t, step.If, map[string]string{"needs.rbe.outputs.mode": mode, "needs.rbe.outputs.rrc": rrc}); got != want {
					t.Errorf("%s mode %s rrc %s: reader runs %v, want %v", name, mode, rrc, got, want)
				}
			}
		}
		dir := t.TempDir()
		if out, err := runRRCScript(t, dir, step.Run, nil); err != nil {
			t.Fatalf("%s reader: %v\n%s", name, err, out)
		}
		got := strings.Split(strings.TrimSpace(readPolicyFile(t, dir, ".bazelrc.local")), "\n")
		if !slices.Equal(got, bazelRRCReadLines) {
			t.Errorf("%s reader writes %q, want %q", name, got, bazelRRCReadLines)
		}
		if strings.Contains(step.Run, "upload") || len(step.Env) != 0 {
			t.Errorf("%s reader mentions uploads or takes env; lanes only read", name)
		}
	}
	sort.Strings(readers)
	if !slices.Equal(readers, bazelRRCReadLanes) {
		t.Errorf("lanes with a reader step %v, want %v", readers, bazelRRCReadLanes)
	}
}

// bazelRRCLaneCommands parses rrc-lane-commands.txt as rrc-run-lanes.sh does.
func bazelRRCLaneCommands(t *testing.T, root string) []string {
	t.Helper()
	var cmds []string
	for _, line := range strings.Split(readPolicyFile(t, root, bazelRRCLaneCmds), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line != "" {
			cmds = append(cmds, line)
		}
	}
	return cmds
}

// bazelRRCEnablingJobs: bazel.yml's jobs that enable the remote repo
// contents cache flag in any mode: a trusted reader step (mode remote) or
// setup-bazel's BAZEL_RRC_READ opt-in (mode cache, write-bazelrc.sh), with
// the steps that do it.
func bazelRRCEnablingJobs(t *testing.T) map[string][]string {
	t.Helper()
	jobs := map[string][]string{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if isBazelRRCJob(name) {
			continue // the writer and its check set the flag themselves
		}
		for _, s := range job.Steps {
			if s.Name == bazelRRCReadStep {
				jobs[name] = append(jobs[name], s.Name)
			}
			if s.Uses == bazelSetupBazelUses && s.Env["BAZEL_RRC_READ"] != "" {
				jobs[name] = append(jobs[name], "setup-bazel BAZEL_RRC_READ="+s.Env["BAZEL_RRC_READ"])
			}
		}
	}
	return jobs
}

// TestBazelRRCOptInMirrorsTheTrustedReaders: setup-bazel's BAZEL_RRC_READ
// (mode cache reads through rbe-cache) is exactly "true" on exactly the
// trusted readers' jobs, bazelRRCReadLanes: not on every mode cache job
// (bazel-release-cross builds for platforms rrc-lane-commands.txt does not
// seed, and the package gates are not lanes).
func TestBazelRRCOptInMirrorsTheTrustedReaders(t *testing.T) {
	var optIn []string
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		for _, s := range job.Steps {
			v, ok := s.Env["BAZEL_RRC_READ"]
			if !ok {
				continue
			}
			if s.Uses != bazelSetupBazelUses || v != "true" {
				t.Errorf("%s step %q sets BAZEL_RRC_READ=%q; only setup-bazel takes it, as \"true\"", name, s.Name, v)
			}
			optIn = append(optIn, name)
		}
	}
	sort.Strings(optIn)
	if !slices.Equal(optIn, bazelRRCReadLanes) {
		t.Errorf("jobs passing BAZEL_RRC_READ %v, want the trusted readers %v", optIn, bazelRRCReadLanes)
	}
}

// TestBazelRRCLaneCommandsCoverEveryReader: rrc-seed seeds (and rrc-verify
// checks) what the readers fetch, so every job that enables the flag
// (bazelRRCEnablingJobs: the trusted reader step or the mode cache opt-in)
// is a reading lane, every --config it passes to `bazel test` or `bazel
// build` has a command in rrc-lane-commands.txt, and every command there is
// a reading lane's.
func TestBazelRRCLaneCommandsCoverEveryReader(t *testing.T) {
	root := sourceRepoRoot(t)
	seeded := map[string]bool{}
	for _, cmd := range bazelRRCLaneCommands(t, root) {
		f := strings.Fields(cmd)
		if f[0] != "test" && f[0] != "build" {
			t.Errorf("lane command %q: want a test or build command", cmd)
		}
		for _, a := range f[1:] {
			if c, ok := strings.CutPrefix(a, "--config="); ok {
				seeded[c] = true
			}
		}
	}
	used := map[string]bool{}
	invocation := regexp.MustCompile(`\bbazel (?:test|build)\b[^\n]*(?:\\\n[^\n]*)*`)
	config := regexp.MustCompile(`--config=([a-z0-9-]+)`)
	wf := readCIWorkflow(t, bazelWorkflowName)
	enabling := bazelRRCEnablingJobs(t)
	for job, how := range enabling {
		if !slices.Contains(bazelRRCReadLanes, job) {
			t.Errorf("%s enables the remote repo contents cache (%v) but is not a reading lane whose configs %s seeds", job, how, bazelRRCLaneCmds)
		}
		var configs []string
		for _, s := range wf.Jobs[job].Steps {
			for _, inv := range invocation.FindAllString(s.Run, -1) {
				for _, m := range config.FindAllStringSubmatch(inv, -1) {
					if m[1] != "sole-run" && m[1] != "fresh" { // test-result options, nothing fetched
						configs = append(configs, m[1])
					}
				}
			}
		}
		if len(configs) == 0 {
			t.Errorf("%s enables the remote repo contents cache (%v) but no `bazel test|build --config=` is visible in its steps, so its configs cannot be checked against %s", job, how, bazelRRCLaneCmds)
		}
		for _, c := range configs {
			used[c] = true
			if !seeded[c] {
				t.Errorf("%s enables the remote repo contents cache (%v) and runs bazel with --config=%s, which %s does not seed", job, how, c, bazelRRCLaneCmds)
			}
		}
	}
	if len(enabling) < len(bazelRRCReadLanes) || len(used) < len(bazelRRCReadLanes) {
		t.Fatalf("found %d jobs enabling the cache and the configs %v; the scan is broken", len(enabling), used)
	}
	for c := range seeded {
		if !used[c] {
			t.Errorf("%s seeds --config=%s, which no reading lane uses", bazelRRCLaneCmds, c)
		}
	}
}

// TestRRCRunLanes runs rrc-run-lanes.sh with a stub bazel: one --nobuild
// invocation per lane command, in order, startup flags first and command
// flags after; `test --nobuild`'s exit 1 after a successful analysis
// passes, a failed analysis fails, and an empty command list fails.
func TestRRCRunLanes(t *testing.T) {
	root := sourceRepoRoot(t)
	script := filepath.Join(root, bazelRRCRunLanes)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>" + calls + "\n" +
		"if [ -n \"${BAZEL_TEST_FAIL:-}\" ]; then echo 'ERROR: no such package'; echo 'ERROR: Build did NOT complete successfully'; exit 1; fi\n" +
		"case \"$*\" in *' build '*) echo 'INFO: Build completed successfully, 0 total actions'; exit 0 ;; esac\n" +
		"echo 'INFO: Build completed successfully, 0 total actions'\necho \"ERROR: Couldn't start the build. Unable to run tests\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "bazel"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"PATH":                bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_STEP_SUMMARY": filepath.Join(dir, "summary"),
	}
	run := "bash " + script + " --experimental_remote_repo_contents_cache -- --loading_phase_threads=64 --remote_upload_local_results"
	if out, err := runRRCScript(t, dir, run, env); err != nil {
		t.Fatalf("rrc-run-lanes.sh: %v\n%s", err, out)
	}
	var want []string
	for _, cmd := range bazelRRCLaneCommands(t, root) {
		want = append(want, "--experimental_remote_repo_contents_cache "+cmd+" --nobuild --loading_phase_threads=64 --remote_upload_local_results")
	}
	if got := strings.Split(strings.TrimSpace(readPolicyFile(t, dir, "calls")), "\n"); !slices.Equal(got, want) {
		t.Errorf("bazel calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if out, err := runRRCScript(t, dir, "bash "+script+" --", env); err != nil {
		t.Fatalf("without flags: %v\n%s", err, out)
	}
	if got := strings.Split(strings.TrimSpace(readPolicyFile(t, dir, "calls")), "\n"); len(got) == 0 || !strings.HasPrefix(got[0], "test ") || !strings.HasSuffix(got[0], " --nobuild") {
		t.Errorf("without flags, first call %q", got)
	}

	failEnv := map[string]string{"BAZEL_TEST_FAIL": "1"}
	for k, v := range env {
		failEnv[k] = v
	}
	if out, err := runRRCScript(t, dir, run, failEnv); err == nil {
		t.Errorf("a failed analysis passed:\n%s", out)
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("# nothing\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyEnv := map[string]string{"RRC_LANE_COMMANDS": empty}
	for k, v := range env {
		emptyEnv[k] = v
	}
	if out, err := runRRCScript(t, dir, run, emptyEnv); err == nil {
		t.Errorf("no lane commands passed; it must fail rather than seed nothing:\n%s", out)
	}
	if out, err := runRRCScript(t, dir, "bash "+script+" --flag", env); err == nil {
		t.Errorf("no -- separator passed:\n%s", out)
	}
}

// TestBazelRRCSeedJob: the only writer runs on push to main in mode remote
// with rrc not off, holds the only id-token permission in bazel.yml, reads
// no secret (its credential is the OIDC-minted certificate), sets Bazel up
// in local mode, seeds only with a certificate, uploads to the cache alone
// (no executor), and always removes the key.
func TestBazelRRCSeedJob(t *testing.T) {
	wf := readCIWorkflow(t, bazelWorkflowName)
	job, ok := wf.Jobs[bazelRRCSeedJob]
	if !ok {
		t.Fatalf("%s has no %s job", bazelWorkflowName, bazelRRCSeedJob)
	}
	if job.If != bazelRRCSeedJobIf {
		t.Errorf("%s if %q, want %q", bazelRRCSeedJob, job.If, bazelRRCSeedJobIf)
	}
	if !slices.Equal(job.Needs, []string{bazelRBEJobName}) || job.Outputs != nil {
		t.Errorf("%s needs %v outputs %v; it needs the rbe job alone and reports nothing to the gate", bazelRRCSeedJob, job.Needs, job.Outputs)
	}
	for _, c := range []struct {
		event, ref, mode, rrc string
		want                  bool
	}{
		{"push", "refs/heads/main", "remote", "seed", true},
		{"push", "refs/heads/main", "remote", "canary", true},
		{"push", "refs/heads/main", "remote", "on", true},
		{"push", "refs/heads/main", "remote", "off", false},
		{"push", "refs/heads/main", "cache", "on", false},
		{"push", "refs/heads/other", "remote", "on", false},
		{"pull_request", "refs/pull/1/merge", "remote", "on", false},
		{"pull_request_target", "refs/heads/main", "remote", "on", false},
		{"merge_group", "refs/heads/gh-readonly-queue/main/pr-1-abc", "remote", "on", false},
		{"workflow_dispatch", "refs/heads/main", "remote", "on", false},
		{"schedule", "refs/heads/main", "remote", "on", false},
	} {
		got := evalRRCIf(t, job.If, map[string]string{
			"github.event_name": c.event, "github.ref": c.ref, "needs.rbe.outputs.mode": c.mode, "needs.rbe.outputs.rrc": c.rrc,
		})
		if got != c.want {
			t.Errorf("%+v: seed runs %v, want %v", c, got, c.want)
		}
	}
	if want := map[string]any{"contents": "read", "id-token": "write"}; !reflect.DeepEqual(job.Permissions, want) {
		t.Errorf("%s permissions %v, want %v", bazelRRCSeedJob, job.Permissions, want)
	}
	raw := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)
	if text := bazelRRCJobText(t, raw, bazelRRCSeedJob); strings.Contains(text, "secrets.") {
		t.Errorf("%s reads a secret; its only credential is the OIDC-minted writer certificate", bazelRRCSeedJob)
	}
	setup := rrcStep(t, job, "setup-bazel", func(s ciWorkflowStep) bool { return s.Uses == bazelSetupBazelUses })
	if len(setup.Env) != 0 {
		t.Errorf("%s setup-bazel env %v; local mode (no executor, no certificate) only", bazelRRCSeedJob, setup.Env)
	}
	cred := rrcStep(t, job, "writer certificate", func(s ciWorkflowStep) bool { return s.ID == "writer" })
	if cred.Run != "bash "+bazelRRCCredential {
		t.Errorf("writer step runs %q, want bash %s", cred.Run, bazelRRCCredential)
	}
	seed := rrcStep(t, job, "seed", func(s ciWorkflowStep) bool { return strings.Contains(s.Run, bazelRRCRunLanes) })
	if seed.If != "${{ steps.writer.outputs.cert != '' }}" {
		t.Errorf("seed step if %q; it must skip when the mint switched seeding off (no certificate)", seed.If)
	}
	for _, want := range []string{
		"bash " + bazelRRCRunLanes + " --experimental_remote_repo_contents_cache --", "--loading_phase_threads=64",
		`"--remote_cache=$ENDPOINT"`, `"--remote_instance_name=$INSTANCE"`,
		`"--tls_client_certificate=$CERT"`, `"--tls_client_key=$KEY"`, "--remote_upload_local_results",
	} {
		if !strings.Contains(seed.Run, want) {
			t.Errorf("seed step lacks %q:\n%s", want, seed.Run)
		}
	}
	if strings.Contains(seed.Run, "remote_executor") {
		t.Errorf("seed step names an executor; the writer certificate is cache only")
	}
	cleanup := rrcStep(t, job, "key removal", func(s ciWorkflowStep) bool { return strings.Contains(s.Run, "rrc-writer.key") })
	if cleanup.If != "${{ always() }}" {
		t.Errorf("the writer key removal runs if %q; it must always run", cleanup.If)
	}
}

// bazelRRCJobText returns a job's raw text in a workflow.
func bazelRRCJobText(t *testing.T, raw, job string) string {
	t.Helper()
	start := strings.Index(raw, "\n  "+job+":\n")
	if start < 0 {
		t.Fatalf("cannot find the %s job's text", job)
	}
	text := raw[start+1:]
	if next := regexp.MustCompile(`\n  [a-z0-9-]+:\n`).FindStringIndex(text); next != nil {
		text = text[:next[0]]
	}
	return text
}

// TestBazelRRCVerifyJob: the nightly check runs from nightly.yml (schedule)
// or a dispatch while the cache is in use, fetches every lane cold without
// the repo contents cache, compares with rrc_verify.py using the lanes'
// read-only certificate and setup-bazel's endpoint (no secret outside
// setup-bazel), and alerts through an issue only when the comparison fails.
func TestBazelRRCVerifyJob(t *testing.T) {
	wf := readCIWorkflow(t, bazelWorkflowName)
	job, ok := wf.Jobs[bazelRRCVerifyJob]
	if !ok {
		t.Fatalf("%s has no %s job", bazelWorkflowName, bazelRRCVerifyJob)
	}
	if job.If != bazelRRCVerifyJobIf {
		t.Errorf("%s if %q, want %q", bazelRRCVerifyJob, job.If, bazelRRCVerifyJobIf)
	}
	if job.Outputs != nil {
		t.Errorf("%s outputs %v; it reports nothing to the gate", bazelRRCVerifyJob, job.Outputs)
	}
	for _, c := range []struct {
		event, mode, rrc string
		want             bool
	}{
		{"schedule", "remote", "on", true},
		{"schedule", "remote", "seed", true},
		{"workflow_dispatch", "remote", "canary", true},
		{"schedule", "remote", "off", false},
		{"schedule", "cache", "on", false},
		{"push", "remote", "on", false},
		{"pull_request", "remote", "on", false},
		{"pull_request_target", "remote", "on", false},
		{"merge_group", "remote", "on", false},
	} {
		got := evalRRCIf(t, job.If, map[string]string{
			"github.event_name": c.event, "needs.rbe.outputs.mode": c.mode, "needs.rbe.outputs.rrc": c.rrc,
		})
		if got != c.want {
			t.Errorf("%+v: verify runs %v, want %v", c, got, c.want)
		}
	}
	if want := map[string]any{"contents": "read", "issues": "write"}; !reflect.DeepEqual(job.Permissions, want) {
		t.Errorf("%s permissions %v, want %v", bazelRRCVerifyJob, job.Permissions, want)
	}
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "experimental_remote_repo_contents_cache") || strings.Contains(s.Run, ".bazelrc.local") ||
			strings.Contains(s.Run, "remote_upload_local_results") {
			t.Errorf("%s step %q reads or writes the repo contents cache; the cold fetch must not", bazelRRCVerifyJob, s.Name)
		}
		for k, v := range s.Env {
			if strings.Contains(v, "secrets.") && s.Uses != bazelSetupBazelUses {
				t.Errorf("%s step %q env %s reads a secret; only setup-bazel may", bazelRRCVerifyJob, s.Name, k)
			}
		}
	}
	fetch := rrcStep(t, job, "cold fetch", func(s ciWorkflowStep) bool { return strings.HasPrefix(s.Name, "Cold fetch") })
	if fetch.Run != "bash "+bazelRRCRunLanes+" -- --loading_phase_threads=64" {
		t.Errorf("cold fetch runs %q; it must run every lane's command with no startup flag", fetch.Run)
	}
	verify := rrcStep(t, job, "verify", func(s ciWorkflowStep) bool { return s.ID == "verify" })
	for _, want := range []string{"python3 " + bazelRRCVerifyTool, `--cert "$secret_dir/client.crt"`, `--key "$secret_dir/client.key"`, "--instance oss", ".bazelversion", "remote_executor="} {
		if !strings.Contains(verify.Run, want) {
			t.Errorf("verify step lacks %q", want)
		}
	}
	alert := rrcStep(t, job, "alert", func(s ciWorkflowStep) bool { return strings.Contains(s.Run, "gh issue") })
	if alert.If != "${{ failure() && steps.verify.outcome == 'failure' }}" {
		t.Errorf("alert step if %q; it must alert only on a failed comparison", alert.If)
	}
	if strings.Contains(alert.Run, "${{") {
		t.Errorf("alert step interpolates an expression into its script; pass it through env")
	}
	// The issue's containment: mode cache lanes see no repository
	// variables, so the committed switches (both repositories) and
	// rbe-cache-gate stop them. The mode remote readers read the same
	// entries, so a mismatch sets the variable to seed (no reader, rrc-seed
	// and this check keep running); off stops seeding and this check too.
	for _, want := range []string{
		"fork_rrc_read=off in both probe scripts", cacheRRCProbe, "gascity tools/rbe/cache-rrc-probe.sh",
		"beads has no fresh-merge", "rbe-cache-gate close", "on a mismatch, set RBE_REPO_CONTENTS_CACHE to seed",
		"set it to off only to stop seeding too", "could not check, leave the variable as it is",
	} {
		if !strings.Contains(alert.Run, want) {
			t.Errorf("alert issue body lacks %q", want)
		}
	}
	for _, bad := range []string{"Stop readers first: set the repository variable", "leave RBE_REPO_CONTENTS_CACHE on"} {
		if strings.Contains(alert.Run, bad) {
			t.Errorf("alert issue body still says %q; a mismatch sets the variable to seed", bad)
		}
	}
}

// permissionRank orders GitHub token permission levels so a grant can be
// compared with a request: write covers read, read covers none.
var permissionRank = map[string]int{"": 0, "none": 0, "read": 1, "write": 2}

type permWorkflow struct {
	Jobs map[string]struct {
		Uses        string            `yaml:"uses"`
		Permissions map[string]string `yaml:"permissions"`
	} `yaml:"jobs"`
}

// TestBazelWorkflowCallersGrantEveryRequestedPermission guards against
// startup_failure (gascity #7478): GitHub validates a reusable workflow's
// job permissions when the caller starts, including jobs whose `if:` would
// skip, so every workflow that calls bazel.yml must grant at least the
// highest level any bazel.yml job requests for each scope.
func TestBazelWorkflowCallersGrantEveryRequestedPermission(t *testing.T) {
	root := sourceRepoRoot(t)
	load := func(name string) permWorkflow {
		var wf permWorkflow
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, root, ".github/workflows/"+name)), &wf); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return wf
	}
	need := map[string]string{}
	for _, job := range load(bazelWorkflowName).Jobs {
		for scope, level := range job.Permissions {
			if permissionRank[level] > permissionRank[need[scope]] {
				need[scope] = level
			}
		}
	}
	if need["id-token"] != "write" || need["issues"] != "write" {
		t.Fatalf("bazel.yml requests %v; the parser is broken or the rrc jobs are gone", need)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	var callers []string
	for _, e := range entries {
		name := e.Name()
		if name == bazelWorkflowName || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		for jobName, job := range load(name).Jobs {
			if job.Uses != "./.github/workflows/"+bazelWorkflowName {
				continue
			}
			callers = append(callers, name)
			var missing []string
			for scope, level := range need {
				if permissionRank[job.Permissions[scope]] < permissionRank[level] {
					missing = append(missing, scope+": "+level)
				}
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				t.Errorf("%s job %q calls %s without granting %s (startup_failure)", name, jobName, bazelWorkflowName, strings.Join(missing, ", "))
			}
		}
	}
	sort.Strings(callers)
	if want := []string{"bazel-farm.yml", "nightly.yml", "pr.yml"}; !slices.Equal(callers, want) {
		t.Errorf("bazel.yml callers %v, want %v", callers, want)
	}
}

// rrcMintCurlStub stands in for curl in rrc-writer-credential.sh. The OIDC
// request (its URL carries audience=) logs the audience and authorization
// and answers a token. A mint request logs its Authorization header and
// answers as the space-separated RBE_TEST_MINT says, one word per request
// (the last repeats): ok (a certificate for the runner's key, CN
// rbe-rrc-writer-beads O=gascity), cn, endpoint, instance, otherkey, nocert
// (each wrong in that one way), 000 (connection refused) or an HTTP status
// with an error body.
const rrcMintCurlStub = `#!/usr/bin/env bash
set -euo pipefail
out= url= headers=()
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift 2 ;;
	-H) headers+=("$2"); shift 2 ;;
	-w | --data | --connect-timeout | --max-time | --retry) shift 2 ;;
	-*) shift ;;
	*) url=$1; shift ;;
	esac
done
case "$url" in
*audience=*)
	echo "oidc audience=${url##*audience=} auth=${headers[0]}" >>"$RBE_TEST_MINT_LOG"
	echo '{"value": "oidc-jwt-value"}'
	exit 0
	;;
esac
echo "mint $url auth=${headers[0]}" >>"$RBE_TEST_MINT_LOG"
n=$(grep -c '^mint ' "$RBE_TEST_MINT_LOG")
read -r -a answers <<<"$RBE_TEST_MINT"
i=$((n - 1)); [ "$i" -lt "${#answers[@]}" ] || i=$((${#answers[@]} - 1))
answer=${answers[$i]}
key="$BAZEL_CI_SECRET_DIR/rrc-writer.key" cn=rbe-rrc-writer-beads endpoint=grpcs://rbe-west.ops.gascity.com:443 instance=oss
case "$answer" in
ok | cn | endpoint | instance | otherkey | nocert) ;;
000) echo "curl: (7) Failed to connect" >&2; exit 7 ;;
*) printf '{"error": "stub %s"}' "$answer" >"$out"; printf '%s' "$answer"; exit 0 ;;
esac
case "$answer" in
cn) cn=rbe-ci ;;
endpoint) endpoint=grpcs://attacker.example:443 ;;
instance) instance=main ;;
otherkey) openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out other.key 2>/dev/null; key=other.key ;;
esac
pem=
if [ "$answer" != nocert ]; then
	pem=$(openssl req -new -x509 -key "$key" -subj "/O=gascity/CN=$cn" -days 1 2>/dev/null)
fi
jq -cn --arg pem "$pem" --arg e "$endpoint" --arg i "$instance" --arg cn "$cn" \
	'{cert_pem: $pem, endpoint: $e, instance: $i, cn: $cn}' >"$out"
printf 200
`

// TestRRCWriterCredential runs rrc-writer-credential.sh against a stubbed
// curl (rrcMintCurlStub): it asks GitHub for an OIDC token with audience
// rbe-rrc-writer, masks it, sends it to the mint, retries only 429, 502 and
// the network, treats 503 or an unreachable mint as seeding off (no
// outputs, exit 0), and accepts only a certificate for the key it generated,
// CN rbe-rrc-writer[-x] O=gascity, for rbe-west's endpoint and instance oss.
func TestRRCWriterCredential(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "openssl"} {
		requireHostTool(t, tool)
	}
	script := filepath.Join(sourceRepoRoot(t), bazelRRCCredential)
	for _, c := range []struct {
		answers string
		ok, off bool
		mints   int
	}{
		{"ok", true, false, 1},
		{"429 ok", true, false, 2},
		{"502 ok", true, false, 2},
		{"000 ok", true, false, 2},
		{"502", false, false, 4},
		{"403", false, false, 1},
		{"401", false, false, 1},
		{"409", false, false, 1},
		{"cn", false, false, 1},
		{"endpoint", false, false, 1},
		{"instance", false, false, 1},
		{"otherkey", false, false, 1},
		{"nocert", false, false, 1},
		{"503", true, true, 1},
		{"000", true, true, 4},
	} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"curl": rrcMintCurlStub, "sleep": "#!/bin/sh\nexit 0\n"} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		secret := filepath.Join(t.TempDir(), "secret")
		output := filepath.Join(dir, "output")
		out, err := runRRCScript(t, dir, "bash "+script, map[string]string{
			"PATH":                           bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"BAZEL_CI_SECRET_DIR":            secret,
			"GITHUB_OUTPUT":                  output,
			"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://token.invalid/oidc?api-version=2.0",
			"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
			"RBE_TEST_MINT":                  c.answers,
			"RBE_TEST_MINT_LOG":              filepath.Join(dir, "mint.log"),
		})
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v, want %v\n%s", c.answers, err == nil, c.ok, out)
			continue
		}
		log := readPolicyFile(t, dir, "mint.log")
		if !strings.Contains(log, "oidc audience=rbe-rrc-writer auth=Authorization: bearer request-token\n") {
			t.Errorf("%q: OIDC request %q", c.answers, log)
		}
		if got := strings.Count(log, "mint https://rbe-mint.ops.gascity.com:8444/v1/rrc-writer/cert auth=Authorization: Bearer oidc-jwt-value\n"); got != c.mints {
			t.Errorf("%q: %d mint requests with the OIDC token, want %d:\n%s", c.answers, got, c.mints, log)
		}
		if !strings.Contains(out, "::add-mask::oidc-jwt-value") {
			t.Errorf("%q: the OIDC token is not masked:\n%s", c.answers, out)
		}
		if !c.ok {
			continue
		}
		if c.off {
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Errorf("%q: wrote outputs (%v); the seed must skip", c.answers, err)
			}
			if !strings.Contains(out, "seeding off (HTTP "+c.answers[len(c.answers)-3:]+")") {
				t.Errorf("%q: no seeding-off warning:\n%s", c.answers, out)
			}
			continue
		}
		got := rrcOutputs(t, output)
		for name, want := range map[string]string{
			"cert": filepath.Join(secret, "rrc-writer.crt"), "key": filepath.Join(secret, "rrc-writer.key"),
			"endpoint": "grpcs://rbe-west.ops.gascity.com:443", "instance": "oss",
		} {
			if got[name] != want {
				t.Errorf("%q: output %s=%q, want %q", c.answers, name, got[name], want)
			}
		}
		if key := readPolicyFile(t, secret, "rrc-writer.key"); !strings.Contains(key, "-----BEGIN PRIVATE KEY-----") {
			t.Errorf("%q: key is not PKCS#8 (Bazel's TLS refuses SEC1)", c.answers)
		}
		if fi, err := os.Stat(filepath.Join(secret, "rrc-writer.key")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%q: key mode %v (%v), want 0600", c.answers, fi, err)
		}
		if _, err := os.Stat(filepath.Join(secret, "rrc-mint.json")); !os.IsNotExist(err) {
			t.Errorf("%q: the mint reply stays on disk (%v)", c.answers, err)
		}
	}

	dir := t.TempDir()
	if out, err := runRRCScript(t, dir, "bash "+script, map[string]string{
		"BAZEL_CI_SECRET_DIR": filepath.Join(t.TempDir(), "secret"), "GITHUB_OUTPUT": filepath.Join(dir, "output"),
	}); err == nil || !strings.Contains(out, "id-token: write") {
		t.Errorf("without an OIDC request URL: err %v, want a failure naming id-token: write\n%s", err, out)
	}
}

// The writer credential script and rrc_verify.py with its unit test are byte
// copies of gascity's (gascity 78c0e5c6f198306b96fe854c18b4d72f16395ce7,
// #7467), canonical there like tools/bazel/ci_analytics_extract.py: the
// pins catch a local edit that would drift out of lockstep.
var bazelRRCVendored = map[string]string{
	bazelRRCCredential: "021788a724876bd1df7e5c117a4b3e1e60c5b25bb18bca472d61c35a08cb1fac",
	bazelRRCVerifyTool: "00d50e569e2a50d8ef3d2740460de7337b71afd14dc76d2d2442dbd162236f94",
	bazelRRCVerifyTest: "d934f13b466660f3148cf3c8dc7273803b4417f2a52f4edc2dd2f43afb93bfaa",
}

func TestBazelRRCVendoredFromGascity(t *testing.T) {
	root := sourceRepoRoot(t)
	for path, want := range bazelRRCVendored {
		sum := sha256.Sum256([]byte(readPolicyFile(t, root, path)))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s sha256 = %s, want %s: it drifted from gascity's copy, or gascity changed it and this pin (and the copy) needs updating", path, got, want)
		}
	}
}

// TestRRCVerifyUnitTests runs gascity's rrc_verify_test.py in place: the
// AC key construction pinned against Bazel 9.3.0's own writes, the
// roll-forward of intermediate entries, a poisoned entry, and a GUID for
// .bazelversion's release.
func TestRRCVerifyUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, filepath.Join(root, bazelRRCVerifyTest), "-v")
	cmd.Dir = filepath.Join(root, "tools", "bazel")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", bazelRRCVerifyTest, err, out)
	}
}
