package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var selectorKeys = []string{
	"embedded_cli", "proxied_cli", "embedded_storage", "embedded_conformance", "server_conformance", "full", "reason",
}

func TestCICapabilitySelectorClassifiesOwnedPaths(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []string
		want    map[string]string
	}{
		{"docs", []string{"A", "docs/guide.md"}, decisions(false, false, false, false, false, "docs_only")},
		{"command", []string{"M", "cmd/bd/create.go"}, decisions(true, true, false, false, false, "cmd_bd")},
		{"embedded", []string{"M", "internal/storage/embeddeddolt/store.go"}, decisions(true, false, true, true, false, "embedded_storage")},
		{"server", []string{"M", "internal/storage/dolt/store.go"}, decisions(false, true, false, false, true, "server_storage")},
		{"shared conformance", []string{"M", "backend/conformance/portable.go"}, decisions(false, false, false, true, true, "shared_conformance")},
		{"mixed union", []string{"M", "cmd/bd/create.go", "M", "internal/storage/embeddeddolt/store.go"}, decisions(true, true, true, true, false, "mixed")},
		{"all leaves normalize full", []string{"M", "cmd/bd/create.go", "M", "internal/storage/embeddeddolt/store.go", "M", "internal/storage/dolt/store.go"}, decisions(true, true, true, true, true, "full")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := runCICapabilitySelector(t, tt.records, "pull_request", true, false)
			assertSelectorOutputs(t, got, tt.want)
		})
	}
}

func TestCICapabilitySelectorFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		event   string
		records []string
		bounds  bool
		gitFail bool
		reason  string
	}{
		{"push", "push", nil, true, false, "push"},
		{"merge group", "merge_group", nil, true, false, "merge_group"},
		{"unsupported event", "schedule", nil, true, false, "unsupported_event"},
		{"missing bounds", "pull_request", nil, false, false, "missing_bounds"},
		{"diff failure", "pull_request", nil, true, true, "diff_failed"},
		{"delete", "pull_request", []string{"D", "docs/guide.md"}, true, false, "unsafe_status"},
		{"rename", "pull_request", []string{"R100", "docs/old.md", "docs/new.md"}, true, false, "unsafe_status"},
		{"copy", "pull_request", []string{"C100", "docs/old.md", "docs/new.md"}, true, false, "unsafe_status"},
		{"type change", "pull_request", []string{"T", "docs/guide.md"}, true, false, "unsafe_status"},
		{"unmerged", "pull_request", []string{"U", "docs/guide.md"}, true, false, "unsafe_status"},
		{"unknown status", "pull_request", []string{"X", "docs/guide.md"}, true, false, "unsafe_status"},
		{"broken pair", "pull_request", []string{"B", "docs/guide.md"}, true, false, "unsafe_status"},
		{"malformed", "pull_request", []string{"A"}, true, false, "malformed_record"},
		{"absolute", "pull_request", []string{"A", "/tmp/unsafe"}, true, false, "unsafe_path"},
		{"traversal", "pull_request", []string{"A", "docs/../unsafe.md"}, true, false, "unsafe_path"},
		{"workflow", "pull_request", []string{"M", ".github/workflows/pr-risk.yml"}, true, false, "unsafe_control"},
		{"selector", "pull_request", []string{"M", ".github/scripts/ci-capability-selector.sh"}, true, false, "unsafe_control"},
		{"build input", "pull_request", []string{"M", "go.mod"}, true, false, "build_input"},
		{"cross cutting storage", "pull_request", []string{"M", "internal/storage/schema/migrations/1.sql"}, true, false, "cross_cutting_storage"},
		{"unowned", "pull_request", []string{"M", "internal/other/change.go"}, true, false, "unowned_path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := runCICapabilitySelector(t, tt.records, tt.event, tt.bounds, tt.gitFail)
			assertSelectorOutputs(t, got, decisions(true, true, true, true, true, tt.reason))
		})
	}
}

func TestCICapabilitySelectorNeverInjectsFilenamesIntoOutputs(t *testing.T) {
	got, raw := runCICapabilitySelector(t, []string{"A", "docs/new\nname\t%=.md"}, "pull_request", true, false)
	assertSelectorOutputs(t, got, decisions(false, false, false, false, false, "docs_only"))
	if strings.Contains(raw, "new\nname") || strings.Contains(raw, "\t%=") {
		t.Fatalf("filename leaked into selector output: %q", raw)
	}

	got, raw = runCICapabilitySelector(t, []string{"A", "unowned/new\nname\t%=.txt"}, "pull_request", true, false)
	assertSelectorOutputs(t, got, decisions(true, true, true, true, true, "unowned_path"))
	if strings.Contains(raw, "new\nname") || strings.Contains(raw, "\t%=") {
		t.Fatalf("untrusted filename leaked into selector output: %q", raw)
	}
}

func TestCICapabilitySelectorWorkflowPreservesExistingAuthority(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), ".github", "workflows", "pr-risk.yml"))
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	var workflow capabilitySelectorWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	wantJobs := map[string]bool{"detect-ci-tier": true, "build-embedded": true, "test-embedded-storage": true, "test-embedded-conformance": true, "test-server-storage": true, "test-server-storage-full": true, "test-embedded-cmd": true, "test-proxied-cmd": true, "test-nix": true, "ci-gate": true}
	if len(workflow.Jobs) != len(wantJobs) {
		t.Errorf("job count = %d, want %d", len(workflow.Jobs), len(wantJobs))
	}
	for name := range workflow.Jobs {
		if !wantJobs[name] {
			t.Errorf("unexpected job %q", name)
		}
	}
	for name := range wantJobs {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("missing job %q", name)
		}
	}
	detector := workflow.Jobs["detect-ci-tier"]
	if detector.Outputs["full_embedded"] != "${{ steps.tier.outputs.full_embedded }}" {
		t.Errorf("full_embedded output = %q", detector.Outputs["full_embedded"])
	}
	tier, shadow := detector.step(t, "Decide embedded Dolt coverage"), detector.step(t, "Shadow CI capability selector")
	if tier.ID != "tier" || shadow.ID != "shadow-selector" || !shadow.ContinueOnError || detector.stepIndex(t, shadow.Name) != detector.stepIndex(t, tier.Name)+1 {
		t.Errorf("shadow step is not immediately after tier: tier=%+v shadow=%+v", tier, shadow)
	}
	for _, name := range []string{"build-embedded", "test-embedded-storage", "test-embedded-conformance", "test-server-storage", "test-server-storage-full", "test-embedded-cmd", "test-proxied-cmd"} {
		if got := workflow.Jobs[name].If; got != "needs.detect-ci-tier.outputs.full_embedded == 'true'" {
			t.Errorf("%s if = %q", name, got)
		}
	}
	if workflow.Jobs["test-nix"].If != "" {
		t.Errorf("test-nix if = %q, want ungated", workflow.Jobs["test-nix"].If)
	}
	gate := workflow.Jobs["ci-gate"]
	wantNeeds := []string{"detect-ci-tier", "build-embedded", "test-embedded-storage", "test-embedded-conformance", "test-server-storage", "test-server-storage-full", "test-embedded-cmd", "test-proxied-cmd", "test-nix"}
	gateStep := gate.step(t, "Evaluate CI gate")
	if gate.If != "${{ always() }}" || strings.Join(gate.Needs, ",") != strings.Join(wantNeeds, ",") || gateStep.Env["FULL_EMBEDDED"] != "${{ needs.detect-ci-tier.outputs.full_embedded }}" || gateStep.Env["CI_GATE_REQUIRED"] != "DETECT_CI_TIER BUILD_EMBEDDED TEST_EMBEDDED_STORAGE TEST_EMBEDDED_CONFORMANCE TEST_SERVER_STORAGE TEST_SERVER_STORAGE_FULL TEST_EMBEDDED_CMD TEST_PROXIED_CMD TEST_NIX" || !strings.Contains(gateStep.Run, "skipped_ok=\"BUILD_EMBEDDED TEST_EMBEDDED_STORAGE TEST_EMBEDDED_CONFORMANCE TEST_SERVER_STORAGE TEST_SERVER_STORAGE_FULL TEST_EMBEDDED_CMD TEST_PROXIED_CMD\"") {
		t.Errorf("ci-gate authority changed: needs=%v env=%v", gate.Needs, gateStep.Env)
	}
	for name, job := range workflow.Jobs {
		if strings.Contains(job.If, "shadow_") {
			t.Errorf("job %q shadow condition = %q", name, job.If)
		}
		for _, step := range job.Steps {
			if strings.Contains(step.If, "shadow_") {
				t.Errorf("step %q shadow condition = %q", step.Name, step.If)
			}
			for key, value := range step.Env {
				if strings.Contains(value, "shadow_") {
					t.Errorf("step %q env %s consumes shadow output", step.Name, key)
				}
			}
		}
	}
}

type capabilitySelectorWorkflow struct {
	Jobs map[string]capabilitySelectorJob `yaml:"jobs"`
}

type capabilitySelectorJob struct {
	Needs   ciWorkflowStringList             `yaml:"needs"`
	If      string                           `yaml:"if"`
	Outputs map[string]string                `yaml:"outputs"`
	Steps   []capabilitySelectorWorkflowStep `yaml:"steps"`
}

type capabilitySelectorWorkflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	If              string            `yaml:"if"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	Run             string            `yaml:"run"`
	Env             map[string]string `yaml:"env"`
}

func (job capabilitySelectorJob) step(t *testing.T, name string) capabilitySelectorWorkflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("job has no %q step", name)
	return capabilitySelectorWorkflowStep{}
}

func (job capabilitySelectorJob) stepIndex(t *testing.T, name string) int {
	t.Helper()
	for index, step := range job.Steps {
		if step.Name == name {
			return index
		}
	}
	t.Fatalf("job has no %q step", name)
	return -1
}

func decisions(embeddedCLI, proxiedCLI, embeddedStorage, embeddedConformance, serverConformance bool, reason string) map[string]string {
	full := embeddedCLI && proxiedCLI && embeddedStorage && embeddedConformance && serverConformance
	return map[string]string{
		"embedded_cli":         boolString(embeddedCLI),
		"proxied_cli":          boolString(proxiedCLI),
		"embedded_storage":     boolString(embeddedStorage),
		"embedded_conformance": boolString(embeddedConformance),
		"server_conformance":   boolString(serverConformance),
		"full":                 boolString(full),
		"reason":               reason,
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func runCICapabilitySelector(t *testing.T, records []string, event string, bounds, gitFail bool) (map[string]string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("selector is a Bash boundary")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "diff")
	if err := os.WriteFile(fixture, append([]byte(strings.Join(records, "\x00")), 0), 0600); err != nil {
		t.Fatalf("write NUL fixture: %v", err)
	}
	fakeGit := filepath.Join(dir, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\n[ \"${FAKE_GIT_FAIL:-}\" = 1 ] && exit 1\ncat \"$FAKE_GIT_FIXTURE\"\n"), 0700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	output := filepath.Join(dir, "github-output")
	cmd := exec.Command("bash", filepath.Join("..", ".github", "scripts", "ci-capability-selector.sh"))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_EVENT_NAME="+event, "FAKE_GIT_FIXTURE="+fixture, "GITHUB_OUTPUT="+output)
	if bounds {
		cmd.Env = append(cmd.Env, "PR_BASE_SHA=base", "PR_HEAD_SHA=head")
	}
	if gitFail {
		cmd.Env = append(cmd.Env, "FAKE_GIT_FAIL=1")
	}
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("selector failed: %v", err)
	}
	outputBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read GITHUB_OUTPUT: %v", err)
	}
	if string(raw) != string(outputBytes) {
		t.Fatalf("stdout and GITHUB_OUTPUT differ:\nstdout=%q\noutput=%q", raw, outputBytes)
	}
	return parseSelectorOutputs(t, string(raw)), string(raw)
}

func parseSelectorOutputs(t *testing.T, raw string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if len(lines) != len(selectorKeys) {
		t.Fatalf("selector output has %d lines, want %d: %q", len(lines), len(selectorKeys), raw)
	}
	got := make(map[string]string, len(lines))
	for index, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != selectorKeys[index] {
			t.Fatalf("selector output line %d = %q, want %q=...", index, line, selectorKeys[index])
		}
		got[key] = value
	}
	return got
}

func assertSelectorOutputs(t *testing.T, got, want map[string]string) {
	t.Helper()
	for _, key := range selectorKeys {
		if got[key] != want[key] {
			t.Errorf("%s = %q, want %q", key, got[key], want[key])
		}
	}
}
