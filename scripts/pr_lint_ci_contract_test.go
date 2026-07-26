package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRLintStockMacMakeUsesFixedInvocationLeaf(t *testing.T) {
	hostScript := readPRLintContractFile(t, "scripts/ci/pr-lint-host.sh")
	workflow := readPRLintContractFile(t, ".github/workflows/pr.yml")
	contractDocs := map[string]string{
		"engdocs/LINTING.md": readPRLintContractFile(t, "engdocs/LINTING.md"),
		"engdocs/CI_CLEANUP_PLAN.md": readPRLintContractFile(
			t,
			"engdocs/CI_CLEANUP_PLAN.md",
		),
	}
	refusalCountContracts := map[string]string{
		"engdocs/LINTING.md":         "fifteen macOS Make/target/cleanup/producer refusals",
		"engdocs/CI_CLEANUP_PLAN.md": "fifteen refusal cases",
	}

	for _, required := range []string{
		`make_invocation_path="$make_candidate"`,
		`[[ "$make_invocation_path" == "/usr/bin/make" ]] ||`,
		`stock macOS GNU Make must be invoked through /usr/bin/make`,
		`canonical=$make_path`,
	} {
		if !strings.Contains(hostScript, required) {
			t.Fatalf("PR lint host script does not bind the stock macOS Make contract through %q", required)
		}
	}
	for _, forbidden := range []string{
		`[[ "$make_path" != "/usr/bin/make" ||`,
		`[[ "$make_invocation_path" != "/usr/bin/make" ||`,
		"apple-darwin",
	} {
		if strings.Contains(hostScript, forbidden) {
			t.Fatalf("PR lint host script retained obsolete macOS Make authority %q", forbidden)
		}
	}
	if !strings.Contains(workflow, "assert_refusal make-invocation-leaf") {
		t.Fatal("PR workflow does not falsify an alternate path to the stock macOS Make executable")
	}
	for path, content := range contractDocs {
		normalized := strings.Join(strings.Fields(content), " ")
		for _, required := range []string{
			"exact `/usr/bin/make` invocation leaf",
			"exact first-line version `GNU Make 3.81`",
			"free-form build banner",
			refusalCountContracts[path],
		} {
			if !strings.Contains(normalized, required) {
				t.Fatalf("%s does not bind the macOS Make contract through %q", path, required)
			}
		}
		for _, forbidden := range []string{"apple-darwin", "fourteen refusal"} {
			if strings.Contains(normalized, forbidden) {
				t.Fatalf("%s retains obsolete macOS Make authority %q", path, forbidden)
			}
		}
	}
}

func TestNativeToolchainPublishesTypedUTF8Payload(t *testing.T) {
	workflow := readPRLintContractFile(t, ".github/workflows/pr.yml")
	step := contractSection(
		t,
		workflow,
		"      - name: Install native GNU Make and MinGW",
		"      - name: Set up MSYS2 GNU Make",
	)

	for _, required := range []string{
		`[string]$outputPayload = (`,
		`"gcc={0}` + "`n" + `gxx={1}` + "`n" + `" -f`,
		`[IO.File]::AppendAllText(`,
		`[Text.UTF8Encoding]::new($false)`,
	} {
		if !strings.Contains(step, required) {
			t.Fatalf("native toolchain step does not publish through %q", required)
		}
	}
	if strings.Contains(step, "[IO.File]::AppendAllLines(") {
		t.Fatal("native toolchain step retained the object-array AppendAllLines overload")
	}
}

func readPRLintContractFile(t *testing.T, relativePath string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(content)
}

func contractSection(t *testing.T, content, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(content, startMarker)
	if start < 0 {
		t.Fatalf("missing contract section start %q", startMarker)
	}
	remaining := content[start+len(startMarker):]
	end := strings.Index(remaining, endMarker)
	if end < 0 {
		t.Fatalf("missing contract section end %q", endMarker)
	}
	return remaining[:end]
}
