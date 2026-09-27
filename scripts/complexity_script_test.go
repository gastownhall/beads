package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeComplexityOutput = `31 main Keep cmd/example.go:12:1
22 workapi BuildListFilter internal/workapi/list.go:99:1
9 main Small cmd/small.go:4:1
45 main TestHelper cmd/helper_test.go:8:1
50 main Conformance backend/conformance/check.go:3:1
32 main Backend backend/live.go:7:1
`

func TestComplexityScriptReportFiltersAndComparesBaseline(t *testing.T) {
	repo := sourceRepoRoot(t)
	fake := filepath.Join(t.TempDir(), "fake-gocyclo")
	writeExecutable(t, fake, "#!/bin/sh\nprintf '%s\\n' "+shellQuote(fakeComplexityOutput))
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(baseline, []byte("# fixture\n30 main Keep cmd/example.go:1:1\n22 workapi BuildListFilter internal/workapi/list.go:1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runComplexityScript(t, repo, fake, baseline, "report")
	if err != nil {
		t.Fatalf("report failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "31 main Keep") || !strings.Contains(out, "22 workapi BuildListFilter") {
		t.Fatalf("report omitted tracked functions:\n%s", out)
	}
	if strings.Contains(out, "Small") || strings.Contains(out, "TestHelper") || strings.Contains(out, "Conformance") {
		t.Fatalf("report included below-threshold/test fixture:\n%s", out)
	}
	if !strings.Contains(out, "32 main Backend") {
		t.Fatalf("report omitted shipped backend function:\n%s", out)
	}
	if !strings.Contains(out, "regressed: 31 main Keep") {
		t.Fatalf("report did not identify regression:\n%s", out)
	}
}

func TestComplexityScriptCheckFailsOnRegression(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-gocyclo")
	writeExecutable(t, fake, "#!/bin/sh\nprintf '%s\\n' '31 main Keep cmd/example.go:12:1'")
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(baseline, []byte("30 main Keep cmd/example.go:1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runComplexityScript(t, sourceRepoRoot(t), fake, baseline, "check")
	if err == nil {
		t.Fatalf("check unexpectedly passed:\n%s", out)
	}
	if !strings.Contains(out, "regressed: 31 main Keep") {
		t.Fatalf("check did not explain regression:\n%s", out)
	}
}

func TestComplexityScriptReportsNewFunctionAdvisory(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-gocyclo")
	writeExecutable(t, fake, "#!/bin/sh\nprintf '%s\\n' '31 main New cmd/new.go:12:1'")
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(baseline, []byte("# empty fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runComplexityScript(t, sourceRepoRoot(t), fake, baseline, "report")
	if err != nil {
		t.Fatalf("report failed for new function: %v\n%s", err, out)
	}
	if !strings.Contains(out, "new: 31 main New") {
		t.Fatalf("report did not identify new function:\n%s", out)
	}
}

func TestComplexityScriptHonorsThreshold(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-gocyclo")
	writeExecutable(t, fake, "#!/bin/sh\nprintf '%s\\n' '21 main Near cmd/near.go:1:1' '31 main Over cmd/over.go:2:1'")
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(baseline, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runComplexityScriptWithThreshold(t, sourceRepoRoot(t), fake, baseline, "report", "30")
	if err != nil {
		t.Fatalf("threshold report failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "Near") || !strings.Contains(out, "Over") {
		t.Fatalf("threshold was not applied:\n%s", out)
	}
	if !strings.Contains(out, "new: 31 main Over") {
		t.Fatalf("empty baseline was not handled as empty:\n%s", out)
	}
}

func TestComplexityScriptDiffReportsCrossingAndTrueDeletionOnly(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-gocyclo")
	phase := filepath.Join(t.TempDir(), "complexity-phase")
	if err := os.WriteFile(phase, []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The phase file is an explicit head/base discriminator. This keeps the
	// fixture independent of checkout directory names and worktree layout.
	writeExecutable(t, fake, "#!/bin/sh\nif [ \"$(cat "+shellQuote(phase)+")\" = head ]; then\nprintf '%s\\n' '31 main Crossing cmd/cross.go:2:1' '10 main Drop cmd/drop.go:2:1'\nprintf '%s\\n' base >"+shellQuote(phase)+"\nelse\nprintf '%s\\n' '25 main Crossing cmd/cross.go:1:1' '58 main Drop cmd/drop.go:1:1' '40 main Gone cmd/gone.go:1:1' '12 main Quiet cmd/quiet.go:1:1'\nprintf '%s\\n' head >"+shellQuote(phase)+"\nfi\n")
	out, err := runComplexityDiff(t, sourceRepoRoot(t), fake)
	if err != nil {
		t.Fatalf("diff failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "regressed: 31 main Crossing") || !strings.Contains(out, "baseline 25") {
		t.Fatalf("diff omitted threshold crossing:\n%s", out)
	}
	if !strings.Contains(out, "deleted:") || !strings.Contains(out, "Gone") {
		t.Fatalf("diff omitted true deletion:\n%s", out)
	}
	if !strings.Contains(out, "improved: 10 main Drop") || !strings.Contains(out, "baseline 58") {
		t.Fatalf("diff omitted drop below threshold:\n%s", out)
	}
	if strings.Contains(out, "Quiet") || strings.Contains(out, "below threshold") {
		t.Fatalf("diff emitted low-complexity base function:\n%s", out)
	}
}

func TestComplexityScriptFailsClearlyWhenToolMissing(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	out, err := runComplexityScript(t, sourceRepoRoot(t), filepath.Join(t.TempDir(), "missing-gocyclo"), baseline, "report")
	if err == nil {
		t.Fatalf("report unexpectedly passed without analyzer:\n%s", out)
	}
	if !strings.Contains(out, "is required") || !strings.Contains(out, "v0.6.0") {
		t.Fatalf("missing-tool error lacks pinned install guidance:\n%s", out)
	}
}

func runComplexityScript(t *testing.T, repo, tool, baseline, mode string) (string, error) {
	return runComplexityScriptWithThreshold(t, repo, tool, baseline, mode, "20")
}

func runComplexityScriptWithThreshold(t *testing.T, repo, tool, baseline, mode, threshold string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repo, "scripts/ci/complexity.sh"), mode)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"COMPLEXITY_TOOL="+tool,
		"COMPLEXITY_BASELINE="+baseline,
		"COMPLEXITY_THRESHOLD="+threshold,
	)
	data, err := cmd.CombinedOutput()
	return string(data), err
}

func runComplexityDiff(t *testing.T, repo, tool string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repo, "scripts/ci/complexity.sh"), "diff")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "COMPLEXITY_TOOL="+tool, "COMPLEXITY_BASE_REF=HEAD", "COMPLEXITY_THRESHOLD=30")
	data, err := cmd.CombinedOutput()
	return string(data), err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
