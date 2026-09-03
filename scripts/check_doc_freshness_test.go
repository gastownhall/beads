//go:build integration

package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var freshnessDocuments = []struct {
	path    string
	sources []string
}{
	{"docs/reference/configuration.md", []string{"cmd/bd/main.go", "cmd/bd/config.go", "internal/configfile/"}},
	{"docs/getting-started/ide-setup.md", []string{"cmd/bd/setup*.go", "internal/recipes/"}},
	{"docs/integrations/azure-devops.md", []string{"cmd/bd/ado*.go", "internal/ado/"}},
	{"docs/reference/json-schema.md", []string{"cmd/bd/output.go", "cmd/bd/errors.go", "cmd/bd/protocol/json_contract_test.go"}},
	{"docs/recovery/init-safety.md", []string{"cmd/bd/init.go", "cmd/bd/init_safety.go", "cmd/bd/init_safety_test.go"}},
	{"engdocs/ERROR_HANDLING.md", []string{"cmd/bd/*.go", "cmd/bd/errors.go"}},
	{"engdocs/SERVE_RUNBOOK.md", []string{"internal/httpapi/server.go", "internal/httpapi/events_watch.go", "cmd/bd/serve.go", "internal/httpapi/auth.go"}},
	{"engdocs/LINTING.md", []string{".golangci.yml", "scripts/ci/pr-lint.sh", "Makefile", ".github/workflows/pr.yml", ".github/workflows/main.yml"}},
	{"engdocs/CI_CLEANUP_PLAN.md", []string{"engdocs/CI_TEST_SURFACE_AUDIT.md", ".github/workflows/*.yml", ".buildflags", ".golangci.yml", "scripts/ci/pr-lint.sh", "Makefile"}},
	{"engdocs/design/otel/otel-data-model.md", []string{"internal/telemetry/", "internal/storage/dolt/store.go", "internal/compact/haiku.go", "cmd/bd/find_duplicates.go", "internal/hooks/"}},
}

type freshnessRun struct {
	output        string
	err           error
	pythonInvoked bool
	dateInvoked   bool
}

const pythonPoisonFunction = `python3() {
    printf 'invoked\n' >"$PYTHON_POISON_MARKER"
    return 97
}
`

const brokenDateFunction = `
date() {
    printf 'invoked\n' >"$DATE_BACKEND_MARKER"
    return 69
}
`

const garbageDateFunction = `
date() {
    printf 'invoked\n' >"$DATE_BACKEND_MARKER"
    printf 'garbage\n'
}
`

const recordingNativeDateFunction = `
date() {
    printf 'invoked\n' >"$DATE_BACKEND_MARKER"
    command date "$@"
}
`

func TestDocFreshnessDoesNotRequirePython(t *testing.T) {
	run := runDocFreshness(t, "2026-01-15", "2026-01-31", "90")
	if run.err != nil {
		t.Fatalf("check-doc-freshness failed without Python: %v\n%s", run.err, run.output)
	}
	if run.pythonInvoked {
		t.Fatalf("check-doc-freshness invoked python3:\n%s", run.output)
	}
	for _, want := range []string{
		"PASS: Last reviewed marker is current: 2026-01-15 (16 days old)",
		"PASSED: Reference doc freshness markers are current and checkable",
	} {
		if !strings.Contains(run.output, want) {
			t.Fatalf("missing %q:\n%s", want, run.output)
		}
	}
}

func TestDocFreshnessValidatesMaxAge(t *testing.T) {
	t.Run("unset uses default", func(t *testing.T) {
		run := runDocFreshnessWithoutMaxAge(t, "2026-01-15", "2026-04-15")
		if run.err != nil {
			t.Fatalf("check-doc-freshness rejected the default max age: %v\n%s", run.err, run.output)
		}
		if !strings.Contains(run.output, "Max age: 90 days") {
			t.Fatalf("missing default max-age result:\n%s", run.output)
		}
	})

	valid := []struct {
		name     string
		reviewed string
		today    string
		maxAge   string
		want     string
	}{
		{name: "empty uses default", reviewed: "2026-01-15", today: "2026-01-31", maxAge: "", want: "Max age: 90 days"},
		{name: "zero", reviewed: "2026-01-15", today: "2026-01-15", maxAge: "0", want: "Max age: 0 days"},
		{name: "leading zeroes", reviewed: "2026-01-15", today: "2026-01-31", maxAge: "000000090", want: "Max age: 90 days"},
		{name: "full calendar span", reviewed: "0001-01-01", today: "9999-12-31", maxAge: "3652058", want: "PASS: Last reviewed marker is current: 0001-01-01 (3652058 days old)"},
		{name: "larger than Bash integer range", reviewed: "2026-01-15", today: "2026-01-31", maxAge: strings.Repeat("9", 100), want: "PASS: Last reviewed marker is current: 2026-01-15 (16 days old)"},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			run := runDocFreshness(t, test.reviewed, test.today, test.maxAge)
			if run.err != nil {
				t.Fatalf("check-doc-freshness rejected max age %q: %v\n%s", test.maxAge, run.err, run.output)
			}
			if !strings.Contains(run.output, test.want) {
				t.Fatalf("missing %q:\n%s", test.want, run.output)
			}
		})
	}

	invalid := []struct {
		name   string
		maxAge string
	}{
		{name: "negative", maxAge: "-1"},
		{name: "signed", maxAge: "+90"},
		{name: "expression", maxAge: "1+2"},
		{name: "variable reference", maxAge: "TODAY_DAY"},
		{name: "whitespace", maxAge: " 90"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			run := runDocFreshness(t, "2026-01-15", "2026-01-31", test.maxAge)
			if exitCode(run.err) != 2 {
				t.Fatalf("exit = %d, want 2; error=%v\n%s", exitCode(run.err), run.err, run.output)
			}
			if !strings.Contains(run.output, "ERROR: DOC_FRESHNESS_MAX_AGE_DAYS must be a nonnegative decimal integer") {
				t.Fatalf("missing max-age diagnostic:\n%s", run.output)
			}
			if strings.Contains(run.output, "Checking reference doc freshness markers") {
				t.Fatalf("invalid max age reached document checks:\n%s", run.output)
			}
		})
	}

	t.Run("long leading-zero value keeps decimal semantics", func(t *testing.T) {
		run := runDocFreshness(t, "2026-01-15", "2026-01-31", strings.Repeat("0", 100)+"15")
		if exitCode(run.err) != 1 {
			t.Fatalf("exit = %d, want 1; error=%v\n%s", exitCode(run.err), run.err, run.output)
		}
		if !strings.Contains(run.output, "Max age: 15 days") ||
			!strings.Contains(run.output, "FAIL: Last reviewed date is stale: 2026-01-15 (16 days old)") {
			t.Fatalf("long leading-zero threshold did not compare as decimal 15:\n%s", run.output)
		}
	})
}

func TestDocFreshnessBashProbeIgnoresStartupEnvironment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Git Bash discovery is Windows-specific")
	}

	poison := filepath.Join(t.TempDir(), "poison.sh")
	if err := os.WriteFile(poison, []byte("exit 97\n"), 0o600); err != nil {
		t.Fatalf("write startup poison: %v", err)
	}
	t.Setenv("BASH_ENV", poison)
	t.Setenv("ENV", poison)

	if bash := docFreshnessBash(t); bash == "" {
		t.Fatal("docFreshnessBash returned an empty executable")
	}
}

func TestDocFreshnessPreservesDateDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		reviewed string
		today    string
		maxAge   string
		want     string
	}{
		{
			name:     "invalid date",
			reviewed: "2026-02-30",
			today:    "2026-03-01",
			maxAge:   "90",
			want:     "FAIL: invalid Last reviewed date: 2026-02-30",
		},
		{
			name:     "year zero",
			reviewed: "0000-01-01",
			today:    "2026-03-01",
			maxAge:   "90",
			want:     "FAIL: invalid Last reviewed date: 0000-01-01",
		},
		{
			name:     "future date",
			reviewed: "2026-03-02",
			today:    "2026-03-01",
			maxAge:   "90",
			want:     "FAIL: Last reviewed date is in the future: 2026-03-02",
		},
		{
			name:     "stale date",
			reviewed: "2025-01-01",
			today:    "2026-03-01",
			maxAge:   "30",
			want:     "FAIL: Last reviewed date is stale: 2025-01-01",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runDocFreshness(t, test.reviewed, test.today, test.maxAge)
			if exitCode(run.err) != 1 {
				t.Fatalf("exit = %d, want 1; error=%v\n%s", exitCode(run.err), run.err, run.output)
			}
			if run.pythonInvoked {
				t.Fatalf("check-doc-freshness invoked python3:\n%s", run.output)
			}
			if !strings.Contains(run.output, test.want) {
				t.Fatalf("missing %q:\n%s", test.want, run.output)
			}
		})
	}
}

func TestDocFreshnessPreservesISODateBoundaries(t *testing.T) {
	for _, value := range []string{"0001-01-01", "2000-02-29", "9999-12-31"} {
		t.Run(value, func(t *testing.T) {
			run := runDocFreshness(t, value, value, "90")
			if run.err != nil {
				t.Fatalf("check-doc-freshness rejected supported boundary %s: %v\n%s", value, run.err, run.output)
			}
			if !strings.Contains(run.output, "PASS: Last reviewed marker is current: "+value+" (0 days old)") {
				t.Fatalf("missing boundary date result for %s:\n%s", value, run.output)
			}
		})
	}
}

func TestDocFreshnessUsesProlepticGregorianLeapRules(t *testing.T) {
	tests := []struct {
		name     string
		reviewed string
		today    string
		wantExit int
		want     string
	}{
		{
			name:     "common century",
			reviewed: "1900-02-28",
			today:    "1900-03-01",
			wantExit: 0,
			want:     "PASS: Last reviewed marker is current: 1900-02-28 (1 days old)",
		},
		{
			name:     "leap century",
			reviewed: "2000-02-28",
			today:    "2000-03-01",
			wantExit: 0,
			want:     "PASS: Last reviewed marker is current: 2000-02-28 (2 days old)",
		},
		{
			name:     "ordinary leap year",
			reviewed: "2024-02-28",
			today:    "2024-03-01",
			wantExit: 0,
			want:     "PASS: Last reviewed marker is current: 2024-02-28 (2 days old)",
		},
		{
			name:     "invalid common-century leap day",
			reviewed: "1900-02-29",
			today:    "1900-03-01",
			wantExit: 1,
			want:     "FAIL: invalid Last reviewed date: 1900-02-29",
		},
		{
			name:     "invalid month zero",
			reviewed: "2026-00-01",
			today:    "2026-03-01",
			wantExit: 1,
			want:     "FAIL: invalid Last reviewed date: 2026-00-01",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runDocFreshness(t, test.reviewed, test.today, "90")
			if exitCode(run.err) != test.wantExit {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", exitCode(run.err), test.wantExit, run.err, run.output)
			}
			if run.pythonInvoked {
				t.Fatalf("check-doc-freshness invoked python3:\n%s", run.output)
			}
			if !strings.Contains(run.output, test.want) {
				t.Fatalf("missing %q:\n%s", test.want, run.output)
			}
		})
	}
}

func TestDocFreshnessUsesNativeDefaultTodayProvider(t *testing.T) {
	run := runDocFreshnessWithDefaultToday(t, "0001-01-01", "4000000", recordingNativeDateFunction)
	if run.err != nil {
		t.Fatalf("check-doc-freshness failed with native default TODAY: %v\n%s", run.err, run.output)
	}
	if !run.dateInvoked {
		t.Fatalf("check-doc-freshness did not invoke native date for the default TODAY value:\n%s", run.output)
	}
	if run.pythonInvoked {
		t.Fatalf("check-doc-freshness invoked python3:\n%s", run.output)
	}
	if !strings.Contains(run.output, "PASSED: Reference doc freshness markers are current and checkable") {
		t.Fatalf("missing successful default-TODAY result:\n%s", run.output)
	}
}

func TestDocFreshnessReportsUnavailableTodayProvider(t *testing.T) {
	run := runDocFreshnessWithDefaultToday(t, "2026-01-15", "90", brokenDateFunction)
	if exitCode(run.err) != 2 {
		t.Fatalf("exit = %d, want 2; error=%v\n%s", exitCode(run.err), run.err, run.output)
	}
	if !run.dateInvoked {
		t.Fatalf("check-doc-freshness did not invoke date for the default TODAY value:\n%s", run.output)
	}
	if !strings.Contains(run.output, "ERROR: check-doc-freshness could not determine today's date") {
		t.Fatalf("missing explicit TODAY provider diagnostic:\n%s", run.output)
	}
	if strings.Contains(run.output, "invalid Last reviewed date") {
		t.Fatalf("broken TODAY provider was mislabeled as document data:\n%s", run.output)
	}
}

func TestDocFreshnessReportsInvalidTodayProviderOutput(t *testing.T) {
	run := runDocFreshnessWithDefaultToday(t, "2026-01-15", "90", garbageDateFunction)
	if exitCode(run.err) != 2 {
		t.Fatalf("exit = %d, want 2; error=%v\n%s", exitCode(run.err), run.err, run.output)
	}
	if !run.dateInvoked {
		t.Fatalf("check-doc-freshness did not invoke date for the default TODAY value:\n%s", run.output)
	}
	if !strings.Contains(run.output, "ERROR: date returned an invalid current date: garbage") {
		t.Fatalf("missing invalid-provider-output diagnostic:\n%s", run.output)
	}
	if strings.Contains(run.output, "DOC_FRESHNESS_TODAY") {
		t.Fatalf("provider output was mislabeled as an override:\n%s", run.output)
	}
}

func TestDocFreshnessReportsInvalidTodayOverride(t *testing.T) {
	run := runDocFreshness(t, "2026-01-15", "garbage", "90")
	if exitCode(run.err) != 2 {
		t.Fatalf("exit = %d, want 2; error=%v\n%s", exitCode(run.err), run.err, run.output)
	}
	if run.dateInvoked {
		t.Fatalf("check-doc-freshness invoked date despite an explicit TODAY override:\n%s", run.output)
	}
	if !strings.Contains(run.output, "ERROR: invalid DOC_FRESHNESS_TODAY date: garbage") {
		t.Fatalf("missing invalid-override diagnostic:\n%s", run.output)
	}
}

func runDocFreshness(t *testing.T, reviewed, today, maxAge string) freshnessRun {
	t.Helper()
	return runDocFreshnessWithDateFunction(t, reviewed, today, &maxAge, "")
}

func runDocFreshnessWithoutMaxAge(t *testing.T, reviewed, today string) freshnessRun {
	t.Helper()
	return runDocFreshnessWithDateFunction(t, reviewed, today, nil, "")
}

func runDocFreshnessWithDateFunction(t *testing.T, reviewed, today string, maxAge *string, dateFunction string) freshnessRun {
	t.Helper()
	return runDocFreshnessProcess(t, reviewed, &today, maxAge, dateFunction)
}

func runDocFreshnessWithDefaultToday(t *testing.T, reviewed, maxAge, dateFunction string) freshnessRun {
	t.Helper()
	return runDocFreshnessProcess(t, reviewed, nil, &maxAge, dateFunction)
}

func runDocFreshnessProcess(t *testing.T, reviewed string, today, maxAge *string, dateFunction string) freshnessRun {
	t.Helper()
	bash := docFreshnessBash(t)

	root := newDocFreshnessFixture(t, reviewed)
	markerDir := t.TempDir()
	pythonMarker := filepath.Join(markerDir, "python-invoked")
	dateMarker := filepath.Join(markerDir, "date-invoked")
	bashEnvironment := filepath.Join(t.TempDir(), "bash-env.sh")
	if err := os.WriteFile(bashEnvironment, []byte(pythonPoisonFunction+dateFunction), 0o600); err != nil {
		t.Fatalf("write Python poison environment: %v", err)
	}

	cmd := exec.Command(
		bash,
		"--noprofile",
		"--norc",
		msysPath(filepath.Join(root, "scripts", "check-doc-freshness.sh")),
	)
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + msysPath(t.TempDir()),
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=" + msysPath(bashEnvironment),
		"ENV=",
		"DATE_BACKEND_MARKER=" + msysPath(dateMarker),
		"PYTHON_POISON_MARKER=" + msysPath(pythonMarker),
	}
	if maxAge != nil {
		cmd.Env = append(cmd.Env, "DOC_FRESHNESS_MAX_AGE_DAYS="+*maxAge)
	}
	if today != nil {
		cmd.Env = append(cmd.Env, "DOC_FRESHNESS_TODAY="+*today)
	}
	output, runErr := cmd.CombinedOutput()
	return freshnessRun{
		output:        string(output),
		err:           runErr,
		pythonInvoked: docFreshnessMarkerExists(t, pythonMarker),
		dateInvoked:   docFreshnessMarkerExists(t, dateMarker),
	}
}

func docFreshnessBash(t *testing.T) string {
	t.Helper()

	if runtime.GOOS != "windows" {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Fatalf("bash is required to exercise check-doc-freshness.sh: %v", err)
		}
		return bash
	}

	git, err := exec.LookPath("git.exe")
	if err != nil {
		t.Fatalf("Git for Windows is required to exercise check-doc-freshness.sh: %v", err)
	}
	execPathCommand := exec.Command(git, "--exec-path")
	execPathCommand.Env = docFreshnessProbeEnv()
	execPathOutput, err := execPathCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("locate the Git for Windows installation: %v: %s", err, strings.TrimSpace(string(execPathOutput)))
	}
	execPath := filepath.Clean(strings.TrimSpace(string(execPathOutput)))
	if !filepath.IsAbs(execPath) {
		t.Fatalf("Git for Windows returned a non-absolute exec path: %q", execPath)
	}
	gitRoot := filepath.Clean(filepath.Join(execPath, "..", "..", ".."))
	candidates := []string{
		filepath.Join(gitRoot, "bin", "bash.exe"),
		filepath.Join(gitRoot, "usr", "bin", "bash.exe"),
	}
	var diagnostics []string
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		probe := exec.Command(candidate, "--noprofile", "--norc", "-c", `
export BASH_ENV=
export ENV=
PATH=/usr/bin:/bin
export PATH
case "$(uname -s)" in
    MINGW*|MSYS*) command -v date >/dev/null ;;
    *) exit 1 ;;
esac
`)
		probe.Env = docFreshnessProbeEnv()
		if output, err := probe.CombinedOutput(); err == nil {
			return candidate
		} else {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v: %s", candidate, err, strings.TrimSpace(string(output))))
		}
	}

	t.Fatalf("a working Git Bash date environment is required: %s", strings.Join(diagnostics, "; "))
	return ""
}

func docFreshnessMarkerExists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("inspect marker %s: %v", path, err)
	}
	return false
}

func newDocFreshnessFixture(t *testing.T, reviewed string) string {
	t.Helper()

	root := t.TempDir()
	repoRoot := sourceRepoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "check-doc-freshness.sh"))
	if err != nil {
		t.Fatalf("read check-doc-freshness.sh: %v", err)
	}
	writeFixtureFile(t, root, "scripts/check-doc-freshness.sh", string(script))

	var inventory strings.Builder
	for _, document := range freshnessDocuments {
		inventoryRef := strings.TrimPrefix(strings.TrimPrefix(document.path, "docs/"), "engdocs/")
		fmt.Fprintf(&inventory, "- `%s`\n", inventoryRef)
		writeFixtureFile(t, root, document.path, fmt.Sprintf(
			"Last reviewed: %s\nFreshness source: %s\n",
			reviewed,
			strings.Join(document.sources, ";"),
		))
		for _, source := range document.sources {
			createFreshnessSource(t, root, source)
		}
	}
	writeFixtureFile(t, root, "engdocs/DOC_INVENTORY.md", inventory.String())
	return root
}

func createFreshnessSource(t *testing.T, root, source string) {
	t.Helper()

	if strings.HasSuffix(source, "/") {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(source)), 0o755); err != nil {
			t.Fatalf("create source directory %s: %v", source, err)
		}
		return
	}

	source = strings.ReplaceAll(source, "*", "fixture")
	source = strings.ReplaceAll(source, "?", "x")
	writeFixtureFile(t, root, source, "")
}

func writeFixtureFile(t *testing.T, root, relative, body string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

func docFreshnessProbeEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upperKey := strings.ToUpper(key)
		if upperKey == "BASH_ENV" || upperKey == "ENV" || strings.HasPrefix(upperKey, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "BASH_ENV=", "ENV=", "GIT_EXEC_PATH=")
}
